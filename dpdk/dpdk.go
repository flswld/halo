package dpdk

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"time"
	"unsafe"
)

// #cgo CFLAGS: -msse4.2
// #cgo LDFLAGS: -Wl,--whole-archive -ldpdk -Wl,--no-whole-archive -ldl -pthread -lnuma -lm
// #include "./cgo/dpdk.c"
import "C"

type Config struct {
	GolangCpuCoreList []int  // golang侧使用的核心编号列表 每个网口两个核心
	StatsLog          bool   // 收发包统计日志
	DpdkCpuCoreList   []int  // dpdk侧使用的核心编号列表 主线程第一个核心 杂项线程第二个核心 每个网口两个核心
	DpdkMemChanNum    int    // dpdk内存通道数
	PortIdList        []int  // 使用网口id列表
	RingBufferSize    int    // 环状缓冲区大小
	DebugLog          bool   // 收发包调试日志
	IdleSleep         bool   // 空闲睡眠 降低cpu占用
	SingleCore        bool   // 单核模式 物理单核机器需要开启
	KniBypass         bool   // kni旁路目标ip 只接收来自目标ip的包 其他的包全部送到kni网卡
	KniBypassTargetIp string // kni旁路目标ip地址
}

type ring_buffer struct {
	mem_send_head         unsafe.Pointer
	mem_send_cur          unsafe.Pointer
	mem_recv_head         unsafe.Pointer
	mem_recv_cur          unsafe.Pointer
	recv_pos_pointer_addr *unsafe.Pointer
	send_pos_pointer_addr *unsafe.Pointer
}

var (
	conf              *Config       = nil
	port_ring_buffer  []ring_buffer = nil
	port_dpdk_rx_chan []chan []byte = nil
	port_dpdk_tx_chan []chan []byte = nil
	force_quit                      = false
)

// Run 启动dpdk
func Run(config *Config) {
	conf = config
	// 配置参数检查
	if !conf.SingleCore {
		if len(conf.GolangCpuCoreList) < len(conf.PortIdList)*2 || len(conf.DpdkCpuCoreList) < 2+len(conf.PortIdList)*2 {
			panic("cpu core num not enough")
		}
	} else {
		if len(conf.GolangCpuCoreList) < 1 || len(conf.DpdkCpuCoreList) < 1 {
			panic("no cpu core can use")
		}
	}
	if conf.DpdkMemChanNum < 1 || conf.DpdkMemChanNum > 4 {
		panic("dpdk mem chan num error")
	}
	if len(conf.PortIdList) == 0 {
		panic("no port can use")
	}
	if conf.RingBufferSize == 0 {
		conf.RingBufferSize = 1520 * 100
	}
	if conf.RingBufferSize < 1520 || conf.RingBufferSize%4 != 0 {
		panic("ring buffer size error")
	}
	go run_dpdk()
	// 等待DPDK启动完成
	time.Sleep(time.Second * 60)
	port_ring_buffer = make([]ring_buffer, len(conf.PortIdList))
	port_dpdk_rx_chan = make([]chan []byte, len(conf.PortIdList))
	port_dpdk_tx_chan = make([]chan []byte, len(conf.PortIdList))
	for port_index := range conf.PortIdList {
		port_ring_buffer[port_index].mem_send_head = C.mem_send_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].mem_recv_head = C.mem_recv_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].recv_pos_pointer_addr = C.recv_pos_pointer_addr(C.int(port_index))
		port_ring_buffer[port_index].send_pos_pointer_addr = C.send_pos_pointer_addr(C.int(port_index))
		port_dpdk_rx_chan[port_index] = make(chan []byte, 1024)
		port_dpdk_tx_chan[port_index] = make(chan []byte, 1024)
		go tx_pkt(port_index)
		go rx_pkt(port_index)
		if conf.DebugLog {
			if conf.RingBufferSize <= 1520*3 {
				go print_ring_buffer(port_index)
			}
		}
		if conf.StatsLog {
			go print_stats(port_index)
		}
	}
}

// Exit 停止dpdk
func Exit() {
	force_quit = true
	time.Sleep(time.Second * 1)
	for port_index := range conf.PortIdList {
		close(port_dpdk_rx_chan[port_index])
		close(port_dpdk_tx_chan[port_index])
	}
	time.Sleep(time.Second * 1)
	C.exit_signal_handler()
	time.Sleep(time.Second * 1)
	port_dpdk_rx_chan = nil
	port_dpdk_tx_chan = nil
	port_ring_buffer = nil
	conf = nil
	force_quit = false
}

// Rx 获取网口RX管道
func Rx(port_index int) chan []byte {
	return port_dpdk_rx_chan[port_index]
}

// Tx 获取网口TX管道
func Tx(port_index int) chan []byte {
	return port_dpdk_tx_chan[port_index]
}

