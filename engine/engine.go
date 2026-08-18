package engine

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/flswld/halo/cpu"
	"github.com/flswld/halo/hashcode"
	"github.com/flswld/halo/hashmap"
	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

var (
	DefaultLogWriter io.Writer = nil
)

// Log 将引擎日志写入默认日志输出器
func Log(msg string) {
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(msg))
	}
}

// IpAddrHash 表示可计算哈希值的 IPv4 地址
type IpAddrHash uint32

// GetHashCode 计算 IPv4 地址的哈希值
func (h IpAddrHash) GetHashCode() uint64 {
	return hashcode.GetHashCodeInt(uint64(h))
}

// PortHash 表示可计算哈希值的端口号
type PortHash uint16

// GetHashCode 计算端口号的哈希值
func (h PortHash) GetHashCode() uint64 {
	return hashcode.GetHashCodeInt(uint64(h))
}

// MacAddrHash 表示可计算哈希值的 MAC 地址
type MacAddrHash [6]byte

// GetHashCode 计算 MAC 地址的哈希值
func (h MacAddrHash) GetHashCode() uint64 {
	return hashcode.GetHashCodeXXH3(h[:])
}

// RouterConfig 路由器配置
type RouterConfig struct {
	DebugLog      bool                // 调试日志
	NetIfList     []*NetIfConfig      // 网卡列表
	RouteList     []*RouteEntryConfig // 静态路由列表
	StaticMemSize int                 // 静态内存池大小
}

// NetIfConfig 网卡配置
type NetIfConfig struct {
	Name               string                       // 网卡名
	MacAddr            string                       // MAC 地址
	IpAddr             string                       // IP 地址
	NetworkMask        string                       // 子网掩码
	Gateway            string                       // 网关地址
	NatEnable          bool                         // 是否开启网络地址转换
	NatType            int                          // 网络地址转换类型
	NatPortMappingList []*NatPortMappingEntryConfig // 网络地址转换端口映射表
	DnsServerAddr      string                       // DNS 服务器地址
	DhcpServerEnable   bool                         // 是否开启 DHCP 服务器
	DhcpClientEnable   bool                         // 是否开启 DHCP 客户端
	EthRxFunc          func() (pkt []byte)          // 网卡收包方法
	EthTxFunc          func(pkt []byte)             // 网卡发包方法
	BindCpuCore        int                          // 绑定的 CPU 核心 负值表示不绑核
}

// NatPortMappingEntryConfig NAT端口映射配置
type NatPortMappingEntryConfig struct {
	WanPort       uint16 // WAN 口端口
	LanHostIpAddr string // LAN 侧主机 IP 地址
	LanHostPort   uint16 // LAN 侧主机端口
	Ipv4HeadProto uint8  // IPv4 上层协议
}

// RouteEntryConfig 路由条目配置
type RouteEntryConfig struct {
	DstIpAddr   string // 目的 IP 地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳地址
	NetIf       string // 出接口名称
}

// NetIf 网卡
type NetIf struct {
	Config                  *NetIfConfig                               // 配置
	MacAddr                 []byte                                     // MAC 地址
	IpAddr                  []byte                                     // IP 地址
	NetworkMask             []byte                                     // 子网掩码
	Gateway                 []byte                                     // 网关地址
	EthTxBuffer             []byte                                     // 网卡发包缓冲区
	EthTxLock               cpu.SpinLock                               // 网卡发包锁
	LoChan                  chan []byte                                // 本地回环管道
	Router                  *Router                                    // 归属路由器
	ArpCacheTable           *hashmap.HashMap[IpAddrHash, *ArpCache]    // ARP 缓存表 键为 IP 地址 值为缓存项
	ArpLock                 sync.RWMutex                               // ARP 表读写锁
	NatFlowTable            *hashmap.HashMap[NatFlowHash, *NatFlow]    // NAT 流表 键为流摘要 值为流信息
	NatWanFlowTable         *hashmap.HashMap[NatWanFlowHash, *NatFlow] // WAN 口回程包 NAT 流表 键为 WAN 流摘要 值为流信息
	NatPortAlloc            *hashmap.HashMap[IpAddrHash, *PortAlloc]   // NAT 端口分配表 键为远程 IP 地址 值为端口分配信息
	NatPortMappingTable     []*NatPortMappingEntry                     // 网络地址转换端口映射表
	NatLock                 sync.RWMutex                               // NAT 表读写锁
	DnsServerAddr           []byte                                     // DNS 服务器地址
	DhcpLeaseTable          *hashmap.HashMap[IpAddrHash, *DhcpLease]   // DHCP 租期表 键为 IP 地址 值为租期信息
	DhcpLock                sync.RWMutex                               // DHCP 表读写锁
	DhcpClientTransactionId []byte                                     // DHCP 客户端事务 ID
	UdpServiceMap           map[uint16]UdpHandleFunc                   // UDP 服务集合 键为端口 值为处理函数
	TcpServiceMap           map[uint16]TcpHandleFunc                   // TCP 服务集合 键为端口 值为处理函数
}

