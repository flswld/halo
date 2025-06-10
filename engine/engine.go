package engine

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flswld/halo/cpu"
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

// Config 协议栈配置
type Config struct {
	DebugLog        bool                  // 调试日志
	NetIfList       []*NetIfConfig        // 网卡列表
	RoutingTable    []*RoutingEntryConfig // 路由表
	CheckSumDisable bool
}

// NetIfConfig 网卡配置
type NetIfConfig struct {
	Name                string                       // 网卡名
	MacAddr             string                       // mac地址
	IpAddr              string                       // ip地址
	NetworkMask         string                       // 子网掩码
	NatEnable           bool                         // 网络地址转换
	NatType             int                          // 网络地址转换类型
	NatPortMappingTable []*NatPortMappingEntryConfig // 网络地址转换端口映射表
	DnsServerAddr       string
	DhcpServerEnable    bool
	DhcpClientEnable    bool
	EthRxFunc           func() (pkt []byte) // 网卡收包方法
	EthTxFunc           func(pkt []byte)    // 网卡发包方法
	BindCpuCore         int                 // 绑定的cpu核心
}

// NatPortMappingEntryConfig NAT端口映射配置
type NatPortMappingEntryConfig struct {
	WanIpAddr     string // wan口ip地址
	WanPort       uint16 // wan口端口
	LanHostIpAddr string // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	Ipv4HeadProto uint8  // ip头部协议
}

// RoutingEntryConfig 路由条目配置
type RoutingEntryConfig struct {
	DstIpAddr   string // 目的ip地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳
	NetIf       string // 出接口
}

// NetIf 网卡
type NetIf struct {
	Name                    string                      // 接口名
	MacAddr                 []byte                      // mac地址
	IpAddr                  []byte                      // ip地址
	NetworkMask             []byte                      // 子网掩码
	EthRxFunc               func() (pkt []byte)         // 网卡收包方法
	EthTxFunc               func(pkt []byte)            // 网卡发包方法
	EthTxBuffer             []byte                      // 网卡发包缓冲区
	EthTxLock               cpu.SpinLock                // 网卡发包锁
	LoChan                  chan []byte                 // 本地回环管道
	Engine                  *Engine                     // 归属Engine指针
	ArpCacheTable           map[uint32][]byte           // arp缓存表 key:ip value:mac
	ArpCacheTableLock       sync.RWMutex                // arp缓存表锁
	NatEnable               bool                        // 是否开启nat
	NatType                 int                         // nat类型
	NatWanFlowTable         map[NatWanFlowHash]*NatFlow // wan口回程包nat流映射表
	NatFlowTable            map[NatFlowHash]*NatFlow    // nat流表
	NatPortAlloc            map[uint32]*PortAlloc       // nat端口分配表 key:目的ip value:端口分配信息
	NatPortMappingTable     []*NatPortMappingEntry      // 网络地址转换端口映射表
	NatTableLock            sync.RWMutex                // nat表锁
	DnsServerAddr           []byte
	DhcpServerEnable        bool
	DhcpLeaseMap            map[uint32]*DhcpLease
	DhcpLock                sync.RWMutex
	DhcpClientEnable        bool
	DhcpClientTransactionId []byte
	BindCpuCore             int // 绑定的cpu核心
	HandleUdp               func(payload []byte, srcPort uint16, dstPort uint16, srcAddr []byte)
	HandleTcp               func(payload []byte, srcPort uint16, dstPort uint16, seqNum uint32, ackNum uint32, flags uint8, srcAddr []byte)
}

// Engine 协议栈
type Engine struct {
	DebugLog       bool                                              // 调试日志
	Stop           atomic.Bool                                       // 停止标志
	NetIfMap       map[string]*NetIf                                 // 网络接口集合 key:接口名 value:接口实例
	RouteTable     *RouteTable                                       // 路由表
	Ipv4PktFwdHook func(raw []byte, dir int) (drop bool, mod []byte) // ip报文转发钩子
}