// BindCpuCore 协程绑核
func BindCpuCore(core int) {
	runtime.LockOSThread()
	ret := C.bind_cpu_core(C.int(core))
	fmt.Printf("goroutine bind cpu core: %v, ret: %v\n", core, ret)
}

// 收包
func rx_pkt(port_index int) {
	runtime.LockOSThread()
	core := 0
	if conf.SingleCore {
		core = conf.GolangCpuCoreList[0]
	} else {
		core = conf.GolangCpuCoreList[port_index*2+0]
	}
	ret := C.bind_cpu_core(C.int(core))
	fmt.Printf("rx goroutine bind cpu core ret: %v, core: %v, port: %v\n", ret, core, conf.PortIdList[port_index])
	pkt_rx_buf := make([]uint8, 1514)
	pkt_len := uint16(0)
	for {
		if force_quit {
			break
		}
		read_recv_mem(port_index, pkt_rx_buf, &pkt_len)
		if pkt_len == 0 {
			if conf.IdleSleep {
				time.Sleep(time.Millisecond * 1)
			}
			// 单个cpu核心轮询
			continue
		}
		pkt := make([]uint8, pkt_len)
		copy(pkt, pkt_rx_buf)
		port_dpdk_rx_chan[port_index] <- pkt
		if conf.DebugLog {
			fmt.Printf("[rx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt)
		}
	}
}

// 发包
func tx_pkt(port_index int) {
	runtime.LockOSThread()
	core := 0
	if conf.SingleCore {
		core = conf.GolangCpuCoreList[0]
	} else {
		core = conf.GolangCpuCoreList[port_index*2+1]
	}
	ret := C.bind_cpu_core(C.int(core))
	fmt.Printf("tx goroutine bind cpu core ret: %v, core: %v, port: %v\n", ret, core, conf.PortIdList[port_index])
	for {
		if force_quit {
			break
		}
		pkt := <-port_dpdk_tx_chan[port_index]
		if pkt == nil {
			continue
		}
		pkt_len := len(pkt)
		if conf.DebugLog {
			fmt.Printf("[tx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt)
		}
		write_send_mem(port_index, pkt, uint16(pkt_len))
	}
}

// 打印环状缓冲区数据
func print_ring_buffer(port_index int) {
	go func() {
		ticker := time.NewTicker(time.Second)
		for {
			if force_quit {
				ticker.Stop()
				break
			}
			<-ticker.C
			fmt.Printf("\n++++++++++ mem recv ++++++++++\n")
			for offset := uintptr(0); offset < uintptr(conf.RingBufferSize); offset++ {
				byte_data := (*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_recv_head) + offset))
				fmt.Printf("%02x ", *byte_data)
			}
			fmt.Printf("\n++++++++++ mem recv ++++++++++\n")
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		for {
			if force_quit {
				ticker.Stop()
				break
			}
			<-ticker.C
			fmt.Printf("\n++++++++++ mem send ++++++++++\n")
			for offset := uintptr(0); offset < uintptr(conf.RingBufferSize); offset++ {
				byte_data := (*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_head) + offset))
				fmt.Printf("%02x ", *byte_data)
			}
			fmt.Printf("\n++++++++++ mem send ++++++++++\n")
		}
	}()
}

// 打印收发包统计信息
func print_stats(port_index int) {
	ticker := time.NewTicker(time.Second)
	for {
		if force_quit {
			ticker.Stop()
			break
		}
		<-ticker.C
		C.print_stats(C.int(port_index))
	}
}

// 构建dpdk eal参数
func build_eal_args() string {
	cpuListParam := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpuListParam += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpuListParam += ","
		}
	}
	argList := []string{
		os.Args[0],
		"-l", cpuListParam,
		"-n", strconv.Itoa(conf.DpdkMemChanNum),
		"--",
	}
	args := ""
	for i, v := range argList {
		args += v
		if i < len(argList)-1 {
			args += " "
		}
	}
	return args
}

// 运行dpdk
func run_dpdk() {
	cpuListParam := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpuListParam += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpuListParam += " "
		}
	}
	portIdListParam := ""
	for i, v := range conf.PortIdList {
		portIdListParam += strconv.Itoa(v)
		if i < len(conf.PortIdList)-1 {
			portIdListParam += " "
		}
	}
	var config C.struct_dpdk_config
	config.eal_args = C.CString(build_eal_args())
	config.cpu_core_list = C.CString(cpuListParam)
	config.port_id_list = C.CString(portIdListParam)
	config.ring_buffer_size = C.int(conf.RingBufferSize)
	config.debug_log = C.bool(conf.DebugLog)
	config.idle_sleep = C.bool(conf.IdleSleep)
	config.single_core = C.bool(conf.SingleCore)
	config.kni_bypass = C.bool(conf.KniBypass)
	config.kni_bypass_target_ip = C.CString(conf.KniBypassTargetIp)
	C.dpdk_main(&config)
	C.free(unsafe.Pointer(config.eal_args))
	C.free(unsafe.Pointer(config.cpu_core_list))
	C.free(unsafe.Pointer(config.port_id_list))
	C.free(unsafe.Pointer(config.kni_bypass_target_ip))
}

