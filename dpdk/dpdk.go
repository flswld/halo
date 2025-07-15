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

	"github.com/flswld/halo/mem"
)

// #cgo pkg-config: libdpdk
// #include "../cgo/dpdk.c"
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
	DpdkCpuCoreList []int    // dpdk使用的核心编号列表 主线程第一个核心 每个网卡队列一个核心
	DpdkMemChanNum  int      // dpdk内存通道数
	PortIdList      []int    // 使用的网卡id列表
	QueueNum        int      // 启用的网卡队列数
	RingBufferSize  int      // 环状缓冲区大小
	AfPacketDevList []string // 使用的af_packet虚拟网卡列表
	StatsLog        bool     // 收发包统计日志
	DebugLog        bool     // 收发包调试日志
	IdleSleep       bool     // 空闲睡眠 降低cpu占用
	SingleCore      bool     // 单核模式 只使用cpu0
	KniEnable       bool     // 开启kni内核网卡
}

type ring_buffer struct {
	send_ring_buffer *C.ring_buffer_t
	recv_ring_buffer *C.ring_buffer_t
}

var (
	conf             *Config       = nil
	port_ring_buffer []ring_buffer = nil
	port_pkt_rx_buf  [][]byte      = nil
	kni_ring_buffer  ring_buffer
	kni_pkt_rx_buf   []byte = nil
	running          atomic.Bool
)

// Run 启动DPDK
func Run(config *Config) {
	conf = config
	// 配置参数检查
	if conf.DpdkMemChanNum == 0 {
		conf.DpdkMemChanNum = 1
	}
	if conf.QueueNum == 0 {
		conf.QueueNum = 1
	}
	if conf.RingBufferSize == 0 {
		conf.RingBufferSize = 128 * mem.MB
	}
	if !conf.SingleCore {
		if len(conf.DpdkCpuCoreList) < 1+len(conf.PortIdList)*2*conf.QueueNum {
			panic("cpu core num not enough")
		}
	} else {
		conf.DpdkCpuCoreList = []int{0}
		conf.QueueNum = 1
	}
	if conf.DpdkMemChanNum < 1 || conf.DpdkMemChanNum > 4 {
		panic("dpdk mem chan num error")
	}
	if len(conf.PortIdList) == 0 {
		panic("no port can use")
	}
	if conf.RingBufferSize&(conf.RingBufferSize-1) != 0 {
		panic("ring buffer size error")
	}
	go run_dpdk()
	// 等待DPDK启动完成
	for {
		if C.running == C.bool(true) {
			break
		}
		time.Sleep(time.Second * 1)
	}
	port_ring_buffer = make([]ring_buffer, len(conf.PortIdList)*conf.QueueNum)
	port_pkt_rx_buf = make([][]byte, len(conf.PortIdList)*conf.QueueNum)
	for port_index := range conf.PortIdList {
		for queue_id := 0; queue_id < conf.QueueNum; queue_id++ {
			i := port_index*conf.QueueNum + queue_id
			port_ring_buffer[i].send_ring_buffer = C.cgo_port_send_ring_buffer(C.int(port_index), C.int(queue_id))
			port_ring_buffer[i].recv_ring_buffer = C.cgo_port_recv_ring_buffer(C.int(port_index), C.int(queue_id))
			port_pkt_rx_buf[i] = make([]byte, 1514)
		}
	}
	if conf.KniEnable {
		kni_ring_buffer.send_ring_buffer = C.cgo_kni_send_ring_buffer()
		kni_ring_buffer.recv_ring_buffer = C.cgo_kni_recv_ring_buffer()
		kni_pkt_rx_buf = make([]byte, 1514)
		go kni_handle()
	}
	running.Store(true)
	if conf.StatsLog {
		go print_port_stats(conf.PortIdList)
	}
}

// Exit 停止DPDK
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
	return EthQueueRxPkt(port_index, 0)
}

// EthTxPkt 网卡发包
func EthTxPkt(port_index int, pkt []byte) {
	EthQueueTxPkt(port_index, 0, pkt)
}