func InitEngine(config *Config) (*Engine, error) {
	r := new(Engine)
	r.DebugLog = config.DebugLog
	r.Stop.Store(false)
	r.NetIfMap = make(map[string]*NetIf)
	r.RouteTable = &RouteTable{
		Root: new(TrieNode),
	}
	r.Ipv4PktFwdHook = nil
	// 网卡列表
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
		netIf := &NetIf{
			Name:                    netIfConfig.Name,
			MacAddr:                 macAddr,
			IpAddr:                  ipAddr,
			NetworkMask:             networkMask,
			EthRxFunc:               netIfConfig.EthRxFunc,
			EthTxFunc:               netIfConfig.EthTxFunc,
			EthTxBuffer:             make([]byte, 0, 1514),
			LoChan:                  make(chan []byte, 1024),
			Engine:                  r,
			ArpCacheTable:           make(map[uint32][]byte),
			NatEnable:               netIfConfig.NatEnable,
			NatType:                 netIfConfig.NatType,
			NatWanFlowTable:         make(map[NatWanFlowHash]*NatFlow),
			NatFlowTable:            make(map[NatFlowHash]*NatFlow),
			NatPortAlloc:            make(map[uint32]*PortAlloc),
			NatPortMappingTable:     make([]*NatPortMappingEntry, 0),
			DnsServerAddr:           dnsServerAddr,
			DhcpServerEnable:        netIfConfig.DhcpServerEnable,
			DhcpLeaseMap:            make(map[uint32]*DhcpLease),
			DhcpClientEnable:        netIfConfig.DhcpClientEnable,
			DhcpClientTransactionId: nil,
			BindCpuCore:             netIfConfig.BindCpuCore,
			HandleUdp:               nil,
			HandleTcp:               nil,
		}
		for _, natPortMappingEntryConfig := range netIfConfig.NatPortMappingTable {
			wanIpAddr, err := protocol.ParseIpAddr(natPortMappingEntryConfig.WanIpAddr)
			if err != nil {
				return nil, err
			}
			lanHostIpAddr, err := protocol.ParseIpAddr(natPortMappingEntryConfig.LanHostIpAddr)
			if err != nil {
				return nil, err
			}
			netIf.NatPortMappingTable = append(netIf.NatPortMappingTable, &NatPortMappingEntry{
				WanIpAddr:     protocol.IpAddrToU(wanIpAddr),
				WanPort:       natPortMappingEntryConfig.WanPort,
				LanHostIpAddr: protocol.IpAddrToU(lanHostIpAddr),
				LanHostPort:   natPortMappingEntryConfig.LanHostPort,
				Ipv4HeadProto: natPortMappingEntryConfig.Ipv4HeadProto,
			})
		}
		r.NetIfMap[netIf.Name] = netIf
	}
	// 路由表
	for _, routingEntryConfig := range config.RoutingTable {
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
		if netIf.DhcpClientEnable {
			continue
		}
		dstIpAddrU := protocol.IpAddrToU(netIf.IpAddr) & protocol.IpAddrToU(netIf.NetworkMask)
		dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
		r.RouteTable.AddRoute(&RouteEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: netIf.NetworkMask,
			NextHop:     nil,
			NetIf:       netIf.Name,
		})
	}
	if config.CheckSumDisable {
		protocol.CheckSumDisable = true
	}
	protocol.SetRandIpHeaderId()
	return r, nil
}

func (e *Engine) RunEngine() {
	for _, netIf := range e.NetIfMap {
		if netIf.DhcpClientEnable {
			netIf.DhcpDiscover()
		} else {
			netIf.SendFreeArp()
		}
		go netIf.PacketHandle()
		if netIf.NatEnable {
			go netIf.NatTableClear()
		}
		if netIf.DhcpServerEnable {
			go netIf.DhcpLeaseClear()
		}
	}
}

func (e *Engine) GetNetIf(name string) *NetIf {
	return e.NetIfMap[name]
}

func (e *Engine) StopEngine() {
	e.Stop.Store(true)
}

func (i *NetIf) PacketHandle() {
	cpu.BindCpuCore(i.BindCpuCore)
	n := 0
	for {
		ethFrm := i.EthRxFunc()
		if ethFrm != nil {
			i.RxEthernet(ethFrm)
		}
		n++
		if n%100 == 0 {
			if i.Engine.Stop.Load() {
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
			}
		}
	}
}

// NAT类型
const (
	NatTypeSymmetric = 0 // 对称型
	NatTypeFullCone  = 1 // 完全圆锥型
)

// NatFlow NAT流
type NatFlow struct {
	NatFlowHash   NatFlowHash // nat流唯一标识
	RemoteIpAddr  uint32      // 远程ip地址
	RemotePort    uint16      // 远程端口
	WanIpAddr     uint32      // wan口ip地址
	WanPort       uint16      // wan口端口
	LanHostIpAddr uint32      // lan口主机ip地址
	LanHostPort   uint16      // lan口主机端口
	Ipv4HeadProto uint8       // ip头部协议
	LastAliveTime uint32      // 上一次活跃时间
}

