package engine

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flswld/halo/protocol"
)

type Config struct {
	DebugLog     bool                  // 调试日志
	NetIfList    []*NetIfConfig        // 网卡列表
	RoutingTable []*RoutingEntryConfig // 路由表
}

type NetIfConfig struct {
	Name        string      // 网卡名
	MacAddr     string      // mac地址
	IpAddr      string      // ip地址
	NetworkMask string      // 子网掩码
	NatEnable   bool        // 网络地址转换
	EthRxChan   chan []byte // 物理层接收管道
	EthTxChan   chan []byte // 物理层发送管道
}

type NatWan struct {
	RemoteIpAddr uint32 // 远程ip地址
	RemotePort   uint16 // 远程端口
	WanIpAddr    uint32 // wan口ip地址
	WanPort      uint16 // wan口端口
}

type NatFlowHash struct {
	RemoteIpAddr  uint32 // 远程ip地址
	RemotePort    uint16 // 远程端口
	LanHostIpAddr uint32 // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
}

type NatFlow struct {
	RemoteIpAddr  uint32 // 远程ip地址
	RemotePort    uint16 // 远程端口
	WanIpAddr     uint32 // wan口ip地址
	WanPort       uint16 // wan口端口
	LanHostIpAddr uint32 // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	LastAliveTime uint32 // 上一次活跃时间
}

type NetIf struct {
	Name              string                   // 接口名
	MacAddr           []byte                   // mac地址
	IpAddr            []byte                   // ip地址
	NetworkMask       []byte                   // 子网掩码
	EthRxChan         chan []byte              // 物理层接收管道
	EthTxChan         chan []byte              // 物理层发送管道
	LoChan            chan []byte              // 本地回环管道
	Engine            *Engine                  // 归属Engine指针
	ArpCacheTable     map[uint32]uint64        // arp缓存表 key:ip value:mac
	ArpCacheTableLock sync.RWMutex             // arp缓存表锁
	NatEnable         bool                     // 是否开启nat
	NatWanTable       map[NatWan]*NatFlow      // nat表
	NatFlowTable      map[NatFlowHash]*NatFlow // nat表
	NatTableLock      sync.RWMutex             // nat表锁
	NatPortAlloc      map[uint32]*PortAlloc    // nat端口分配表 key:目的ip value:端口分配信息
	NatPortAllocLock  sync.RWMutex             // nat端口分配表锁
	HandleUdp         func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte)
}

type PortAlloc struct {
	Lock          sync.RWMutex        // 锁
	AllocPortMap  map[SrcAddr]uint16  // 分配端口集合 key:源地址 value:端口
	RemainPortMap map[uint16]struct{} // 剩余端口集合 key:端口
}

type SrcAddr struct {
	IpAddr uint32 // ip地址
	Port   uint16 // 端口
}

type RoutingEntryConfig struct {
	DstIpAddr   string // 目的ip地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳
	NetIf       string // 出接口
}

type RoutingEntry struct {
	DstIpAddr   []byte // 目的ip地址
	NetworkMask []byte // 网络掩码
	NextHop     []byte // 下一跳
	NetIf       string // 出接口
}

type Engine struct {
	DebugLog       bool                                     // 调试日志
	Stop           bool                                     // 停止标志
	NetIfMap       map[string]*NetIf                        // 网络接口集合 key:接口名 value:接口实例
	RoutingTable   []*RoutingEntry                          // 路由表
	Ipv4PktFwdHook func(raw []byte) (drop bool, mod []byte) // ip报文转发钩子
}

var (
	BROADCAST_MAC_ADDR = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
)

func ParseMacAddr(macAddrStr string) ([]byte, error) {
	macAddrSplit := strings.Split(macAddrStr, ":")
	macAddr := make([]byte, 6)
	for i := 0; i < 6; i++ {
		split, err := strconv.ParseUint(macAddrSplit[i], 16, 8)
		if err != nil {
			return nil, err
		}
		macAddr[i] = uint8(split)
	}
	return macAddr, nil
}

func ParseIpAddr(ipAddrStr string) ([]byte, error) {
	ipAddrSplit := strings.Split(ipAddrStr, ".")
	ipAddr := make([]byte, 4)
	for i := 0; i < 4; i++ {
		split, err := strconv.Atoi(ipAddrSplit[i])
		if err != nil {
			return nil, err
		}
		ipAddr[i] = uint8(split)
	}
	return ipAddr, nil
}