// Router 路由器
type Router struct {
	Config                  *RouterConfig                                      // 配置
	Stop                    atomic.Bool                                        // 停止标志
	StopWaitGroup           sync.WaitGroup                                     // 停止等待组
	NetIfMap                map[string]*NetIf                                  // 网络接口集合 键为接口名 值为接口实例
	RouteTable              *RouteTable                                        // 路由表
	NatPortMappingFlowTable *hashmap.HashMap[NatFlowHash, *NatPortMappingFlow] // 端口映射回程 NAT 流表 键为流摘要 值为流信息
	NatPortMappingFlowLock  sync.RWMutex                                       // 端口映射回程 NAT 流表读写锁
	Ipv4PktFwdHook          func(raw []byte, dir int) (drop bool, mod []byte)  // IPv4 报文转发钩子
	StaticAllocatorPtr      unsafe.Pointer                                     // 静态内存分配器指针
	StaticAllocator         mem.Allocator                                      // 静态内存分配器
	TimeNow                 uint32                                             // 当前 Unix 秒级时间戳
}

// InitRouter 根据配置初始化路由器
func InitRouter(config *RouterConfig) (*Router, error) {
	if config.StaticMemSize == 0 {
		config.StaticMemSize = 8 * mem.MB
	}
	// 路由器级静态内存池由所有接口的 ARP NAT DHCP 和端口映射表共享
	heapAllocator := mem.GetHeapAllocator()
	staticAllocatorPtr := heapAllocator.Malloc(uint64(config.StaticMemSize))
	staticAllocator := mem.NewStaticAllocator(staticAllocatorPtr, uint64(config.StaticMemSize))
	initSuccess := false
	// 任一配置解析失败时回收尚未移交给路由器生命周期的内存池
	defer func() {
		if !initSuccess {
			heapAllocator.Free(staticAllocatorPtr)
		}
	}()
	r := &Router{
		Config:   config,
		NetIfMap: make(map[string]*NetIf),
		RouteTable: &RouteTable{
			Root:   new(TrieNode),
			IpHash: fnv.New32a(),
		},
		NatPortMappingFlowTable: hashmap.NewHashMap[NatFlowHash, *NatPortMappingFlow](staticAllocator),
		Ipv4PktFwdHook:          nil,
		StaticAllocatorPtr:      staticAllocatorPtr,
		StaticAllocator:         staticAllocator,
		TimeNow:                 uint32(time.Now().Unix()),
	}
	// 网卡列表
	for _, netIfConfig := range config.NetIfList {
		// 配置中的文本地址在初始化阶段统一转换为数据面使用的定长字节表示
		macAddr, err := protocol.ParseMacAddr(netIfConfig.MacAddr)
		if err != nil {
			return nil, err
		}
		ipAddr := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.IpAddr != "" {
			ipAddr, err = protocol.ParseIpAddr(netIfConfig.IpAddr)
			if err != nil {
				return nil, err
			}
		}
		networkMask := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.NetworkMask != "" {
			networkMask, err = protocol.ParseIpAddr(netIfConfig.NetworkMask)
			if err != nil {
				return nil, err
			}
		}
		gateway := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.Gateway != "" {
			gateway, err = protocol.ParseIpAddr(netIfConfig.Gateway)
			if err != nil {
				return nil, err
			}
		}
		dnsServerAddr := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.DnsServerAddr != "" {
			dnsServerAddr, err = protocol.ParseIpAddr(netIfConfig.DnsServerAddr)
			if err != nil {
				return nil, err
			}
		}
		netIf := &NetIf{
			Config:                  netIfConfig,
			MacAddr:                 macAddr,
			IpAddr:                  ipAddr,
			NetworkMask:             networkMask,
			Gateway:                 gateway,
			EthTxBuffer:             make([]byte, 0, 1514),
			LoChan:                  make(chan []byte, 1024),
			Router:                  r,
			ArpCacheTable:           hashmap.NewHashMap[IpAddrHash, *ArpCache](staticAllocator),
			NatFlowTable:            hashmap.NewHashMap[NatFlowHash, *NatFlow](staticAllocator),
			NatWanFlowTable:         hashmap.NewHashMap[NatWanFlowHash, *NatFlow](staticAllocator),
			NatPortAlloc:            hashmap.NewHashMap[IpAddrHash, *PortAlloc](staticAllocator),
			NatPortMappingTable:     make([]*NatPortMappingEntry, 0),
			DnsServerAddr:           dnsServerAddr,
			DhcpLeaseTable:          hashmap.NewHashMap[IpAddrHash, *DhcpLease](staticAllocator),
			DhcpClientTransactionId: nil,
			UdpServiceMap:           make(map[uint16]UdpHandleFunc),
			TcpServiceMap:           make(map[uint16]TcpHandleFunc),
		}
		// 静态端口映射预先转换 LAN 地址 避免转发热路径重复解析字符串
		for _, natPortMappingEntryConfig := range netIfConfig.NatPortMappingList {
			lanHostIpAddr, err := protocol.ParseIpAddr(natPortMappingEntryConfig.LanHostIpAddr)
			if err != nil {
				return nil, err
			}
			netIf.NatPortMappingTable = append(netIf.NatPortMappingTable, &NatPortMappingEntry{
				WanPort:       natPortMappingEntryConfig.WanPort,
				LanHostIpAddr: protocol.IpAddrToU(lanHostIpAddr),
				LanHostPort:   natPortMappingEntryConfig.LanHostPort,
				Ipv4HeadProto: natPortMappingEntryConfig.Ipv4HeadProto,
			})
		}
		r.NetIfMap[netIf.Config.Name] = netIf
	}
	// 路由表
	for _, routingEntryConfig := range config.RouteList {
		// 静态路由保持配置顺序写入 相同前缀由流哈希选择等价路径
		dstIpAddr, err := protocol.ParseIpAddr(routingEntryConfig.DstIpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := protocol.ParseIpAddr(routingEntryConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		nextHop, err := protocol.ParseIpAddr(routingEntryConfig.NextHop)
		if err != nil {
			return nil, err
		}
		r.RouteTable.AddRoute(&RouteEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: networkMask,
			NextHop:     nextHop,
			NetIf:       routingEntryConfig.NetIf,
		})
	}
	// 直连路由
	for _, netIf := range r.NetIfMap {
		// DHCP 接口在获得地址和掩码后再安装直连路由
		if netIf.Config.DhcpClientEnable {
			continue
		}
		dstIpAddrU := protocol.IpAddrToU(netIf.IpAddr) & protocol.IpAddrToU(netIf.NetworkMask)
		dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
		r.RouteTable.AddRoute(&RouteEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: netIf.NetworkMask,
			NextHop:     nil,
			NetIf:       netIf.Config.Name,
		})
	}
	protocol.SetRandIpHeaderId()
	initSuccess = true
	return r, nil
}