// EthQueueRxPkt 网卡队列收包
func EthQueueRxPkt(port_index int, queue_id int) (pkt []byte) {
	pkt_rx_buf := port_pkt_rx_buf[port_index*conf.QueueNum+queue_id]
	pkt_len := uint16(0)
	buffer := &(port_ring_buffer[port_index*conf.QueueNum+queue_id])
	ok := mem.ReadPacket((*mem.RingBuffer)(unsafe.Pointer(buffer.recv_ring_buffer)), pkt_rx_buf, &pkt_len)
	if !ok {
		if conf.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
		return nil
	}
	pkt = pkt_rx_buf[:pkt_len]
	if conf.DebugLog {
		Log(fmt.Sprintf("[eth rx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
	}
	return pkt
}

// EthQueueTxPkt 网卡队列发包
func EthQueueTxPkt(port_index int, queue_id int, pkt []byte) {
	pkt_len := len(pkt)
	buffer := &(port_ring_buffer[port_index*conf.QueueNum+queue_id])
	mem.WritePacket((*mem.RingBuffer)(unsafe.Pointer(buffer.send_ring_buffer)), pkt, uint16(pkt_len))
	if conf.DebugLog {
		Log(fmt.Sprintf("[eth tx pkt] port_index: %v, len: %v, data: %02x\n", port_index, pkt_len, pkt))
	}
}

// KniRxPkt KNI网卡收包
func KniRxPkt() (pkt []byte) {
	if !conf.KniEnable {
		return nil
	}
	pkt_rx_buf := kni_pkt_rx_buf
	pkt_len := uint16(0)
	buffer := &(kni_ring_buffer)
	ok := mem.ReadPacket((*mem.RingBuffer)(unsafe.Pointer(buffer.recv_ring_buffer)), pkt_rx_buf, &pkt_len)
	if !ok {
		if conf.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
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
	if !conf.KniEnable {
		return
	}
	pkt_len := len(pkt)
	buffer := &(kni_ring_buffer)
	mem.WritePacket((*mem.RingBuffer)(unsafe.Pointer(buffer.send_ring_buffer)), pkt, uint16(pkt_len))
	if conf.DebugLog {
		Log(fmt.Sprintf("[kni tx pkt] len: %v, data: %02x\n", pkt_len, pkt))
	}
}

// 打印网卡收发包统计信息
func print_port_stats(port_list []int) {
	ticker := time.NewTicker(time.Second)
	for {
		<-ticker.C
		if !running.Load() {
			ticker.Stop()
			break
		}
		for port_index := range port_list {
			var pinner runtime.Pinner
			var msg [1 * mem.KB]C.char
			pinner.Pin(&msg[0])
			C.cgo_print_stats(C.int(port_index), &msg[0])
			pinner.Unpin()
			Log(C.GoString(&msg[0]))
		}
	}
}

// 处理KNI内核网卡数据包
func kni_handle() {
	ticker := time.NewTicker(time.Millisecond * 100)
	for {
		<-ticker.C
		if !running.Load() {
			ticker.Stop()
			break
		}
		C.cgo_kni_handle()
	}
}

// 构建EAL参数
func build_eal_arg() (int, []*C.char) {
	eal_argc := 0
	eal_argv := make([]*C.char, 0)
	eal_argc++
	eal_argv = append(eal_argv, C.CString(os.Args[0]))
	cpu_list_param := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpu_list_param += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpu_list_param += ","
		}
	}
	eal_argc++
	eal_argv = append(eal_argv, C.CString("-l"))
	eal_argc++
	eal_argv = append(eal_argv, C.CString(cpu_list_param))
	eal_argc++
	eal_argv = append(eal_argv, C.CString("-n"))
	eal_argc++
	eal_argv = append(eal_argv, C.CString(strconv.Itoa(conf.DpdkMemChanNum)))
	for i, v := range conf.AfPacketDevList {
		eal_argc++
		eal_argv = append(eal_argv, C.CString("--vdev=net_af_packet"+strconv.Itoa(i)+",iface="+v))
	}
	eal_argc++
	eal_argv = append(eal_argv, C.CString("--"))
	return eal_argc, eal_argv
}

// 运行DPDK
func run_dpdk() {
	var pinner runtime.Pinner
	var config C.struct_dpdk_config
	eal_argc, eal_argv := build_eal_arg()
	config.eal_argc = C.int(eal_argc)
	var _eal_argv [128]*C.char
	for i, v := range eal_argv {
		_eal_argv[i] = v
	}
	pinner.Pin(&_eal_argv[0])
	config.eal_argv = &_eal_argv[0]
	config.cpu_core_num = C.int(len(conf.DpdkCpuCoreList))
	var _cpu_core_list [128]C.int
	for i, v := range conf.DpdkCpuCoreList {
		_cpu_core_list[i] = C.int(v)
	}
	pinner.Pin(&_cpu_core_list[0])
	config.cpu_core_list = &_cpu_core_list[0]
	config.port_num = C.int(len(conf.PortIdList))
	var _port_list [128]C.int
	for i, v := range conf.PortIdList {
		_port_list[i] = C.int(v)
	}
	pinner.Pin(&_port_list[0])
	config.port_list = &_port_list[0]
	config.queue_num = C.int(conf.QueueNum)
	config.ring_buffer_size = C.int(conf.RingBufferSize)
	config.debug_log = C.bool(conf.DebugLog)
	config.idle_sleep = C.bool(conf.IdleSleep)
	config.single_core = C.bool(conf.SingleCore)
	config.kni_enable = C.bool(conf.KniEnable)
	C.cgo_dpdk_main(&config)
	for _, arg := range eal_argv {
		C.free(unsafe.Pointer(arg))
	}
	pinner.Unpin()
}
