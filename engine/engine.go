package engine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/flswld/halo/cpu"
	"github.com/flswld/halo/hashmap"
	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

var (
	DefaultLogWriter io.Writer = nil
)

func Log(msg string) {
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(msg))
	}
}

type IpAddrHash uint32

func (h IpAddrHash) GetHashCode() uint64 {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, uint32(h))
	return hashmap.GetHashCode(data)
}

type PortHash uint16

func (h PortHash) GetHashCode() uint64 {
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, uint16(h))
	return hashmap.GetHashCode(data)
}

type MacAddrHash [6]byte

func (h MacAddrHash) GetHashCode() uint64 {
	return hashmap.GetHashCode(h[:])
}

// RouterConfig 路由器配置
type RouterConfig struct {
	DebugLog  bool                // 调试日志
	NetIfList []*NetIfConfig      // 网卡列表
	RouteList []*RouteEntryConfig // 静态路由列表
}

// NetIfConfig 网卡配置
type NetIfConfig struct {
	Name               string                       // 网卡名
	MacAddr            string                       // mac地址
	IpAddr             string                       // ip地址
	NetworkMask        string                       // 子网掩码
	NatEnable          bool                         // 开启网络地址转换
	NatType            int                          // 网络地址转换类型
	NatPortMappingList []*NatPortMappingEntryConfig // 网络地址转换端口映射表
	DnsServerAddr      string                       // dns服务器地址
	DhcpServerEnable   bool                         // 开启dhcp服务器
	DhcpClientEnable   bool                         // 开启dhcp客户端
	EthRxFunc          func() (pkt []byte)          // 网卡收包方法
	EthTxFunc          func(pkt []byte)             // 网卡发包方法
	BindCpuCore        int                          // 绑定的cpu核心
	StaticHeapSize     int                          // 静态堆内存大小
}

// NatPortMappingEntryConfig NAT端口映射配置
type NatPortMappingEntryConfig struct {
	WanPort       uint16 // wan口端口
	LanHostIpAddr string // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	Ipv4HeadProto uint8  // ip头部协议
}

// RouteEntryConfig 路由条目配置
type RouteEntryConfig struct {
	DstIpAddr   string // 目的ip地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳
	NetIf       string // 出接口
}

// NetIf 网卡
type NetIf struct {
	Config                  *NetIfConfig                               // 配置
	MacAddr                 []byte                                     // mac地址
	IpAddr                  []byte                                     // ip地址
	NetworkMask             []byte                                     // 子网掩码
	EthTxBuffer             []byte                                     // 网卡发包缓冲区
	EthTxLock               cpu.SpinLock                               // 网卡发包锁
	LoChan                  chan []byte                                // 本地回环管道
	Router                  *Router                                    // 归属Router指针
	ArpCacheTable           *hashmap.HashMap[IpAddrHash, *ArpCache]    // arp缓存表 key:ip value:mac
	ArpLock                 sync.RWMutex                               // arp锁
	NatFlowTable            *hashmap.HashMap[NatFlowHash, *NatFlow]    // nat流表 key:流摘要 value:流信息
	NatWanFlowTable         *hashmap.HashMap[NatWanFlowHash, *NatFlow] // wan口回程包nat流映射表 key:wan口流摘要 value:流信息
	NatPortAlloc            *hashmap.HashMap[IpAddrHash, *PortAlloc]   // nat端口分配表 key:远程ip value:端口分配信息
	NatPortMappingTable     []*NatPortMappingEntry                     // 网络地址转换端口映射表
	NatLock                 sync.RWMutex                               // nat锁
	DnsServerAddr           []byte                                     // dns服务器地址
	DhcpLeaseTable          *hashmap.HashMap[IpAddrHash, *DhcpLease]   // dhcp租期表 key:ip value:租期信息
	DhcpLock                sync.RWMutex                               // dhcp锁
	DhcpClientTransactionId []byte                                     // dhcp客户端事务id
	UdpServiceMap           map[uint16]UdpHandleFunc                   // udp服务集合 key:端口 value:处理函数
	TcpServiceMap           map[uint16]TcpHandleFunc                   // tcp服务集合 key:端口 value:处理函数
	StaticHeapPtr           unsafe.Pointer                             // 静态堆内存指针
	StaticHeap              mem.Heap                                   // 静态堆内存
}

