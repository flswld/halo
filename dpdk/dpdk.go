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

const maxPacketSize = 1514

var (
	DefaultLogWriter io.Writer = nil
)

// Log 将 DPDK 日志写入默认日志输出器
func Log(msg string) {
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(msg))
	}
}

// Config 定义 DPDK 数据面的运行参数
type Config struct {
	DpdkCpuCoreList []int    // DPDK 使用的核心编号列表 首项用于主线程 其余项用于收发工作线程
	DpdkMemChanNum  int      // DPDK 内存通道数
	PortIdList      []int    // 使用的网卡 ID 列表
	QueueNum        int      // 启用的网卡队列数
	RingBufferSize  int      // 环状缓冲区大小
	EalArgs         []string // 追加的 EAL 参数列表
	StatsLog        bool     // 收发包统计日志
	DebugLog        bool     // 收发包调试日志
	IdleSleep       bool     // 空闲时睡眠以降低 CPU 占用
	SingleCore      bool     // 仅使用 CPU 0 的单核模式
	KniEnable       bool     // 是否启用 KNI 内核网卡
	RxChecksum      bool     // 是否启用接收硬件校验和
	TxChecksum      bool     // 是否启用发送硬件校验和 禁用时由软件计算
	TxOnly          bool     // 是否仅启动网卡发包工作线程
}

// PortStats 保存网卡端口累计统计信息
type PortStats struct {
	RxPackets uint64 // 接收包数量
	TxPackets uint64 // 发送包数量
	RxBytes   uint64 // 接收字节数
	TxBytes   uint64 // 发送字节数
	RxMissed  uint64 // 硬件接收丢包数量
	RxNoMbuf  uint64 // mbuf 不足导致的接收丢包数量
	RxErrors  uint64 // 接收错误数量
	TxErrors  uint64 // 发送错误数量
}

// ring_buffer 保存一组 DPDK 收发环形缓冲区
type ring_buffer struct {
	send_ring_buffer *C.ring_buffer_t        // 发送环形缓冲区
	recv_ring_buffer *C.ring_buffer_t        // 接收环形缓冲区
	sendProducer     *mem.RingBufferProducer // Go 到 DPDK 的独占生产者
	recvConsumer     *mem.RingBufferConsumer // DPDK 到 Go 的独占消费者
}

var (
	conf             *Config       = nil
	port_ring_buffer []ring_buffer = nil
	port_pkt_rx_buf  [][]byte      = nil
	kni_ring_buffer  ring_buffer
	kni_pkt_rx_buf   []byte = nil
	running          atomic.Bool
)

// Run 启动 DPDK 数据面
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
		worker_num := len(conf.PortIdList) * conf.QueueNum
		if !conf.TxOnly {
			worker_num *= 2
		}
		if len(conf.DpdkCpuCoreList) < 1+worker_num {
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
	if 8+len(conf.EalArgs) > 128 {
		panic("eal arg num too large")
	}
	// C 主循环会阻塞当前线程 因此放入独立协程启动
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
	// C 层拥有环形缓冲区内存 Go 层只保存映射指针和复用接收切片
	for port_index := range conf.PortIdList {
		for queue_id := 0; queue_id < conf.QueueNum; queue_id++ {
			i := port_index*conf.QueueNum + queue_id
			port_ring_buffer[i].send_ring_buffer = C.cgo_port_send_ring_buffer(C.int(port_index), C.int(queue_id))
			port_ring_buffer[i].recv_ring_buffer = C.cgo_port_recv_ring_buffer(C.int(port_index), C.int(queue_id))
			port_ring_buffer[i].sendProducer = mem.NewRingBufferProducer((*mem.RingBuffer)(unsafe.Pointer(port_ring_buffer[i].send_ring_buffer)), 0)
			port_ring_buffer[i].recvConsumer = mem.NewRingBufferConsumer((*mem.RingBuffer)(unsafe.Pointer(port_ring_buffer[i].recv_ring_buffer)), 0)
			if port_ring_buffer[i].sendProducer == nil || port_ring_buffer[i].recvConsumer == nil {
				panic("ring buffer context init failed")
			}
			port_pkt_rx_buf[i] = make([]byte, maxPacketSize)
		}
	}
	if conf.KniEnable {
		kni_ring_buffer.send_ring_buffer = C.cgo_kni_send_ring_buffer()
		kni_ring_buffer.recv_ring_buffer = C.cgo_kni_recv_ring_buffer()
		kni_ring_buffer.sendProducer = mem.NewRingBufferProducer((*mem.RingBuffer)(unsafe.Pointer(kni_ring_buffer.send_ring_buffer)), 0)
		kni_ring_buffer.recvConsumer = mem.NewRingBufferConsumer((*mem.RingBuffer)(unsafe.Pointer(kni_ring_buffer.recv_ring_buffer)), 0)
		if kni_ring_buffer.sendProducer == nil || kni_ring_buffer.recvConsumer == nil {
			panic("kni ring buffer context init failed")
		}
		kni_pkt_rx_buf = make([]byte, maxPacketSize)
		go kni_handle()
	}
	running.Store(true)
	if conf.StatsLog {
		go print_port_stats(conf.PortIdList)
	}
}

