package engine

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxIpv4(ethPayload []byte) {
	ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse ip packet error: %v\n", err))
		return
	}
	if ipv4DstAddr[3] == 255 {
		if ipv4HeadProto == protocol.IPH_PROTO_UDP {
			i.RxUdp(ipv4Payload, ipv4SrcAddr)
		}
		return
	}
	if !bytes.Equal(ipv4DstAddr, i.IpAddr) || i.Config.NatEnable {
		ok := i.Ipv4RouteForward(ethPayload, ipv4SrcAddr, ipv4DstAddr, ipv4HeadProto)
		if !ok && ipv4HeadProto == protocol.IPH_PROTO_ICMP {
			i.RxIcmp(ipv4Payload, ipv4SrcAddr)
		}
		return
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
}

func (i *NetIf) TxIpv4(ipv4Payload []byte, ipv4HeadProto uint8, ipv4DstAddr []byte) bool {
	ipv4Pkt := make([]byte, 0, 1500)
	ipv4Pkt, err := protocol.BuildIpv4Pkt(ipv4Pkt, ipv4Payload, ipv4HeadProto, i.IpAddr, ipv4DstAddr)
	if err != nil {
		Log(fmt.Sprintf("build ip packet error: %v\n", err))
		return false
	}
	// 三层路由
	var nextHopIpAddr []byte = nil
	var outNetIf *NetIf = nil
	if ipv4DstAddr[3] == 255 {
		outNetIf = i
	} else {
		_nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
		if _nextHopIpAddr == nil && outNetIfName == "" {
			Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
			return false
		}
		nextHopIpAddr = _nextHopIpAddr
		outNetIf = i.Engine.NetIfMap[outNetIfName]
		dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
		outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
		if dstIpAddrU == outNetIfIpAddrU {
			// 本地回环
			_ipv4Pkt := make([]byte, len(ipv4Pkt))
			copy(_ipv4Pkt, ipv4Pkt)
			outNetIf.LoChan <- _ipv4Pkt
			return true
		}
	}
	// 二层封装
	var ethDstMac []byte = nil
	if ipv4DstAddr[3] == 255 {
		ethDstMac = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	} else if nextHopIpAddr != nil {
		ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
	}
	if ethDstMac == nil {
		return false
	}
	return outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
}

const (
	LanToWan = iota
	WanToLan
)

func (i *NetIf) Ipv4RouteForward(ethPayload []byte, ipv4SrcAddr []byte, ipv4DstAddr []byte, ipv4HeadProto uint8) bool {
	// DNAT 公网地址 -> 私网地址
	if i.Config.NatEnable {
		srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
		isIcmpTtl := false
		ethPayload, isIcmpTtl = i.IcmpTtlDeepNat(ethPayload)
		if !isIcmpTtl {
			natPortMappingEntry := i.CheckNatPortMapping(WanToLan, i.IpAddr, dstPort, ipv4HeadProto)
			if natPortMappingEntry == nil {
				natFlow := i.NatGetFlowByWan(ipv4SrcAddr, srcPort, ipv4DstAddr, dstPort, ipv4HeadProto)
				if natFlow == nil {
					// 没有nat表项
					return false
				}
				natFlow.LastAliveTime = uint32(time.Now().Unix())
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
			} else {
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natPortMappingEntry.LanHostIpAddr), natPortMappingEntry.LanHostPort)
			}
		}
	}
	// 处理ttl
	alive := false
	ethPayload, alive = protocol.HandleIpv4PktTtl(ethPayload)
	if !alive {
		// ttl超时
		if len(ethPayload) > 28 {
			ethPayload = ethPayload[:28]
		}
		i.TxIcmp(ethPayload, protocol.ICMP_TTL, []byte{0x00, 0x00}, 0, ipv4SrcAddr)
		return true
	}
	// 外部钩子回调
	if i.Engine.Ipv4PktFwdHook != nil {
		dir := 0
		if i.Config.NatEnable {
			dir = WanToLan
		} else {
			dir = LanToWan
		}
		drop, mod := i.Engine.Ipv4PktFwdHook(ethPayload, dir)
		if drop {
			// 外部钩子回调强制丢弃
			return true
		}
		ethPayload = mod
	}
	// 三层路由
	nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
	if nextHopIpAddr == nil && outNetIfName == "" {
		// 没有路由
		Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
		return true
	}
	outNetIf := i.Engine.NetIfMap[outNetIfName]
	dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
	outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
	if dstIpAddrU == outNetIfIpAddrU && !i.Config.NatEnable {
		// 本地回环
		outNetIf.LoChan <- ethPayload
		return true
	}
	// SNAT 私网地址 -> 公网地址
	if outNetIf.Config.NatEnable {
		srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
		natPortMappingEntry := outNetIf.CheckNatPortMapping(LanToWan, ipv4SrcAddr, srcPort, ipv4HeadProto)
		if natPortMappingEntry == nil {
			natFlow := outNetIf.NatGetFlowByHash(NatFlowHash{
				RemoteIpAddr:  protocol.IpAddrToU(ipv4DstAddr),
				RemotePort:    dstPort,
				LanHostIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
				LanHostPort:   srcPort,
				Ipv4HeadProto: ipv4HeadProto,
			})
			if natFlow == nil {
				natFlow = outNetIf.NatAddFlow(ipv4SrcAddr, ipv4DstAddr, srcPort, dstPort, ipv4HeadProto)
				if natFlow == nil {
					// nat端口分配失败
					return true
				}
			}
			natFlow.LastAliveTime = uint32(time.Now().Unix())
			ethPayload = protocol.NatChangeSrc(ethPayload, protocol.UToIpAddr(natFlow.WanIpAddr), natFlow.WanPort)
		} else {
			ethPayload = protocol.NatChangeSrc(ethPayload, outNetIf.IpAddr, natPortMappingEntry.WanPort)
		}
	}
	// 二层封装
	var ethDstMac []byte = nil
	if nextHopIpAddr != nil {
		ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
	}
	if ethDstMac == nil {
		// 二层地址查询失败
		return true
	}
	outNetIf.TxEthernet(ethPayload, ethDstMac, protocol.ETH_PROTO_IPV4)
	return true
}

