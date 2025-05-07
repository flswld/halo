package dpdk

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/flswld/halo/cpu"
	"github.com/flswld/halo/mem"
)

// #cgo CFLAGS: -msse4.2
// #cgo LDFLAGS: -Wl,--whole-archive -ldpdk -Wl,--no-whole-archive -ldl -pthread -lnuma -lm
// #include "./cgo/dpdk.c"
import "C"

var (
	DefaultLogWriter io.Writer = nil
)

func Log(msg string) {
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(msg))
	}
}

type Config struct {
	DpdkCpuCoreList   []int    // dpdk侧使用的核心编号列表 主线程第一个核心 杂项线程第二个核心 每个网口两个核心
	GolangCpuCoreList []int    // golang侧使用的核心编号列表 每个网口两个核心
	DpdkMemChanNum    int      // dpdk内存通道数
	RingBufferSize    int      // 环状缓冲区大小
	PortIdList        []int    // 使用的网卡id列表
	AfPacketDevList   []string // 使用的AF_PACKET虚拟网卡列表
	StatsLog          bool     // 收发包统计日志
	DebugLog          bool     // 收发包调试日志
	IdleSleep         bool     // 空闲睡眠 降低cpu占用
	SingleCore        bool     // 单核模式 只使用cpu0
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
	kni_ring_buffer   ring_buffer
	port_dpdk_rx_chan []chan []byte = nil
	port_dpdk_tx_chan []chan []byte = nil
	kni_dpdk_rx_chan  chan []byte   = nil
	kni_dpdk_tx_chan  chan []byte   = nil
	force_quit        atomic.Bool
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
		conf.GolangCpuCoreList = []int{0}
		conf.DpdkCpuCoreList = []int{0}
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
	for {
		if C.dpdk_start == C.bool(true) {
			break
		}
		time.Sleep(time.Second * 1)
	}
	port_ring_buffer = make([]ring_buffer, len(conf.PortIdList))
	port_dpdk_rx_chan = make([]chan []byte, len(conf.PortIdList))
	port_dpdk_tx_chan = make([]chan []byte, len(conf.PortIdList))
	for port_index := range conf.PortIdList {
		port_ring_buffer[port_index].mem_send_head = C.cgo_mem_send_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].mem_recv_head = C.cgo_mem_recv_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].recv_pos_pointer_addr = C.cgo_recv_pos_pointer_addr(C.int(port_index))
		port_ring_buffer[port_index].send_pos_pointer_addr = C.cgo_send_pos_pointer_addr(C.int(port_index))
		kni_ring_buffer.mem_send_head = C.cgo_kni_mem_send_head_pointer()
		kni_ring_buffer.mem_recv_head = C.cgo_kni_mem_recv_head_pointer()
		kni_ring_buffer.recv_pos_pointer_addr = C.cgo_kni_recv_pos_pointer_addr()
		kni_ring_buffer.send_pos_pointer_addr = C.cgo_kni_send_pos_pointer_addr()
		port_dpdk_rx_chan[port_index] = make(chan []byte, 1024)
		port_dpdk_tx_chan[port_index] = make(chan []byte, 1024)
		go eth_tx_pkt(port_index)
		go eth_rx_pkt(port_index)
		if conf.DebugLog {
			if conf.RingBufferSize <= 1520*3 {
				go print_ring_buffer(&(port_ring_buffer[port_index]))
			}
		}
	}
	kni_dpdk_rx_chan = make(chan []byte, 1024)
	kni_dpdk_tx_chan = make(chan []byte, 1024)
	go kni_rx_tx_pkt()
	if conf.DebugLog {
		if conf.RingBufferSize <= 1520*3 {
			go print_ring_buffer(&(kni_ring_buffer))
		}
	}
	if conf.StatsLog {
		go print_port_stats(conf.PortIdList)
	}
}

// Exit 停止dpdk
func Exit() {
	force_quit.Store(true)
	time.Sleep(time.Second * 1)
	for port_index := range conf.PortIdList {
		close(port_dpdk_rx_chan[port_index])
		close(port_dpdk_tx_chan[port_index])
	}
	close(kni_dpdk_rx_chan)
	close(kni_dpdk_tx_chan)
	time.Sleep(time.Second * 1)
	C.cgo_exit_signal_handler()
	time.Sleep(time.Second * 1)
	port_dpdk_rx_chan = nil
	port_dpdk_tx_chan = nil
	kni_dpdk_rx_chan = nil
	kni_dpdk_tx_chan = nil
	port_ring_buffer = nil
	kni_ring_buffer = ring_buffer{}
	conf = nil
	force_quit.Store(false)
}

