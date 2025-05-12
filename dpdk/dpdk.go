package dpdk

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"time"
	"unsafe"

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
	DpdkCpuCoreList []int    // dpdk使用的核心编号列表 主线程第一个核心 杂项线程第二个核心 每个网卡两个核心
	DpdkMemChanNum  int      // dpdk内存通道数
	RingBufferSize  int      // 环状缓冲区大小
	PortIdList      []int    // 使用的网卡id列表
	AfPacketDevList []string // 使用的AF_PACKET虚拟网卡列表
	StatsLog        bool     // 收发包统计日志
	DebugLog        bool     // 收发包调试日志
	IdleSleep       bool     // 空闲睡眠 降低cpu占用
	SingleCore      bool     // 单核模式 只使用cpu0
}

var (
	conf             *Config       = nil
	port_ring_buffer []ring_buffer = nil
	port_pkt_rx_buf  [][]byte      = nil
	kni_ring_buffer  ring_buffer
	kni_pkt_rx_buf   []byte = nil
	running          atomic.Bool
)

// Run 启动dpdk
func Run(config *Config) {
	conf = config
	// 配置参数检查
	if !conf.SingleCore {
		if len(conf.DpdkCpuCoreList) < 2+len(conf.PortIdList)*2 {
			panic("cpu core num not enough")
		}
	} else {
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
	port_pkt_rx_buf = make([][]byte, len(conf.PortIdList))
	for port_index := range conf.PortIdList {
		port_ring_buffer[port_index].mem_send_head = C.cgo_mem_send_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].mem_recv_head = C.cgo_mem_recv_head_pointer(C.int(port_index))
		port_ring_buffer[port_index].recv_pos_pointer_addr = C.cgo_recv_pos_pointer_addr(C.int(port_index))
		port_ring_buffer[port_index].send_pos_pointer_addr = C.cgo_send_pos_pointer_addr(C.int(port_index))
		port_ring_buffer[port_index].size = uint64(conf.RingBufferSize)
		port_pkt_rx_buf[port_index] = make([]byte, 1514)
		if conf.DebugLog {
			if conf.RingBufferSize <= 1520*3 {
				go func() {
					ticker := time.NewTicker(time.Second)
					for {
						if !running.Load() {
							ticker.Stop()
							break
						}
						debug_print_ring_buffer(&(port_ring_buffer[port_index]))
						<-ticker.C
					}
				}()
			}
		}
	}
	kni_ring_buffer.mem_send_head = C.cgo_kni_mem_send_head_pointer()
	kni_ring_buffer.mem_recv_head = C.cgo_kni_mem_recv_head_pointer()
	kni_ring_buffer.recv_pos_pointer_addr = C.cgo_kni_recv_pos_pointer_addr()
	kni_ring_buffer.send_pos_pointer_addr = C.cgo_kni_send_pos_pointer_addr()
	kni_ring_buffer.size = uint64(conf.RingBufferSize)
	kni_pkt_rx_buf = make([]byte, 1514)
	if conf.DebugLog {
		if conf.RingBufferSize <= 1520*3 {
			go func() {
				ticker := time.NewTicker(time.Second)
				for {
					if !running.Load() {
						ticker.Stop()
						break
					}
					debug_print_ring_buffer(&(kni_ring_buffer))
					<-ticker.C
				}
			}()
		}
	}
	if conf.StatsLog {
		go print_port_stats(conf.PortIdList)
	}
	running.Store(true)
}

// Exit 停止dpdk
func Exit() {
	running.Store(false)
	C.cgo_exit_signal_handler()
	time.Sleep(time.Second * 1)
	port_ring_buffer = nil
	port_pkt_rx_buf = nil
	kni_ring_buffer = ring_buffer{}
	kni_pkt_rx_buf = nil
	conf = nil
}