// RouteTable 路由表
type RouteTable struct {
	Root *TrieNode    // 根节点
	Lock sync.RWMutex // 锁
}

// TrieNode 路由树节点
type TrieNode struct {
	RouteList []*RouteEntry // 路由信息
	Left      *TrieNode     // 左节点
	Right     *TrieNode     // 右节点
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
	newRouteList := make([]*RouteEntry, 0, len(node.RouteList))
	for _, routeEntry := range node.RouteList {
		if protocol.IpAddrToU(routeEntry.DstIpAddr) == protocol.IpAddrToU(oldRoute.DstIpAddr) &&
			protocol.IpAddrToU(routeEntry.NetworkMask) == protocol.IpAddrToU(oldRoute.NetworkMask) &&
			protocol.IpAddrToU(routeEntry.NextHop) == protocol.IpAddrToU(oldRoute.NextHop) &&
			routeEntry.NetIf == oldRoute.NetIf {
			continue
		}
		newRouteList = append(newRouteList, routeEntry)
	}
	if newRoute != nil {
		newRouteList = append(newRouteList, newRoute)
	}
	node.RouteList = newRouteList
}

func (r *RouteTable) FindRoute(ip []byte) *RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	node := r.Root
	var lastMatch []*RouteEntry
	for i := 0; i < 32; i++ {
		if node.RouteList != nil {
			lastMatch = node.RouteList
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
	if node.RouteList != nil {
		lastMatch = node.RouteList
	}
	h := fnv.New32a()
	_, _ = h.Write(ip)
	return lastMatch[h.Sum32()%uint32(len(lastMatch))]
}

func (r *RouteTable) ListRoute() []*RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	return r.foreachNode(r.Root)
}

func (r *RouteTable) foreachNode(node *TrieNode) []*RouteEntry {
	if node == nil {
		return nil
	}
	result := make([]*RouteEntry, 0)
	if node.RouteList != nil {
		result = append(result, node.RouteList...)
	}
	routeList := r.foreachNode(node.Left)
	for _, route := range routeList {
		result = append(result, route)
	}
	routeList = r.foreachNode(node.Right)
	for _, route := range routeList {
		result = append(result, route)
	}
	return result
}

func (i *NetIf) FindRoute(ipv4DstAddr []byte) ([]byte, string) {
	route := i.Engine.RouteTable.FindRoute(ipv4DstAddr)
	if route == nil {
		return nil, ""
	}
	return route.NextHop, route.NetIf
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
	i.NatLock.RLock()
	natFlow, exist := i.NatFlowTable[natFlowHash]
	i.NatLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatGetFlowByWan(remoteIpAddr []byte, remotePort uint16, wanIpAddr []byte, wanPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.Config.NatType == NatTypeSymmetric {
		_remoteAddrU = protocol.IpAddrToU(remoteIpAddr)
		_remotePort = remotePort
	} else if i.Config.NatType == NatTypeFullCone {
		_remoteAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	i.NatLock.RLock()
	natFlow, exist := i.NatWanFlowTable[NatWanFlowHash{
		RemoteIpAddr:  _remoteAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(wanIpAddr),
		WanPort:       wanPort,
		Ipv4HeadProto: ipv4HeadProto,
	}]
	i.NatLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatAddFlow(lanHostAddr []byte, remoteAddr []byte, lanHostPort uint16, remotePort uint16, ipv4HeadProto uint8) *NatFlow {
	i.NatLock.Lock()
	defer i.NatLock.Unlock()
	_remoteAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.Config.NatType == NatTypeSymmetric {
		_remoteAddrU = protocol.IpAddrToU(remoteAddr)
		_remotePort = remotePort
	} else if i.Config.NatType == NatTypeFullCone {
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
			if entry.WanPort == port && entry.Ipv4HeadProto == ipv4HeadProto {
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

func (i *NetIf) NatTableClear() {
	ticker := time.NewTicker(time.Minute * 1)
	for {
		<-ticker.C
		if i.Engine.Stop.Load() {
			break
		}
		now := uint32(time.Now().Unix())
		i.NatLock.Lock()
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
		i.NatLock.Unlock()
	}
}