// 环状缓冲区写入
func write_send_mem_core(port_index int, data [1520]uint8, len uint16) uint8 {
	// 内存对齐
	aling_size := len % 4
	if aling_size != 0 {
		aling_size = 4 - aling_size
	}
	len += aling_size
	head_u32 := uint32(0)
	head_u32 = ((uint32(data[0])) << 0) +
		((uint32(data[1])) << 8) +
		((uint32(data[2])) << 16) +
		((uint32(data[3])) << 24)
	overflow := int32((uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(len)) -
		(uintptr(port_ring_buffer[port_index].mem_send_head) + uintptr(conf.RingBufferSize)))
	if conf.DebugLog {
		fmt.Printf("[write_send_mem_core] overflow: %d, len: %d, mem_send_cur: %p, mem_send_head: %p, send_pos_pointer: %p\n", overflow, len,
			port_ring_buffer[port_index].mem_send_cur, port_ring_buffer[port_index].mem_send_head, *port_ring_buffer[port_index].send_pos_pointer_addr)
	}
	if overflow >= 0 {
		// 有溢出
		if uintptr(port_ring_buffer[port_index].mem_send_cur) < uintptr(*port_ring_buffer[port_index].send_pos_pointer_addr) {
			// 已经处于读写指针交叉状态 丢弃数据
			return 1
		}
		if uintptr(overflow) >= (uintptr(*port_ring_buffer[port_index].send_pos_pointer_addr) - uintptr(port_ring_buffer[port_index].mem_send_head)) {
			// 即使进入读写指针交叉状态 剩余内存依然不足 丢弃数据
			return 1
		}
		head_ptr := (*uint32)(port_ring_buffer[port_index].mem_send_cur)
		// 写入头部 原子操作
		*head_ptr = head_u32
		if (int32(len) - overflow) > 4 {
			// 拷贝前半段数据
			{
				internel_slice_ptr := new(reflect.SliceHeader)
				internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(4)
				internel_slice_ptr.Len = int((int32(len) - overflow) - 4)
				internel_slice_ptr.Cap = int((int32(len) - overflow) - 4)
				slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
				copy(slice_ptr, data[4:])
			}
		}
		port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head
		// 拷贝后半段数据
		{
			internel_slice_ptr := new(reflect.SliceHeader)
			internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_send_cur)
			internel_slice_ptr.Len = int(overflow)
			internel_slice_ptr.Cap = int(overflow)
			slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
			copy(slice_ptr, data[4+((int32(len)-overflow)-4):])
		}
		port_ring_buffer[port_index].mem_send_cur = unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(overflow))
		if uintptr(port_ring_buffer[port_index].mem_send_cur) >= uintptr(port_ring_buffer[port_index].mem_send_head)+uintptr(conf.RingBufferSize) {
			port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head
		}
	} else {
		// 无溢出
		if (uintptr(port_ring_buffer[port_index].mem_send_cur) < uintptr(*port_ring_buffer[port_index].send_pos_pointer_addr)) &&
			((uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(len)) >= uintptr(*port_ring_buffer[port_index].send_pos_pointer_addr)) {
			// 读写指针交叉状态下剩余内存不足 丢弃数据
			return 1
		}
		head_ptr := (*uint32)(port_ring_buffer[port_index].mem_send_cur)
		// 写入头部 原子操作
		*head_ptr = head_u32
		{
			internel_slice_ptr := new(reflect.SliceHeader)
			internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(4)
			internel_slice_ptr.Len = int(len - 4)
			internel_slice_ptr.Cap = int(len - 4)
			slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
			copy(slice_ptr, data[4:])
		}
		port_ring_buffer[port_index].mem_send_cur = unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(len))
		if uintptr(port_ring_buffer[port_index].mem_send_cur) >= uintptr(port_ring_buffer[port_index].mem_send_head)+uintptr(conf.RingBufferSize) {
			port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head
		}
	}
	return 0
}