// RxChan 获取网卡RX管道
func RxChan(port_index int) chan []byte {
	return port_dpdk_rx_chan[port_index]
}

// TxChan 获取网卡TX管道
func TxChan(port_index int) chan []byte {
	return port_dpdk_tx_chan[port_index]
}

// KniRxChan 获取KNI网卡RX管道
func KniRxChan() chan []byte {
	return kni_dpdk_rx_chan
}

// KniTxChan 获取KNI网卡TX管道
func KniTxChan() chan []byte {
	return kni_dpdk_tx_chan
}

// 网卡收包
func eth_rx_pkt(port_index int) {
	runtime.LockOSThread()
	core := 0
	if !conf.SingleCore {
		core = conf.GolangCpuCoreList[port_index*2+0]
	}
	ret := cpu.BindCpuCore(core)
	Log(fmt.Sprintf("eth rx goroutine bind cpu core ret: %v, core: %v, port: %v\n", ret, core, conf.PortIdList[port_index]))
	pkt_rx_buf := make([]uint8, 1514)
	pkt_len := uint16(0)
	for {
		if force_quit.Load() {
			break
		}
		read_recv_mem(&(port_ring_buffer[port_index]), pkt_rx_buf, &pkt_len)
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
			Log(fmt.Sprintf("[eth rx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
		}
	}
}

// 网卡发包
func eth_tx_pkt(port_index int) {
	runtime.LockOSThread()
	core := 0
	if !conf.SingleCore {
		core = conf.GolangCpuCoreList[port_index*2+1]
	}
	ret := cpu.BindCpuCore(core)
	Log(fmt.Sprintf("eth tx goroutine bind cpu core ret: %v, core: %v, port: %v\n", ret, core, conf.PortIdList[port_index]))
	for {
		if force_quit.Load() {
			break
		}
		pkt := <-port_dpdk_tx_chan[port_index]
		if pkt == nil {
			continue
		}
		pkt_len := len(pkt)
		if conf.DebugLog {
			Log(fmt.Sprintf("[eth tx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
		}
		write_send_mem(&(port_ring_buffer[port_index]), pkt, uint16(pkt_len))
	}
}

// KNI网卡收包发包
func kni_rx_tx_pkt() {
	runtime.LockOSThread()
	core := conf.GolangCpuCoreList[0]
	ret := cpu.BindCpuCore(core)
	Log(fmt.Sprintf("kni rx tx goroutine bind cpu core ret: %v, core: %v\n", ret, core))
	pkt_rx_buf := make([]uint8, 1514)
	pkt_len := uint16(0)
	for {
		if force_quit.Load() {
			break
		}
		select {
		case pkt := <-kni_dpdk_tx_chan:
			if pkt == nil {
				continue
			}
			pkt_len := len(pkt)
			if conf.DebugLog {
				Log(fmt.Sprintf("[kni tx pkt] len: %v, data: %02x\n", pkt_len, pkt))
			}
			write_send_mem(&kni_ring_buffer, pkt, uint16(pkt_len))
		default:
			read_recv_mem(&kni_ring_buffer, pkt_rx_buf, &pkt_len)
			if pkt_len == 0 {
				time.Sleep(time.Millisecond * 1)
				// 单个cpu核心轮询
				continue
			}
			pkt := make([]uint8, pkt_len)
			copy(pkt, pkt_rx_buf)
			kni_dpdk_rx_chan <- pkt
			if conf.DebugLog {
				Log(fmt.Sprintf("[kni rx pkt] len: %v, data: %02x\n", pkt_len, pkt))
			}
		}
	}
}

// 打印环状缓冲区数据
func print_ring_buffer(buffer *ring_buffer) {
	go func() {
		ticker := time.NewTicker(time.Second)
		for {
			if force_quit.Load() {
				ticker.Stop()
				break
			}
			<-ticker.C
			Log(fmt.Sprintf("\n++++++++++ mem recv ++++++++++\n"))
			for offset := uintptr(0); offset < uintptr(conf.RingBufferSize); offset++ {
				byte_data := (*uint8)(unsafe.Pointer(uintptr(buffer.mem_recv_head) + offset))
				Log(fmt.Sprintf("%02x ", *byte_data))
			}
			Log(fmt.Sprintf("\n++++++++++ mem recv ++++++++++\n"))
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		for {
			if force_quit.Load() {
				ticker.Stop()
				break
			}
			<-ticker.C
			Log(fmt.Sprintf("\n++++++++++ mem send ++++++++++\n"))
			for offset := uintptr(0); offset < uintptr(conf.RingBufferSize); offset++ {
				byte_data := (*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_head) + offset))
				Log(fmt.Sprintf("%02x ", *byte_data))
			}
			Log(fmt.Sprintf("\n++++++++++ mem send ++++++++++\n"))
		}
	}()
}

// 打印网卡收发包统计信息
func print_port_stats(port_index_list []int) {
	ticker := time.NewTicker(time.Second)
	for {
		if force_quit.Load() {
			ticker.Stop()
			break
		}
		<-ticker.C
		for _, port_index := range port_index_list {
			var msg [1 * mem.KB]C.char
			C.cgo_print_stats(C.int(port_index), (*C.char)(&msg[0]))
			Log(C.GoString((*C.char)(&msg[0])))
		}
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
	}
	for index, afPacketDev := range conf.AfPacketDevList {
		argList = append(argList, "--vdev=net_af_packet"+strconv.Itoa(index)+",iface="+afPacketDev)
	}
	argList = append(argList, "--")
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
	C.cgo_dpdk_main(&config)
	C.free(unsafe.Pointer(config.eal_args))
	C.free(unsafe.Pointer(config.cpu_core_list))
	C.free(unsafe.Pointer(config.port_id_list))
}

// 环状缓冲区写入
func write_send_mem_core(buffer *ring_buffer, data [1520]uint8, len uint16) uint8 {
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
	overflow := int32((uintptr(buffer.mem_send_cur) + uintptr(len)) -
		(uintptr(buffer.mem_send_head) + uintptr(conf.RingBufferSize)))
	if conf.DebugLog {
		Log(fmt.Sprintf("[write_send_mem_core] buffer: %p, overflow: %d, len: %d, mem_send_cur: %p, mem_send_head: %p, send_pos_pointer: %p\n", buffer, overflow, len,
			buffer.mem_send_cur, buffer.mem_send_head, *(buffer.send_pos_pointer_addr)))
	}
	if overflow >= 0 {
		// 有溢出
		if uintptr(buffer.mem_send_cur) < uintptr(*(buffer.send_pos_pointer_addr)) {
			// 已经处于读写指针交叉状态 丢弃数据
			return 1
		}
		if uintptr(overflow) >= (uintptr(*(buffer.send_pos_pointer_addr)) - uintptr(buffer.mem_send_head)) {
			// 即使进入读写指针交叉状态 剩余内存依然不足 丢弃数据
			return 1
		}
		head_ptr := (*uint32)(buffer.mem_send_cur)
		// 写入头部 原子操作
		*head_ptr = head_u32
		if (int32(len) - overflow) > 4 {
			// 拷贝前半段数据
			{
				src := data[4:]
				mem.MemCpy(unsafe.Pointer(uintptr(buffer.mem_send_cur)+uintptr(4)), unsafe.Pointer(&src[0]), uint64((int32(len)-overflow)-4))
			}
		}
		buffer.mem_send_cur = buffer.mem_send_head
		// 拷贝后半段数据
		{
			src := data[4+((int32(len)-overflow)-4):]
			mem.MemCpy(buffer.mem_send_cur, unsafe.Pointer(&src[0]), uint64(overflow))
		}
		buffer.mem_send_cur = unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(overflow))
		if uintptr(buffer.mem_send_cur) >= uintptr(buffer.mem_send_head)+uintptr(conf.RingBufferSize) {
			buffer.mem_send_cur = buffer.mem_send_head
		}
	} else {
		// 无溢出
		if (uintptr(buffer.mem_send_cur) < uintptr(*(buffer.send_pos_pointer_addr))) &&
			((uintptr(buffer.mem_send_cur) + uintptr(len)) >= uintptr(*(buffer.send_pos_pointer_addr))) {
			// 读写指针交叉状态下剩余内存不足 丢弃数据
			return 1
		}
		head_ptr := (*uint32)(buffer.mem_send_cur)
		// 写入头部 原子操作
		*head_ptr = head_u32
		{
			src := data[4:]
			mem.MemCpy(unsafe.Pointer(uintptr(buffer.mem_send_cur)+uintptr(4)), unsafe.Pointer(&src[0]), uint64(len-4))
		}
		buffer.mem_send_cur = unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(len))
		if uintptr(buffer.mem_send_cur) >= uintptr(buffer.mem_send_head)+uintptr(conf.RingBufferSize) {
			buffer.mem_send_cur = buffer.mem_send_head
		}
	}
	return 0
}