// NatFlowHash NAT流摘要
type NatFlowHash struct {
	RemoteIpAddr  uint32 // 远程ip地址
	RemotePort    uint16 // 远程端口
	LanHostIpAddr uint32 // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	Ipv4HeadProto uint8  // ip头部协议
}

// NatWanFlowHash WAN口NAT流摘要
type NatWanFlowHash struct {
	RemoteIpAddr  uint32 // 远程ip地址
	RemotePort    uint16 // 远程端口
	WanIpAddr     uint32 // wan口ip地址
	WanPort       uint16 // wan口端口
	Ipv4HeadProto uint8  // ip头部协议
}

// NatPortMappingEntry NAT端口映射条目
type NatPortMappingEntry struct {
	WanIpAddr     uint32 // wan口ip地址
	WanPort       uint16 // wan口端口
	LanHostIpAddr uint32 // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	Ipv4HeadProto uint8  // ip头部协议
}

// PortAlloc 端口分配器
type PortAlloc struct {
	Lock          sync.RWMutex                // 锁
	AllocPortMap  map[PortAllocSrcAddr]uint16 // 分配端口集合 key:源地址 value:端口
	RemainPortMap map[uint16]struct{}         // 剩余端口集合 key:端口
}

type PortAllocSrcAddr struct {
	IpAddr uint32 // ip地址
	Port   uint16 // 端口
}