// Exit 停止 DPDK 数据面
func Exit() {
	// 先停止 Go 侧后台任务 再通知 C 层等待工作核心退出并释放资源
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

// EthTxPkt 通过网卡默认队列非阻塞发送单个报文并返回是否写入成功
func EthTxPkt(port_index int, pkt []byte) bool {
	return EthQueueTxPkt(port_index, 0, pkt)
}

// EthQueueRxPkt 网卡队列收包
func EthQueueRxPkt(port_index int, queue_id int) (pkt []byte) {
	pkt_rx_buf := port_pkt_rx_buf[port_index*conf.QueueNum+queue_id]
	pkt_len := uint32(0)
	buffer := &(port_ring_buffer[port_index*conf.QueueNum+queue_id])
	ok := buffer.recvConsumer.ReadPacket(pkt_rx_buf, &pkt_len)
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

// EthQueueTxPkt 通过指定网卡队列非阻塞发送单个报文并返回是否写入成功
func EthQueueTxPkt(port_index int, queue_id int, pkt []byte) bool {
	if len(pkt) == 0 || len(pkt) > maxPacketSize {
		return false
	}
	buffer := &(port_ring_buffer[port_index*conf.QueueNum+queue_id])
	// 环形缓冲区满时当前接口直接丢包 数据面不阻塞等待空间
	ok := buffer.sendProducer.WritePacket(pkt)
	if conf.DebugLog {
		Log(fmt.Sprintf("[eth tx pkt] port_index: %v, len: %v, data: %02x\n", port_index, len(pkt), pkt))
	}
	return ok
}

// KniRxPkt 从 KNI 网卡接收报文
func KniRxPkt() (pkt []byte) {
	if !conf.KniEnable {
		return nil
	}
	pkt_rx_buf := kni_pkt_rx_buf
	pkt_len := uint32(0)
	buffer := &(kni_ring_buffer)
	ok := buffer.recvConsumer.ReadPacket(pkt_rx_buf, &pkt_len)
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

// KniTxPkt 通过 KNI 网卡发送报文
func KniTxPkt(pkt []byte) {
	if !conf.KniEnable || len(pkt) == 0 || len(pkt) > maxPacketSize {
		return
	}
	buffer := &(kni_ring_buffer)
	buffer.sendProducer.WritePacket(pkt)
	if conf.DebugLog {
		Log(fmt.Sprintf("[kni tx pkt] len: %v, data: %02x\n", len(pkt), pkt))
	}
}

// GetPortStats 获取网卡端口累计统计信息
func GetPortStats(port_index int) (PortStats, error) {
	if conf == nil || port_index < 0 || port_index >= len(conf.PortIdList) {
		return PortStats{}, fmt.Errorf("port index out of range: %d", port_index)
	}
	var stats C.struct_rte_eth_stats
	if ret := C.cgo_get_stats(C.int(port_index), &stats); ret != 0 {
		return PortStats{}, fmt.Errorf("get port stats failed: %d", int(ret))
	}
	return PortStats{
		RxPackets: uint64(stats.ipackets),
		TxPackets: uint64(stats.opackets),
		RxBytes:   uint64(stats.ibytes),
		TxBytes:   uint64(stats.obytes),
		RxMissed:  uint64(stats.imissed),
		RxNoMbuf:  uint64(stats.rx_nombuf),
		RxErrors:  uint64(stats.ierrors),
		TxErrors:  uint64(stats.oerrors),
	}, nil
}

// print_port_stats 定时打印网卡收发包速率统计信息
func print_port_stats(port_id_list []int) {
	ticker := time.NewTicker(time.Second)
	old_stats := make([]PortStats, len(port_id_list))
	for {
		<-ticker.C
		if !running.Load() {
			ticker.Stop()
			break
		}
		for port_index, port_id := range port_id_list {
			new_stats, err := GetPortStats(port_index)
			if err != nil {
				Log(fmt.Sprintf("[rte_eth_stats]\tport:%2d | error: %v\n", port_id, err))
				continue
			}
			Log(fmt.Sprintf(
				"[rte_eth_stats]\tport:%2d | rx:%10d (pps) | tx:%10d (pps) | drop:%10d (pps) | rx:%20d (byte/s) | tx:%20d (byte/s)\n",
				port_id,
				new_stats.RxPackets-old_stats[port_index].RxPackets,
				new_stats.TxPackets-old_stats[port_index].TxPackets,
				new_stats.RxMissed-old_stats[port_index].RxMissed,
				new_stats.RxBytes-old_stats[port_index].RxBytes,
				new_stats.TxBytes-old_stats[port_index].TxBytes,
			))
			old_stats[port_index] = new_stats
		}
	}
}

// kni_handle 定时处理 KNI 内核网卡数据包
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

// build_eal_arg 构建 DPDK EAL 启动参数
func build_eal_arg() []*C.char {
	eal_argv := make([]*C.char, 0)
	eal_argv = append(eal_argv, C.CString(os.Args[0]))
	cpu_list_param := ""
	for i, v := range conf.DpdkCpuCoreList {
		cpu_list_param += strconv.Itoa(v)
		if i < len(conf.DpdkCpuCoreList)-1 {
			cpu_list_param += ","
		}
	}
	eal_argv = append(eal_argv, C.CString("-l"))
	eal_argv = append(eal_argv, C.CString(cpu_list_param))
	// EAL 默认选择编号最小的启用核心 显式指定列表首项才能兑现配置约定
	eal_argv = append(eal_argv, C.CString("--main-lcore"))
	eal_argv = append(eal_argv, C.CString(strconv.Itoa(conf.DpdkCpuCoreList[0])))
	eal_argv = append(eal_argv, C.CString("-n"))
	eal_argv = append(eal_argv, C.CString(strconv.Itoa(conf.DpdkMemChanNum)))
	for _, arg := range conf.EalArgs {
		eal_argv = append(eal_argv, C.CString(arg))
	}
	eal_argv = append(eal_argv, C.CString("--"))
	// 返回的 C 字符串由 run_dpdk 在 C 主循环退出后统一释放
	return eal_argv
}

// run_dpdk 组装 C 层配置并运行 DPDK
func run_dpdk() {
	var pinner runtime.Pinner
	var config C.struct_dpdk_config
	eal_argv := build_eal_arg()
	config.eal_argc = C.int(len(eal_argv))
	var _eal_argv [128]*C.char
	for i, v := range eal_argv {
		_eal_argv[i] = v
	}
	// C 主循环存续期间固定 Go 栈上数组 防止运行时移动或回收传入内存
	pinner.Pin(&_eal_argv[0])
	config.eal_argv = &_eal_argv[0]
	config.dpdk_cpu_core_num = C.int(len(conf.DpdkCpuCoreList))
	var _cpu_core_list [128]C.int
	for i, v := range conf.DpdkCpuCoreList {
		_cpu_core_list[i] = C.int(v)
	}
	pinner.Pin(&_cpu_core_list[0])
	config.dpdk_cpu_core_list = &_cpu_core_list[0]
	config.port_id_num = C.int(len(conf.PortIdList))
	var _port_list [128]C.int
	for i, v := range conf.PortIdList {
		_port_list[i] = C.int(v)
	}
	pinner.Pin(&_port_list[0])
	config.port_id_list = &_port_list[0]
	config.queue_num = C.int(conf.QueueNum)
	config.ring_buffer_size = C.int(conf.RingBufferSize)
	config.debug_log = C.bool(conf.DebugLog)
	config.idle_sleep = C.bool(conf.IdleSleep)
	config.single_core = C.bool(conf.SingleCore)
	config.kni_enable = C.bool(conf.KniEnable)
	config.rx_checksum = C.bool(conf.RxChecksum)
	config.tx_checksum = C.bool(conf.TxChecksum)
	config.tx_only = C.bool(conf.TxOnly)
	C.cgo_dpdk_main(&config)
	// cgo_dpdk_main 返回后 C 层不再持有参数数组和字符串
	for _, arg := range eal_argv {
		C.free(unsafe.Pointer(arg))
	}
	pinner.Unpin()
}