// Router 路由器
type Router struct {
	Config         *RouterConfig                                     // 配置
	Stop           atomic.Bool                                       // 停止标志
	StopWaitGroup  sync.WaitGroup                                    // 停止等待组
	NetIfMap       map[string]*NetIf                                 // 网络接口集合 key:接口名 value:接口实例
	RouteTable     *RouteTable                                       // 路由表
	Ipv4PktFwdHook func(raw []byte, dir int) (drop bool, mod []byte) // ip报文转发钩子
	TimeNow        uint32                                            // 当前毫秒时间戳
}

func InitRouter(config *RouterConfig) (*Router, error) {
	r := &Router{
		Config:   config,
		NetIfMap: make(map[string]*NetIf),
		RouteTable: &RouteTable{
			Root:   new(TrieNode),
			IpHash: fnv.New32a(),
		},
		Ipv4PktFwdHook: nil,
		TimeNow:        uint32(time.Now().Unix()),
	}
	// 网卡列表
	cHeap := mem.NewCHeap()
	for _, netIfConfig := range config.NetIfList {
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
		dnsServerAddr := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.DnsServerAddr != "" {
			dnsServerAddr, err = protocol.ParseIpAddr(netIfConfig.DnsServerAddr)
			if err != nil {
				return nil, err
			}
		}
		if netIfConfig.StaticHeapSize == 0 {
			netIfConfig.StaticHeapSize = 8 * mem.MB
		}
		staticHeapPtr := cHeap.Malloc(uint64(netIfConfig.StaticHeapSize))
		staticHeap := mem.NewStaticHeap(staticHeapPtr, uint64(netIfConfig.StaticHeapSize))
		netIf := &NetIf{
			Config:                  netIfConfig,
			MacAddr:                 macAddr,
			IpAddr:                  ipAddr,
			NetworkMask:             networkMask,
			EthTxBuffer:             make([]byte, 0, 1514),
			LoChan:                  make(chan []byte, 1024),
			Router:                  r,
			ArpCacheTable:           hashmap.NewHashMap[IpAddrHash, *ArpCache](staticHeap),
			NatFlowTable:            hashmap.NewHashMap[NatFlowHash, *NatFlow](staticHeap),
			NatWanFlowTable:         hashmap.NewHashMap[NatWanFlowHash, *NatFlow](staticHeap),
			NatPortAlloc:            hashmap.NewHashMap[IpAddrHash, *PortAlloc](staticHeap),
			NatPortMappingTable:     make([]*NatPortMappingEntry, 0),
			DnsServerAddr:           dnsServerAddr,
			DhcpLeaseTable:          hashmap.NewHashMap[IpAddrHash, *DhcpLease](staticHeap),
			DhcpClientTransactionId: nil,
			UdpServiceMap:           make(map[uint16]UdpHandleFunc),
			TcpServiceMap:           make(map[uint16]TcpHandleFunc),
			StaticHeapPtr:           staticHeapPtr,
			StaticHeap:              staticHeap,
		}
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
	return r, nil
}

func (r *Router) RunRouter() {
	r.Stop.Store(false)
	go r.Monitor()
	r.StopWaitGroup.Add(1)
	for _, netIf := range r.NetIfMap {
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

func (r *Router) GetNetIf(name string) *NetIf {
	return r.NetIfMap[name]
}

func (r *Router) StopRouter() {
	r.Stop.Store(true)
	r.StopWaitGroup.Wait()
	cHeap := mem.NewCHeap()
	for _, netIf := range r.NetIfMap {
		cHeap.Free(netIf.StaticHeapPtr)
	}
}

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
	VlanId      uint16              // vlan号
	BindCpuCore int                 // 绑定的cpu核心
}

// SwitchConfig 交换机配置
type SwitchConfig struct {
	SwitchPortList []*SwitchPortConfig // 端口列表
	StaticHeapSize int                 // 静态堆内存大小
}

// SwitchPort 交换机端口
type SwitchPort struct {
	Config      *SwitchPortConfig // 配置
	Switch      *Switch           // 归属Switch指针
	EthTxBuffer []byte            // 端口发包缓冲区
	EthTxLock   cpu.SpinLock      // 端口发包锁
}

// Switch 交换机
type Switch struct {
	Config             *SwitchConfig                                 // 配置
	Stop               atomic.Bool                                   // 停止标志
	StopWaitGroup      sync.WaitGroup                                // 停止等待组
	SwitchPortMap      map[string]*SwitchPort                        // 交换机端口集合 key:端口名 value:端口实例
	SwitchMacAddrTable *hashmap.HashMap[MacAddrHash, *SwitchMacAddr] // 交换机mac地址表 key:mac地址 value:地址信息
	SwitchMacAddrLock  sync.RWMutex                                  // 交换机mac地址锁
	StaticHeapPtr      unsafe.Pointer                                // 静态堆内存指针
	StaticHeap         mem.Heap                                      // 静态堆内存
	TimeNow            uint32                                        // 当前毫秒时间戳
}

func InitSwitch(config *SwitchConfig) (*Switch, error) {
	if config.StaticHeapSize == 0 {
		config.StaticHeapSize = 8 * mem.MB
	}
	cHeap := mem.NewCHeap()
	staticHeapPtr := cHeap.Malloc(uint64(config.StaticHeapSize))
	staticHeap := mem.NewStaticHeap(staticHeapPtr, uint64(config.StaticHeapSize))
	s := &Switch{
		Config:             config,
		SwitchPortMap:      make(map[string]*SwitchPort),
		SwitchMacAddrTable: hashmap.NewHashMap[MacAddrHash, *SwitchMacAddr](staticHeap),
		StaticHeapPtr:      staticHeapPtr,
		StaticHeap:         staticHeap,
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

func (s *Switch) GetSwitchPort(name string) *SwitchPort {
	return s.SwitchPortMap[name]
}

func (s *Switch) StopSwitch() {
	s.Stop.Store(true)
	s.StopWaitGroup.Wait()
	cHeap := mem.NewCHeap()
	cHeap.Free(s.StaticHeapPtr)
}

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

type Wire struct {
	Memory     unsafe.Pointer
	RingBuffer *mem.RingBuffer
	Data       []byte
	IdleSleep  bool
}

func NewWire(idleSleep bool) *Wire {
	memory := new(mem.CHeap).Malloc(mem.SizeOf[mem.RingBuffer]() + 8*mem.MB)
	ringBuffer := mem.RingBufferCreate(memory, uint32(mem.SizeOf[mem.RingBuffer]()+8*mem.MB))
	return &Wire{
		Memory:     memory,
		RingBuffer: ringBuffer,
		Data:       make([]byte, 1514),
		IdleSleep:  idleSleep,
	}
}

func (w *Wire) Rx() (pkt []byte) {
	dataLen := uint16(0)
	mem.ReadPacket(w.RingBuffer, w.Data, &dataLen)
	if dataLen == 0 {
		if w.IdleSleep {
			time.Sleep(time.Millisecond * 10)
		}
		return nil
	}
	return w.Data[:dataLen]
}

func (w *Wire) Tx(pkt []byte) {
	mem.WritePacket(w.RingBuffer, pkt, uint16(len(pkt)))
}

func (w *Wire) Destroy() {
	mem.RingBufferDestroy(w.RingBuffer)
	new(mem.CHeap).Free(w.Memory)
}
