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
	AfPacketDevList []string // 使用的AF_PACKET虚拟网卡列表
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

// Run 启动dpdk
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
		// 单个cpu核心轮询
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
func print_port_stats(port_index_list []int) {
	ticker := time.NewTicker(time.Second)
	for {
		if !running.Load() {
			ticker.Stop()
			break
		}
		for _, port_index := range port_index_list {
			var msg [1 * mem.KB]C.char
			C.cgo_print_stats(C.int(port_index), (*C.char)(&msg[0]))
			Log(C.GoString((*C.char)(&msg[0])))
		}
		<-ticker.C
	}
}

// 处理kni内核网卡数据包
func kni_handle() {
	ticker := time.NewTicker(time.Millisecond * 100)
	for {
		if !running.Load() {
			ticker.Stop()
			break
		}
		C.cgo_kni_handle()
		<-ticker.C
	}
}

// 构建dpdk eal参数
func build_eal_args() string {
	cpu_list_param := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpu_list_param += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpu_list_param += ","
		}
	}
	arg_list := []string{
		os.Args[0],
		"-l", cpu_list_param,
		"-n", strconv.Itoa(conf.DpdkMemChanNum),
	}
	for index, af_packet_dev := range conf.AfPacketDevList {
		arg_list = append(arg_list, "--vdev=net_af_packet"+strconv.Itoa(index)+",iface="+af_packet_dev)
	}
	arg_list = append(arg_list, "--")
	args := ""
	for i, v := range arg_list {
		args += v
		if i < len(arg_list)-1 {
			args += " "
		}
	}
	return args
}

// 运行dpdk
func run_dpdk() {
	cpu_list_param := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpu_list_param += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpu_list_param += " "
		}
	}
	port_id_list_param := ""
	for i, v := range conf.PortIdList {
		port_id_list_param += strconv.Itoa(v)
		if i < len(conf.PortIdList)-1 {
			port_id_list_param += " "
		}
	}
	var config C.struct_dpdk_config
	config.eal_args = C.CString(build_eal_args())
	config.cpu_core_list = C.CString(cpu_list_param)
	config.port_id_list = C.CString(port_id_list_param)
	config.queue_num = C.int(conf.QueueNum)
	config.ring_buffer_size = C.int(conf.RingBufferSize)
	config.debug_log = C.bool(conf.DebugLog)
	config.idle_sleep = C.bool(conf.IdleSleep)
	config.single_core = C.bool(conf.SingleCore)
	config.kni_enable = C.bool(conf.KniEnable)
	C.cgo_dpdk_main(&config)
	C.free(unsafe.Pointer(config.eal_args))
	C.free(unsafe.Pointer(config.cpu_core_list))
	C.free(unsafe.Pointer(config.port_id_list))
}