// RunRouter 启动路由器及各网络接口的后台处理任务
func (r *Router) RunRouter() {
	r.Stop.Store(false)
	// Monitor 为各老化任务提供统一的秒级时间基准
	go r.Monitor()
	r.StopWaitGroup.Add(1)
	// 端口映射回程流属于路由器级状态 只启动一个清理任务
	go r.NatPortMappingFlowClear()
	r.StopWaitGroup.Add(1)
	for _, netIf := range r.NetIfMap {
		// DHCP WAN 先发现地址 静态地址接口则主动通告本机地址
		if netIf.Config.DhcpClientEnable {
			netIf.DhcpDiscover()
		} else {
			netIf.SendFreeArp()
		}
		go netIf.ArpTableRefresh()
		r.StopWaitGroup.Add(1)
		go netIf.ArpTableClear()
		r.StopWaitGroup.Add(1)
		go netIf.PacketHandle()
		r.StopWaitGroup.Add(1)
		if netIf.Config.NatEnable {
			go netIf.NatTableClear()
			r.StopWaitGroup.Add(1)
		}
		if netIf.Config.DhcpServerEnable {
			go netIf.DhcpLeaseClear()
			r.StopWaitGroup.Add(1)
		}
	}
}

// Monitor 更新路由器使用的当前时间
func (r *Router) Monitor() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if r.Stop.Load() {
			break
		}
		r.TimeNow = uint32(time.Now().Unix())
	}
	r.StopWaitGroup.Done()
}