// 写入发送缓冲区
func write_send_mem(buffer *ring_buffer, data []uint8, len uint16) {
	if buffer.mem_send_cur == nil {
		buffer.mem_send_cur = buffer.mem_send_head
	}
	if len > 1514 {
		return
	}
	// 4字节头部 + 最大1514字节数据 + 2字节内存对齐
	data_pkt := [4 + 1514 + 2]uint8{0x00}
	// 数据长度标识
	data_pkt[0] = uint8(len)
	data_pkt[1] = uint8(len >> 8)
	// 写入完成标识
	finish_flag_pointer := (*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(2)))
	data_pkt[2] = 0x00
	// 内存对齐
	data_pkt[3] = 0x00
	// 写入数据
	copy(data_pkt[4:], data[:len])
	ret := write_send_mem_core(buffer, data_pkt, len+4)
	if ret == 1 {
		return
	}
	// 将后4个字节即长度标识与写入完成标识的内存置为0x00
	*(*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(0))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(1))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(2))) = 0x00
	*(*uint8)(unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(3))) = 0x00
	// 修改写入完成标识 原子操作
	*finish_flag_pointer = 0x01
}

// 读取接收缓冲区
func read_recv_mem(buffer *ring_buffer, data []uint8, len *uint16) {
	if buffer.mem_recv_cur == nil {
		buffer.mem_recv_cur = buffer.mem_recv_head
	}
	*len = 0
	// 读取头部 原子操作
	head_ptr := (*uint32)(buffer.mem_recv_cur)
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
	buffer.mem_recv_cur = unsafe.Pointer(uintptr(buffer.mem_recv_cur) + uintptr(4))
	if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(conf.RingBufferSize) {
		buffer.mem_recv_cur = buffer.mem_recv_head
	}
	overflow := int32((uintptr(buffer.mem_recv_cur) + uintptr(*len)) -
		(uintptr(buffer.mem_recv_head) + uintptr(conf.RingBufferSize)))
	if conf.DebugLog {
		Log(fmt.Sprintf("[read_recv_mem] buffer: %p, overflow: %d, len: %d, mem_recv_cur: %p, mem_recv_head: %p, recv_pos_pointer: %p\n", buffer, overflow, *len,
			buffer.mem_recv_cur, buffer.mem_recv_head, *(buffer.recv_pos_pointer_addr)))
	}
	if overflow >= 0 {
		// 拷贝前半段数据
		{
			mem.MemCpy(unsafe.Pointer(&data[0]), buffer.mem_recv_cur, uint64(int32(*len)-overflow))
		}
		buffer.mem_recv_cur = buffer.mem_recv_head
		// 拷贝后半段数据
		{
			dst := data[int32(*len)-overflow:]
			mem.MemCpy(unsafe.Pointer(&dst[0]), buffer.mem_recv_cur, uint64(int32(*len)-(int32(*len)-overflow)))
		}
		// 内存对齐
		aling_size := overflow % 4
		if aling_size != 0 {
			aling_size = 4 - aling_size
		}
		buffer.mem_recv_cur = unsafe.Pointer(uintptr(buffer.mem_recv_cur) + uintptr(overflow) + uintptr(aling_size))
		if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(conf.RingBufferSize) {
			buffer.mem_recv_cur = buffer.mem_recv_head
		}
		*(buffer.recv_pos_pointer_addr) = buffer.mem_recv_cur
	} else {
		{
			mem.MemCpy(unsafe.Pointer(&data[0]), buffer.mem_recv_cur, uint64(*len))
		}
		// 内存对齐
		aling_size := *len % 4
		if aling_size != 0 {
			aling_size = 4 - aling_size
		}
		buffer.mem_recv_cur = unsafe.Pointer(uintptr(buffer.mem_recv_cur) + uintptr(*len) + uintptr(aling_size))
		if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(conf.RingBufferSize) {
			buffer.mem_recv_cur = buffer.mem_recv_head
		}
		*(buffer.recv_pos_pointer_addr) = buffer.mem_recv_cur
	}
}