func (i *NetIf) NatGetFlowByHash(natFlowHash NatFlowHash) *NatFlow {
	i.NatTableLock.RLock()
	natFlow, exist := i.NatFlowTable[natFlowHash]
	i.NatTableLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatGetFlowByWan(remoteIpAddr []byte, remotePort uint16, wanIpAddr []byte, wanPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.NatType == NatTypeSymmetric {
		_remoteAddrU = protocol.IpAddrToU(remoteIpAddr)
		_remotePort = remotePort
	} else if i.NatType == NatTypeFullCone {
		_remoteAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	i.NatTableLock.RLock()
	natFlow, exist := i.NatWanFlowTable[NatWanFlowHash{
		RemoteIpAddr:  _remoteAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(wanIpAddr),
		WanPort:       wanPort,
		Ipv4HeadProto: ipv4HeadProto,
	}]
	i.NatTableLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatAddFlow(lanHostAddr []byte, remoteAddr []byte, lanHostPort uint16, remotePort uint16, ipv4HeadProto uint8) *NatFlow {
	i.NatTableLock.Lock()
	defer i.NatTableLock.Unlock()
	_remoteAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.NatType == NatTypeSymmetric {
		_remoteAddrU = protocol.IpAddrToU(remoteAddr)
		_remotePort = remotePort
	} else if i.NatType == NatTypeFullCone {
		_remoteAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	wanPort := uint16(0)
	if lanHostPort != 0 {
		// nat端口分配
		portAlloc, exist := i.NatPortAlloc[_remoteAddrU]
		if !exist {
			portAlloc = &PortAlloc{
				AllocPortMap:  make(map[PortAllocSrcAddr]uint16),
				RemainPortMap: make(map[uint16]struct{}),
			}
			port := uint16(32768)
			for {
				portAlloc.RemainPortMap[port] = struct{}{}
				port++
				if port == 0 {
					break
				}
			}
			i.NatPortAlloc[_remoteAddrU] = portAlloc
		}
		portAlloc.Lock.RLock()
		wanPort, exist = portAlloc.AllocPortMap[PortAllocSrcAddr{IpAddr: protocol.IpAddrToU(lanHostAddr), Port: lanHostPort}]
		portAlloc.Lock.RUnlock()
		if !exist {
			portAlloc.Lock.Lock()
			if len(portAlloc.RemainPortMap) != 0 {
				for port := range portAlloc.RemainPortMap {
					wanPort = port
					portAlloc.AllocPortMap[PortAllocSrcAddr{IpAddr: protocol.IpAddrToU(lanHostAddr), Port: lanHostPort}] = port
					delete(portAlloc.RemainPortMap, port)
					break
				}
			}
			portAlloc.Lock.Unlock()
		}
		if wanPort == 0 {
			return nil
		}
	}
	natFlowHash := NatFlowHash{
		RemoteIpAddr:  _remoteAddrU,
		RemotePort:    _remotePort,
		LanHostIpAddr: protocol.IpAddrToU(lanHostAddr),
		LanHostPort:   lanHostPort,
		Ipv4HeadProto: ipv4HeadProto,
	}
	natFlow := &NatFlow{
		NatFlowHash:   natFlowHash,
		RemoteIpAddr:  _remoteAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(i.IpAddr),
		WanPort:       wanPort,
		LanHostIpAddr: protocol.IpAddrToU(lanHostAddr),
		LanHostPort:   lanHostPort,
		Ipv4HeadProto: ipv4HeadProto,
		LastAliveTime: uint32(time.Now().Unix()),
	}
	i.NatFlowTable[natFlowHash] = natFlow
	i.NatWanFlowTable[NatWanFlowHash{
		RemoteIpAddr:  _remoteAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(i.IpAddr),
		WanPort:       wanPort,
		Ipv4HeadProto: ipv4HeadProto,
	}] = natFlow
	return natFlow
}

func (i *NetIf) NatTableClear() {
	ticker := time.NewTicker(time.Minute * 1)
	for {
		<-ticker.C
		if i.Engine.Stop.Load() {
			break
		}
		now := uint32(time.Now().Unix())
		i.NatTableLock.Lock()
		for natFlowHash, natFlow := range i.NatFlowTable {
			if now-natFlow.LastAliveTime > 60 {
				delete(i.NatFlowTable, natFlowHash)
				delete(i.NatWanFlowTable, NatWanFlowHash{
					RemoteIpAddr: natFlow.RemoteIpAddr,
					RemotePort:   natFlow.RemotePort,
					WanIpAddr:    natFlow.WanIpAddr,
					WanPort:      natFlow.WanPort,
				})
				portAlloc := i.NatPortAlloc[natFlow.RemoteIpAddr]
				if portAlloc == nil {
					continue
				}
				portAlloc.Lock.Lock()
				delete(portAlloc.AllocPortMap, PortAllocSrcAddr{IpAddr: natFlow.LanHostIpAddr, Port: natFlow.LanHostPort})
				portAlloc.RemainPortMap[natFlow.WanPort] = struct{}{}
				portAlloc.Lock.Unlock()
			}
		}
		i.NatTableLock.Unlock()
	}
}

func (i *NetIf) DhcpLeaseClear() {
	ticker := time.NewTicker(time.Minute * 1)
	for {
		<-ticker.C
		if i.Engine.Stop.Load() {
			break
		}
		now := time.Now().Unix()
		i.DhcpLock.Lock()
		for ipAddrU, dhcpLease := range i.DhcpLeaseMap {
			if now > dhcpLease.ExpTime {
				delete(i.DhcpLeaseMap, ipAddrU)
			}
		}
		i.DhcpLock.Unlock()
	}
}

const (
	LanToWan = iota
	WanToLan
)

func (i *NetIf) CheckNatPortMapping(dir int, ipAddr []byte, port uint16, ipv4HeadProto uint8) *NatPortMappingEntry {
	if ipv4HeadProto != protocol.IPH_PROTO_TCP && ipv4HeadProto != protocol.IPH_PROTO_UDP {
		return nil
	}
	var natPortMappingEntry *NatPortMappingEntry = nil
	if dir == LanToWan {
		for _, entry := range i.NatPortMappingTable {
			if entry.LanHostIpAddr == protocol.IpAddrToU(ipAddr) && entry.LanHostPort == port && entry.Ipv4HeadProto == ipv4HeadProto {
				natPortMappingEntry = entry
				break
			}
		}
	} else if dir == WanToLan {
		for _, entry := range i.NatPortMappingTable {
			if entry.WanIpAddr == protocol.IpAddrToU(ipAddr) && entry.WanPort == port && entry.Ipv4HeadProto == ipv4HeadProto {
				natPortMappingEntry = entry
				break
			}
		}
	}
	if natPortMappingEntry != nil {
		return natPortMappingEntry
	}
	return nil
}

func (i *NetIf) SendUdpPktByFlow(natFlowHash NatFlowHash, dir int, udpPayload []byte) {
	natFlowHash.Ipv4HeadProto = protocol.IPH_PROTO_UDP
	remoteIpAddr := protocol.UToIpAddr(natFlowHash.RemoteIpAddr)
	lanHostIpAddr := protocol.UToIpAddr(natFlowHash.LanHostIpAddr)
	natFlow := i.NatGetFlowByHash(natFlowHash)
	if natFlow == nil {
		natFlow = i.NatAddFlow(lanHostIpAddr, remoteIpAddr, natFlowHash.LanHostPort, natFlowHash.RemotePort, natFlowHash.Ipv4HeadProto)
		if natFlow == nil {
			return
		}
	}
	natFlow.LastAliveTime = uint32(time.Now().Unix())
	switch dir {
	case LanToWan:
		udpPkt := make([]byte, 0, 1480)
		udpPkt, err := protocol.BuildUdpPkt(udpPkt, udpPayload, natFlow.WanPort, natFlow.RemotePort, i.IpAddr, remoteIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt := make([]byte, 0, 1500)
		ipv4Pkt, err = protocol.BuildIpv4Pkt(ipv4Pkt, udpPkt, protocol.IPH_PROTO_UDP, i.IpAddr, remoteIpAddr)
		if err != nil {
			return
		}
		nextHopIpAddr, _ := i.FindRoute(remoteIpAddr)
		if nextHopIpAddr == nil {
			return
		}
		ethDstMac := i.GetArpCache(nextHopIpAddr)
		if ethDstMac == nil {
			return
		}
		i.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	case WanToLan:
		udpPkt := make([]byte, 0, 1480)
		udpPkt, err := protocol.BuildUdpPkt(udpPkt, udpPayload, natFlow.RemotePort, natFlow.LanHostPort, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt := make([]byte, 0, 1500)
		ipv4Pkt, err = protocol.BuildIpv4Pkt(ipv4Pkt, udpPkt, protocol.IPH_PROTO_UDP, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		_, outNetIfName := i.FindRoute(lanHostIpAddr)
		if outNetIfName == "" {
			return
		}
		outNetIf := i.Engine.NetIfMap[outNetIfName]
		ethDstMac := outNetIf.GetArpCache(lanHostIpAddr)
		if ethDstMac == nil {
			return
		}
		outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	default:
	}
}

// RouteTable 路由表
type RouteTable struct {
	Root *TrieNode    // 根节点
	Lock sync.RWMutex // 锁
}

// TrieNode 路由树节点
type TrieNode struct {
	Route *RouteEntry // 路由信息
	Left  *TrieNode   // 左节点
	Right *TrieNode   // 右节点
}

// RouteEntry 路由条目
type RouteEntry struct {
	DstIpAddr   []byte // 目的ip地址
	NetworkMask []byte // 网络掩码
	NextHop     []byte // 下一跳
	NetIf       string // 出接口
}

func (r *RouteTable) AddRoute(route *RouteEntry) {
	r.UpdateRoute(route, route)
}

func (r *RouteTable) DeleteRoute(route *RouteEntry) {
	r.UpdateRoute(route, nil)
}

func (r *RouteTable) UpdateRoute(oldRoute *RouteEntry, newRoute *RouteEntry) {
	r.Lock.Lock()
	defer r.Lock.Unlock()
	node := r.Root
	maskSize := 0
	networkMaskU := protocol.IpAddrToU(oldRoute.NetworkMask)
	if networkMaskU != 0 {
		for i := 1; i <= 32; i++ {
			maskSize++
			if networkMaskU<<i == 0 {
				break
			}
		}
	}
	for i := 0; i < maskSize; i++ {
		bit := (oldRoute.DstIpAddr[i/8] >> (7 - uint(i%8))) & 1
		if bit == 0 {
			if node.Left == nil {
				node.Left = new(TrieNode)
			}
			node = node.Left
		} else {
			if node.Right == nil {
				node.Right = new(TrieNode)
			}
			node = node.Right
		}
	}
	node.Route = newRoute
}

func (r *RouteTable) FindRoute(ip []byte) *RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	node := r.Root
	var lastMatch *RouteEntry
	for i := 0; i < 32; i++ {
		if node.Route != nil {
			lastMatch = node.Route
		}
		bit := (ip[i/8] >> (7 - uint(i%8))) & 1
		if bit == 0 {
			if node.Left == nil {
				break
			}
			node = node.Left
		} else {
			if node.Right == nil {
				break
			}
			node = node.Right
		}
	}
	if node.Route != nil {
		lastMatch = node.Route
	}
	return lastMatch
}

func (r *RouteTable) ListRoute() []*RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	return r._ForNode(r.Root)
}

func (r *RouteTable) _ForNode(node *TrieNode) []*RouteEntry {
	if node == nil {
		return nil
	}
	result := make([]*RouteEntry, 0)
	if node.Route != nil {
		result = append(result, node.Route)
	}
	routeList := r._ForNode(node.Left)
	for _, route := range routeList {
		result = append(result, route)
	}
	routeList = r._ForNode(node.Right)
	for _, route := range routeList {
		result = append(result, route)
	}
	return result
}

func (i *NetIf) FindRoute(ipv4DstAddr []byte) (nextHopIpAddr []byte, outNetIfName string) {
	route := i.Engine.RouteTable.FindRoute(ipv4DstAddr)
	if route != nil {
		nextHopIpAddr = route.NextHop
		outNetIfName = route.NetIf
	}
	return nextHopIpAddr, outNetIfName
}