// GetNetIf 按名称获取路由器网络接口
func (r *Router) GetNetIf(name string) *NetIf {
	return r.NetIfMap[name]
}

// StopRouter 停止路由器并释放静态内存池
func (r *Router) StopRouter() {
	r.Stop.Store(true)
	r.StopWaitGroup.Wait()
	heapAllocator := mem.GetHeapAllocator()
	heapAllocator.Free(r.StaticAllocatorPtr)
}

// PacketHandle 持续接收并分发网络接口报文
func (i *NetIf) PacketHandle() {
	if i.Config.BindCpuCore >= 0 {
		cpu.BindCpuCore(i.Config.BindCpuCore)
	}
	n := 0
	for {
		if i.Router.Stop.Load() {
			break
		}
		ethFrm := i.Config.EthRxFunc()
		if ethFrm != nil {
			i.RxEthernet(ethFrm)
		}
		n++
		if n == 100-1 {
			// 每处理一批外部轮询后集中排空本地回环 避免回环流量长期饥饿
			for {
				if n == 0 {
					break
				}
				select {
				case ipv4Pkt := <-i.LoChan:
					ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ipv4Pkt)
					if err != nil {
						Log(fmt.Sprintf("parse ip packet error: %v\n", err))
						continue
					}
					if !bytes.Equal(ipv4DstAddr, i.IpAddr) {
						continue
					}
					switch ipv4HeadProto {
					case protocol.IPH_PROTO_ICMP:
						i.RxIcmp(ipv4Payload, ipv4SrcAddr)
					case protocol.IPH_PROTO_UDP:
						i.RxUdp(ipv4Payload, ipv4SrcAddr)
					case protocol.IPH_PROTO_TCP:
						i.RxTcp(ipv4Payload, ipv4SrcAddr)
					default:
					}
				default:
					n = 0
				}
			}
		}
	}
	i.Router.StopWaitGroup.Done()
}

// SwitchPortConfig 交换机端口配置
type SwitchPortConfig struct {
	Name        string              // 端口名
	EthRxFunc   func() (pkt []byte) // 端口收包方法
	EthTxFunc   func(pkt []byte)    // 端口发包方法
	VlanId      uint16              // VLAN 编号
	BindCpuCore int                 // 绑定的 CPU 核心 负值表示不绑核
}

// SwitchConfig 交换机配置
type SwitchConfig struct {
	SwitchPortList []*SwitchPortConfig // 端口列表
	StaticMemSize  int                 // 静态内存池大小
}

// SwitchPort 交换机端口
type SwitchPort struct {
	Config      *SwitchPortConfig // 配置
	Switch      *Switch           // 归属交换机
	EthTxBuffer []byte            // 端口发包缓冲区
	EthTxLock   cpu.SpinLock      // 端口发包锁
}

// Switch 交换机
type Switch struct {
	Config             *SwitchConfig                                 // 配置
	Stop               atomic.Bool                                   // 停止标志
	StopWaitGroup      sync.WaitGroup                                // 停止等待组
	SwitchPortMap      map[string]*SwitchPort                        // 交换机端口集合 键为端口名 值为端口实例
	SwitchMacAddrTable *hashmap.HashMap[MacAddrHash, *SwitchMacAddr] // 交换机 MAC 地址表 键为 MAC 地址 值为地址信息
	SwitchMacAddrLock  sync.RWMutex                                  // 交换机 MAC 地址表读写锁
	StaticAllocatorPtr unsafe.Pointer                                // 静态内存分配器指针
	StaticAllocator    mem.Allocator                                 // 静态内存分配器
	TimeNow            uint32                                        // 当前 Unix 秒级时间戳
}

// InitSwitch 根据配置初始化交换机
func InitSwitch(config *SwitchConfig) (*Switch, error) {
	if config.StaticMemSize == 0 {
		config.StaticMemSize = 8 * mem.MB
	}
	// 交换机 MAC 表及其条目统一从交换机级静态内存池分配
	heapAllocator := mem.GetHeapAllocator()
	staticAllocatorPtr := heapAllocator.Malloc(uint64(config.StaticMemSize))
	staticAllocator := mem.NewStaticAllocator(staticAllocatorPtr, uint64(config.StaticMemSize))
	s := &Switch{
		Config:             config,
		SwitchPortMap:      make(map[string]*SwitchPort),
		SwitchMacAddrTable: hashmap.NewHashMap[MacAddrHash, *SwitchMacAddr](staticAllocator),
		StaticAllocatorPtr: staticAllocatorPtr,
		StaticAllocator:    staticAllocator,
		TimeNow:            uint32(time.Now().Unix()),
	}
	for _, switchPortConfig := range config.SwitchPortList {
		switchPort := &SwitchPort{
			Config:      switchPortConfig,
			Switch:      s,
			EthTxBuffer: make([]byte, 0, 1514),
		}
		s.SwitchPortMap[switchPort.Config.Name] = switchPort
	}
	return s, nil
}