// 写入发送缓冲区
func write_send_mem(port_index int, data []uint8, len uint16) {
	if port_ring_buffer[port_index].mem_send_cur == nil {
		port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head
	}
	if len > 1514 {
		return
	}
	// 4字节头部 + 最大1514字节数据 + 2字节内存对齐
	data_pkg := [4 + 1514 + 2]uint8{0x00}
	// 数据长度标识
	data_pkg[0] = uint8(len)
	data_pkg[1] = uint8(len >> 8)
	// 写入完成标识
	finish_flag_pointer := (*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(2)))
	data_pkg[2] = 0x00
	// 内存对齐
	data_pkg[3] = 0x00
	// 写入数据
	copy(data_pkg[4:], data[:len])
	ret := write_send_mem_core(port_index, data_pkg, len+4)
	if ret == 1 {
		return
	}
	// 将后4个字节即长度标识与写入完成标识的内存置为0x00
	*(*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(0))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(1))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(2))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_send_cur) + uintptr(3))) = 0x00
	// 修改写入完成标识 原子操作
	*finish_flag_pointer = 0x01
	return
}

// 读取接收缓冲区
func read_recv_mem(port_index int, data []uint8, len *uint16) {
	if port_ring_buffer[port_index].mem_recv_cur == nil {
		port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head
	}
	*len = 0
	// 读取头部 原子操作
	head_ptr := (*uint32)(port_ring_buffer[port_index].mem_recv_cur)
	head_u32 := *head_ptr
	*len = uint16(head_u32)
	if *len == 0 {
		// 没有新数据
		return
	}
	finish_flag := uint8(head_u32 >> 16)
	if finish_flag == 0x00 {
		// 数据尚未写入完成
		*len = 0
		return
	}
	port_ring_buffer[port_index].mem_recv_cur = unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_recv_cur) + uintptr(4))
	if uintptr(port_ring_buffer[port_index].mem_recv_cur) >= uintptr(port_ring_buffer[port_index].mem_recv_head)+uintptr(conf.RingBufferSize) {
		port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head
	}
	overflow := int32((uintptr(port_ring_buffer[port_index].mem_recv_cur) + uintptr(*len)) -
		(uintptr(port_ring_buffer[port_index].mem_recv_head) + uintptr(conf.RingBufferSize)))
	if conf.DebugLog {
		fmt.Printf("[read_recv_mem] overflow: %d, len: %d, mem_recv_cur: %p, mem_recv_head: %p, recv_pos_pointer: %p\n", overflow, *len,
			port_ring_buffer[port_index].mem_recv_cur, port_ring_buffer[port_index].mem_recv_head, *port_ring_buffer[port_index].recv_pos_pointer_addr)
	}
	if overflow >= 0 {
		// 拷贝前半段数据
		{
			internel_slice_ptr := new(reflect.SliceHeader)
			internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_recv_cur)
			internel_slice_ptr.Len = int(int32(*len) - overflow)
			internel_slice_ptr.Cap = int(int32(*len) - overflow)
			slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
			copy(data, slice_ptr)
		}
		port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head
		// 拷贝后半段数据
		{
			internel_slice_ptr := new(reflect.SliceHeader)
			internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_recv_cur)
			internel_slice_ptr.Len = int(int32(*len) - (int32(*len) - overflow))
			internel_slice_ptr.Cap = int(int32(*len) - (int32(*len) - overflow))
			slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
			copy(data[int32(*len)-overflow:], slice_ptr)
		}
		// 内存对齐
		aling_size := overflow % 4
		if aling_size != 0 {
			aling_size = 4 - aling_size
		}
		port_ring_buffer[port_index].mem_recv_cur = unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_recv_cur) + uintptr(overflow) + uintptr(aling_size))
		if uintptr(port_ring_buffer[port_index].mem_recv_cur) >= uintptr(port_ring_buffer[port_index].mem_recv_head)+uintptr(conf.RingBufferSize) {
			port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head
		}
		*port_ring_buffer[port_index].recv_pos_pointer_addr = port_ring_buffer[port_index].mem_recv_cur
	} else {
		{
			internel_slice_ptr := new(reflect.SliceHeader)
			internel_slice_ptr.Data = uintptr(port_ring_buffer[port_index].mem_recv_cur)
			internel_slice_ptr.Len = int(*len)
			internel_slice_ptr.Cap = int(*len)
			slice_ptr := *(*[]uint8)(unsafe.Pointer(internel_slice_ptr))
			copy(data, slice_ptr)
		}
		// 内存对齐
		aling_size := *len % 4
		if aling_size != 0 {
			aling_size = 4 - aling_size
		}
		port_ring_buffer[port_index].mem_recv_cur = unsafe.Pointer(uintptr(port_ring_buffer[port_index].mem_recv_cur) + uintptr(*len) + uintptr(aling_size))
		if uintptr(port_ring_buffer[port_index].mem_recv_cur) >= uintptr(port_ring_buffer[port_index].mem_recv_head)+uintptr(conf.RingBufferSize) {
			port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head
		}
		*port_ring_buffer[port_index].recv_pos_pointer_addr = port_ring_buffer[port_index].mem_recv_cur
	}
	return
}