func InitEngine(config *Config) (*Engine, error) {
	r := new(Engine)
	r.DebugLog = config.DebugLog
	r.Stop = false
	r.NetIfMap = make(map[string]*NetIf)
	r.RoutingTable = make([]*RoutingEntry, 0)
	r.Ipv4PktFwdHook = nil
	// 网卡列表
	for _, netIfConfig := range config.NetIfList {
		macAddr, err := ParseMacAddr(netIfConfig.MacAddr)
		if err != nil {
			return nil, err
		}
		ipAddr, err := ParseIpAddr(netIfConfig.IpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := ParseIpAddr(netIfConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		netIf := &NetIf{
			Name:              netIfConfig.Name,
			MacAddr:           macAddr,
			IpAddr:            ipAddr,
			NetworkMask:       networkMask,
			EthRxChan:         netIfConfig.EthRxChan,
			EthTxChan:         netIfConfig.EthTxChan,
			LoChan:            make(chan []byte, 1024),
			Engine:            r,
			ArpCacheTable:     make(map[uint32]uint64),
			ArpCacheTableLock: sync.RWMutex{},
			NatEnable:         netIfConfig.NatEnable,
			NatWanTable:       make(map[NatWan]*NatFlow),
			NatFlowTable:      make(map[NatFlowHash]*NatFlow),
			NatTableLock:      sync.RWMutex{},
			NatPortAlloc:      make(map[uint32]*PortAlloc),
			NatPortAllocLock:  sync.RWMutex{},
			HandleUdp:         nil,
		}
		r.NetIfMap[netIf.Name] = netIf
	}
	// 路由表
	for _, routingEntryConfig := range config.RoutingTable {
		dstIpAddr, err := ParseIpAddr(routingEntryConfig.DstIpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := ParseIpAddr(routingEntryConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		nextHop, err := ParseIpAddr(routingEntryConfig.NextHop)
		if err != nil {
			return nil, err
		}
		r.RoutingTable = append(r.RoutingTable, &RoutingEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: networkMask,
			NextHop:     nextHop,
			NetIf:       routingEntryConfig.NetIf,
		})
	}
	// 直连路由
	for _, netIf := range r.NetIfMap {
		dstIpAddrU := protocol.IpAddrToU(netIf.IpAddr) & protocol.IpAddrToU(netIf.NetworkMask)
		dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
		r.RoutingTable = append(r.RoutingTable, &RoutingEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: netIf.NetworkMask,
			NextHop:     nil,
			NetIf:       netIf.Name,
		})
	}
	sort.Slice(r.RoutingTable, func(i, j int) bool {
		ii := protocol.IpAddrToU(r.RoutingTable[i].DstIpAddr) & protocol.IpAddrToU(r.RoutingTable[i].NetworkMask)
		jj := protocol.IpAddrToU(r.RoutingTable[j].DstIpAddr) & protocol.IpAddrToU(r.RoutingTable[j].NetworkMask)
		return ii > jj
	})
	protocol.SetRandIpHeaderId()
	return r, nil
}

func (e *Engine) RunEngine() {
	for _, netIf := range e.NetIfMap {
		go netIf.PacketHandle()
		if netIf.NatEnable {
			go netIf.NatTableClear()
		}
	}
	time.Sleep(time.Second * 10)
}

func (e *Engine) GetNetIf(name string) *NetIf {
	return e.NetIfMap[name]
}

func (e *Engine) StopEngine() {
	e.Stop = true
}

func (i *NetIf) PacketHandle() {
	for {
		if i.Engine.Stop {
			break
		}
		select {
		case ethFrm := <-i.EthRxChan:
			i.RxEthernet(ethFrm)
		case ipv4Pkt := <-i.LoChan:
			ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ipv4Pkt)
			if err != nil {
				fmt.Printf("parse ip packet error: %v\n", err)
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
				i.RxTcp()
			default:
			}
		}
	}
}

func (i *NetIf) FindRoute(ipv4DstAddr []byte) (nextHopIpAddr []byte, outNetIfName string) {
	dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
	lpmNetworkMaskU := uint32(0)
	for _, routingEntry := range i.Engine.RoutingTable {
		routeDstIpAddrU := protocol.IpAddrToU(routingEntry.DstIpAddr)
		routeNetworkMaskU := protocol.IpAddrToU(routingEntry.NetworkMask)
		if dstIpAddrU&routeNetworkMaskU != routeDstIpAddrU {
			continue
		}
		if routeNetworkMaskU >= lpmNetworkMaskU {
			lpmNetworkMaskU = routeNetworkMaskU
			nextHopIpAddr = routingEntry.NextHop
			outNetIfName = routingEntry.NetIf
		}
	}
	return nextHopIpAddr, outNetIfName
}

func (i *NetIf) NatGetFlowByWan(remoteIpAddr []byte, remotePort uint16, wanIpAddr []byte, wanPort uint16) *NatFlow {
	i.NatTableLock.RLock()
	natFlow, exist := i.NatWanTable[NatWan{
		RemoteIpAddr: protocol.IpAddrToU(remoteIpAddr),
		RemotePort:   remotePort,
		WanIpAddr:    protocol.IpAddrToU(wanIpAddr),
		WanPort:      wanPort,
	}]
	i.NatTableLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatAddFlow(ipv4SrcAddr []byte, ipv4DstAddr []byte, srcPort uint16, dstPort uint16) *NatFlow {
	wanPort := uint16(0)
	if srcPort != 0 {
		// nat端口分配
		i.NatPortAllocLock.RLock()
		portAlloc, exist := i.NatPortAlloc[protocol.IpAddrToU(ipv4DstAddr)]
		i.NatPortAllocLock.RUnlock()
		if !exist {
			portAlloc = &PortAlloc{
				Lock:          sync.RWMutex{},
				AllocPortMap:  make(map[SrcAddr]uint16),
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
			i.NatPortAllocLock.Lock()
			i.NatPortAlloc[protocol.IpAddrToU(ipv4DstAddr)] = portAlloc
			i.NatPortAllocLock.Unlock()
		}
		portAlloc.Lock.RLock()
		wanPort, exist = portAlloc.AllocPortMap[SrcAddr{IpAddr: protocol.IpAddrToU(ipv4SrcAddr), Port: srcPort}]
		portAlloc.Lock.RUnlock()
		if !exist {
			portAlloc.Lock.Lock()
			if len(portAlloc.RemainPortMap) != 0 {
				for port := range portAlloc.RemainPortMap {
					wanPort = port
					portAlloc.AllocPortMap[SrcAddr{IpAddr: protocol.IpAddrToU(ipv4SrcAddr), Port: srcPort}] = port
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
	i.NatTableLock.Lock()
	natFlow := &NatFlow{
		RemoteIpAddr:  protocol.IpAddrToU(ipv4DstAddr),
		RemotePort:    dstPort,
		WanIpAddr:     protocol.IpAddrToU(i.IpAddr),
		WanPort:       wanPort,
		LanHostIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
		LanHostPort:   srcPort,
		LastAliveTime: uint32(time.Now().Unix()),
	}
	i.NatFlowTable[NatFlowHash{
		RemoteIpAddr:  protocol.IpAddrToU(ipv4DstAddr),
		RemotePort:    dstPort,
		LanHostIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
		LanHostPort:   srcPort,
	}] = natFlow
	i.NatWanTable[NatWan{
		RemoteIpAddr: protocol.IpAddrToU(ipv4DstAddr),
		RemotePort:   dstPort,
		WanIpAddr:    protocol.IpAddrToU(i.IpAddr),
		WanPort:      wanPort,
	}] = natFlow
	i.NatTableLock.Unlock()
	return natFlow
}

func (i *NetIf) NatTableClear() {
	ticker := time.NewTicker(time.Minute * 10)
	for {
		<-ticker.C
		if i.Engine.Stop {
			break
		}
		now := uint32(time.Now().Unix())
		i.NatTableLock.Lock()
		for natFlowHash, natFlow := range i.NatFlowTable {
			if now-natFlow.LastAliveTime > 600 {
				delete(i.NatFlowTable, natFlowHash)
				delete(i.NatWanTable, NatWan{
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
				delete(portAlloc.AllocPortMap, SrcAddr{IpAddr: natFlow.LanHostIpAddr, Port: natFlow.LanHostPort})
				portAlloc.RemainPortMap[natFlow.WanPort] = struct{}{}
				portAlloc.Lock.Unlock()
			}
		}
		i.NatTableLock.Unlock()
	}
}

const (
	LanToWan = iota
	WanToLan
)

func (i *NetIf) SendUdpPktByFlow(natFlowHash NatFlowHash, dir int, udpPayload []byte) {
	i.NatTableLock.RLock()
	natFlow, exist := i.NatFlowTable[natFlowHash]
	i.NatTableLock.RUnlock()
	if !exist {
		return
	}
	remoteIpAddr := protocol.UToIpAddr(natFlow.RemoteIpAddr)
	lanHostIpAddr := protocol.UToIpAddr(natFlow.LanHostIpAddr)
	switch dir {
	case LanToWan:
		udpPkt, err := protocol.BuildUdpPkt(udpPayload, natFlow.WanPort, natFlow.RemotePort, i.IpAddr, remoteIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt, err := protocol.BuildIpv4Pkt(udpPkt, protocol.IPH_PROTO_UDP, i.IpAddr, remoteIpAddr)
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
		_, outNetIfName := i.FindRoute(lanHostIpAddr)
		if outNetIfName == "" {
			return
		}
		outNetIf := i.Engine.NetIfMap[outNetIfName]
		udpPkt, err := protocol.BuildUdpPkt(udpPayload, natFlow.RemotePort, natFlow.LanHostPort, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt, err := protocol.BuildIpv4Pkt(udpPkt, protocol.IPH_PROTO_UDP, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		ethDstMac := outNetIf.GetArpCache(lanHostIpAddr)
		if ethDstMac == nil {
			return
		}
		outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	default:
	}
}