// EthRxPkt 网卡收包
func EthRxPkt(port_index int) (pkt []byte) {
	pkt_rx_buf := port_pkt_rx_buf[port_index]
	pkt_len := uint16(0)
	buffer := &(port_ring_buffer[port_index])
	read_recv_mem(buffer, pkt_rx_buf, &pkt_len)
	if pkt_len == 0 {
		if conf.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
		// 单个cpu核心轮询
		return nil
	}
	pkt = pkt_rx_buf[:pkt_len]
	if conf.DebugLog {
		Log(fmt.Sprintf("[eth rx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
	}
	return pkt
}

// EthTxPkt 网卡发包
func EthTxPkt(port_index int, pkt []byte) {
	pkt_len := len(pkt)
	buffer := &(port_ring_buffer[port_index])
	write_send_mem(buffer, pkt, uint16(pkt_len))
	if conf.DebugLog {
		Log(fmt.Sprintf("[eth tx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
	}
}

// KniRxPkt KNI网卡收包
func KniRxPkt() (pkt []byte) {
	pkt_rx_buf := kni_pkt_rx_buf
	pkt_len := uint16(0)
	buffer := &(kni_ring_buffer)
	read_recv_mem(buffer, pkt_rx_buf, &pkt_len)
	if pkt_len == 0 {
		if conf.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
		// 单个cpu核心轮询
		return nil
	}
	pkt = pkt_rx_buf[:pkt_len]
	if conf.DebugLog {
		Log(fmt.Sprintf("[kni rx pkt] len: %v, data: %02x\n", pkt_len, pkt))
	}
	return pkt
}

// KniTxPkt KNI网卡发包
func KniTxPkt(pkt []byte) {
	pkt_len := len(pkt)
	buffer := &(kni_ring_buffer)
	write_send_mem(buffer, pkt, uint16(pkt_len))
	if conf.DebugLog {
		Log(fmt.Sprintf("[kni tx pkt] len: %v, data: %02x\n", pkt_len, pkt))
	}
}

// 打印网卡收发包统计信息
func print_port_stats(port_index_list []int) {
	ticker := time.NewTicker(time.Second)
	for {
		if !running.Load() {
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

type ring_buffer struct {
	mem_send_head         unsafe.Pointer
	mem_send_cur          unsafe.Pointer
	mem_recv_head         unsafe.Pointer
	mem_recv_cur          unsafe.Pointer
	recv_pos_pointer_addr *unsafe.Pointer
	send_pos_pointer_addr *unsafe.Pointer
	size                  uint64
}

// 写入发送缓冲区
func write_send_mem(buffer *ring_buffer, data []uint8, len uint16) {
	if buffer.mem_send_cur == nil {
		buffer.mem_send_cur = buffer.mem_send_head
	}
	if len > 1514 {
		return
	}
	// 4字节头部
	head := [4]uint8{uint8(len), uint8(len >> 8), 0x00, 0x00}
	head_ptr := (*uint32)(buffer.mem_send_cur)
	_len := len + 4
	// 内存对齐
	aling_size := _len % 4
	if aling_size != 0 {
		aling_size = 4 - aling_size
	}
	_len += aling_size
	overflow := int32((uintptr(buffer.mem_send_cur) + uintptr(_len)) - (uintptr(buffer.mem_send_head) + uintptr(buffer.size)))
	if conf.DebugLog {
		Log(fmt.Sprintf("[write_send_mem] buffer: %p, overflow: %d, len: %d, mem_send_cur: %p, mem_send_head: %p, send_pos_pointer: %p\n", buffer, overflow, len,
			buffer.mem_send_cur, buffer.mem_send_head, *(buffer.send_pos_pointer_addr)))
	}
	if overflow >= 0 {
		// 有溢出
		if uintptr(buffer.mem_send_cur) < uintptr(*(buffer.send_pos_pointer_addr)) {
			// 已经处于读写指针交叉状态 丢弃数据
			return
		}
		if uintptr(overflow) >= (uintptr(*(buffer.send_pos_pointer_addr)) - uintptr(buffer.mem_send_head)) {
			// 即使进入读写指针交叉状态 剩余内存依然不足 丢弃数据
			return
		}
		// 写入头部 原子操作
		head_u32 := ((uint32(head[0])) << 0) + ((uint32(head[1])) << 8) + ((uint32(head[2])) << 16) + ((uint32(head[3])) << 24)
		atomic.StoreUint32(head_ptr, head_u32)
		if (int32(_len) - overflow) > 4 {
			// 拷贝前半段数据
			mem.MemCpy(unsafe.Pointer(uintptr(buffer.mem_send_cur)+uintptr(4)), unsafe.Pointer(&data[0]), uint64(int32(len)-overflow))
		}
		buffer.mem_send_cur = buffer.mem_send_head
		if overflow > 0 {
			// 拷贝后半段数据
			mem.MemCpy(buffer.mem_send_cur, unsafe.Pointer(&data[(int32(len)-overflow)]), uint64(overflow))
			buffer.mem_send_cur = unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(overflow))
			if uintptr(buffer.mem_send_cur) >= uintptr(buffer.mem_send_head)+uintptr(buffer.size) {
				buffer.mem_send_cur = buffer.mem_send_head
			}
		}
	} else {
		// 无溢出
		if (uintptr(buffer.mem_send_cur) < uintptr(*(buffer.send_pos_pointer_addr))) &&
			((uintptr(buffer.mem_send_cur) + uintptr(_len)) >= uintptr(*(buffer.send_pos_pointer_addr))) {
			// 读写指针交叉状态下剩余内存不足 丢弃数据
			return
		}
		// 写入头部 原子操作
		head_u32 := ((uint32(head[0])) << 0) + ((uint32(head[1])) << 8) + ((uint32(head[2])) << 16) + ((uint32(head[3])) << 24)
		atomic.StoreUint32(head_ptr, head_u32)
		mem.MemCpy(unsafe.Pointer(uintptr(buffer.mem_send_cur)+uintptr(4)), unsafe.Pointer(&data[0]), uint64(len))
		buffer.mem_send_cur = unsafe.Pointer(uintptr(buffer.mem_send_cur) + uintptr(_len))
		if uintptr(buffer.mem_send_cur) >= uintptr(buffer.mem_send_head)+uintptr(buffer.size) {
			buffer.mem_send_cur = buffer.mem_send_head
		}
	}
	// 将后面的数据长度与写入完成标识置为0x00
	atomic.StoreUint32((*uint32)(buffer.mem_send_cur), 0x00)
	// 修改写入完成标识 原子操作
	head[2] = 0x01
	head_u32 := ((uint32(head[0])) << 0) + ((uint32(head[1])) << 8) + ((uint32(head[2])) << 16) + ((uint32(head[3])) << 24)
	atomic.StoreUint32(head_ptr, head_u32)
}

// 读取接收缓冲区
func read_recv_mem(buffer *ring_buffer, data []uint8, len *uint16) {
	if buffer.mem_recv_cur == nil {
		buffer.mem_recv_cur = buffer.mem_recv_head
	}
	*len = 0
	// 读取头部 原子操作
	head_ptr := (*uint32)(buffer.mem_recv_cur)
	head_u32 := atomic.LoadUint32(head_ptr)
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
	if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(buffer.size) {
		buffer.mem_recv_cur = buffer.mem_recv_head
	}
	overflow := int32((uintptr(buffer.mem_recv_cur) + uintptr(*len)) - (uintptr(buffer.mem_recv_head) + uintptr(buffer.size)))
	if conf.DebugLog {
		Log(fmt.Sprintf("[read_recv_mem] buffer: %p, overflow: %d, len: %d, mem_recv_cur: %p, mem_recv_head: %p, recv_pos_pointer: %p\n", buffer, overflow, *len,
			buffer.mem_recv_cur, buffer.mem_recv_head, *(buffer.recv_pos_pointer_addr)))
	}
	if overflow >= 0 {
		// 拷贝前半段数据
		mem.MemCpy(unsafe.Pointer(&data[0]), buffer.mem_recv_cur, uint64(int32(*len)-overflow))
		buffer.mem_recv_cur = buffer.mem_recv_head
		if overflow > 0 {
			// 拷贝后半段数据
			mem.MemCpy(unsafe.Pointer(&data[int32(*len)-overflow]), buffer.mem_recv_cur, uint64(overflow))
			// 内存对齐
			aling_size := overflow % 4
			if aling_size != 0 {
				aling_size = 4 - aling_size
			}
			buffer.mem_recv_cur = unsafe.Pointer(uintptr(buffer.mem_recv_cur) + uintptr(overflow) + uintptr(aling_size))
			if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(buffer.size) {
				buffer.mem_recv_cur = buffer.mem_recv_head
			}
		}
	} else {
		mem.MemCpy(unsafe.Pointer(&data[0]), buffer.mem_recv_cur, uint64(*len))
		// 内存对齐
		aling_size := *len % 4
		if aling_size != 0 {
			aling_size = 4 - aling_size
		}
		buffer.mem_recv_cur = unsafe.Pointer(uintptr(buffer.mem_recv_cur) + uintptr(*len) + uintptr(aling_size))
		if uintptr(buffer.mem_recv_cur) >= uintptr(buffer.mem_recv_head)+uintptr(buffer.size) {
			buffer.mem_recv_cur = buffer.mem_recv_head
		}
	}
	atomic.StorePointer(buffer.recv_pos_pointer_addr, buffer.mem_recv_cur)
}

// 打印环状缓冲区数据
func debug_print_ring_buffer(buffer *ring_buffer) {
	Log(fmt.Sprintf("\n++++++++++ mem recv ++++++++++\n"))
	for offset := uint64(0); offset < buffer.size; offset++ {
		data := (*uint8)(mem.Offset(buffer.mem_recv_head, offset))
		Log(fmt.Sprintf("%02x ", *data))
	}
	Log(fmt.Sprintf("\n++++++++++ mem recv ++++++++++\n"))
	Log(fmt.Sprintf("\n++++++++++ mem send ++++++++++\n"))
	for offset := uint64(0); offset < buffer.size; offset++ {
		data := (*uint8)(mem.Offset(buffer.mem_send_head, offset))
		Log(fmt.Sprintf("%02x ", *data))
	}
	Log(fmt.Sprintf("\n++++++++++ mem send ++++++++++\n"))
}