// RunSwitch 启动交换机及各端口的后台处理任务
func (s *Switch) RunSwitch() {
	s.Stop.Store(false)
	go s.Monitor()
	s.StopWaitGroup.Add(1)
	for _, switchPort := range s.SwitchPortMap {
		go switchPort.PacketHandle()
		s.StopWaitGroup.Add(1)
	}
	go s.SwitchMacAddrClear()
	s.StopWaitGroup.Add(1)
}

// Monitor 更新交换机使用的当前时间
func (s *Switch) Monitor() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if s.Stop.Load() {
			break
		}
		s.TimeNow = uint32(time.Now().Unix())
	}
	s.StopWaitGroup.Done()
}

// GetSwitchPort 按名称获取交换机端口
func (s *Switch) GetSwitchPort(name string) *SwitchPort {
	return s.SwitchPortMap[name]
}

// StopSwitch 停止交换机并释放静态内存池
func (s *Switch) StopSwitch() {
	s.Stop.Store(true)
	s.StopWaitGroup.Wait()
	heapAllocator := mem.GetHeapAllocator()
	heapAllocator.Free(s.StaticAllocatorPtr)
}

// PacketHandle 持续接收并分发交换机端口报文
func (s *SwitchPort) PacketHandle() {
	if s.Config.BindCpuCore >= 0 {
		cpu.BindCpuCore(s.Config.BindCpuCore)
	}
	for {
		if s.Switch.Stop.Load() {
			break
		}
		ethFrm := s.Config.EthRxFunc()
		if ethFrm != nil {
			s.RxEthernet(ethFrm)
		}
	}
	s.Switch.StopWaitGroup.Done()
}

const wireMaxPacketSize = 1514

// Wire 提供基于内存环形缓冲区的虚拟链路
type Wire struct {
	Memory     unsafe.Pointer          // 环形缓冲区使用的底层内存
	RingBuffer *mem.RingBuffer         // 环形缓冲区
	Data       []byte                  // 接收报文缓冲区
	IdleSleep  bool                    // 空闲时是否睡眠
	Producer   *mem.RingBufferProducer // 独占写入端的生产者上下文 直接写入时单包不得超过 1514 字节
	Consumer   *mem.RingBufferConsumer // 独占读取端的消费者上下文
}

// NewWire 创建虚拟链路
func NewWire(idleSleep bool) *Wire {
	// Wire 同时持有环形缓冲区头部和 8 MiB 数据区的底层内存
	memory := mem.GetHeapAllocator().Malloc(mem.SizeOf[mem.RingBuffer]() + 8*mem.MB)
	ringBuffer := mem.RingBufferCreate(memory, mem.SizeOf[mem.RingBuffer]()+8*mem.MB)
	return &Wire{
		Memory:     memory,
		RingBuffer: ringBuffer,
		Data:       make([]byte, wireMaxPacketSize),
		IdleSleep:  idleSleep,
		Producer:   mem.NewRingBufferProducer(ringBuffer, 0),
		Consumer:   mem.NewRingBufferConsumer(ringBuffer, 0),
	}
}

// Rx 从虚拟链路接收一个报文
func (w *Wire) Rx() (pkt []byte) {
	dataLen := uint32(0)
	ok := w.Consumer.ReadPacket(w.Data, &dataLen)
	if !ok {
		if w.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
		return nil
	}
	return w.Data[:dataLen]
}

// Tx 向虚拟链路发送一个报文
func (w *Wire) Tx(pkt []byte) {
	if len(pkt) == 0 || len(pkt) > wireMaxPacketSize {
		return
	}
	w.Producer.WritePacket(pkt)
}

// Destroy 销毁虚拟链路并释放底层内存
func (w *Wire) Destroy() {
	mem.RingBufferDestroy(w.RingBuffer)
	mem.GetHeapAllocator().Free(w.Memory)
}
