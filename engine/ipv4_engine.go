package engine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/flswld/halo/hashmap"
	"github.com/flswld/halo/mem"
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
			i.RxUdpBroadcast(ipv4Payload, ipv4SrcAddr, ipv4DstAddr)
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
	var arpCache *ArpCache = nil
	if ipv4DstAddr[3] == 255 {
		ethDstMac = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	} else if nextHopIpAddr != nil {
		arpCache = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		arpCache = outNetIf.GetArpCache(ipv4DstAddr)
	}
	if ethDstMac != nil {
		return outNetIf.SendEthernet(ipv4Pkt, ethDstMac, outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
	} else if arpCache != nil {
		return outNetIf.SendEthernet(ipv4Pkt, arpCache.MacAddr[:], outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
	} else {
		return false
	}
}

// 流方向
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
				natFlow.LastAliveTime = i.Engine.TimeNow
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
			natFlow := outNetIf.NatGetFlowByHash(ipv4DstAddr, dstPort, ipv4SrcAddr, srcPort, ipv4HeadProto)
			if natFlow == nil {
				natFlow = outNetIf.NatAddFlow(ipv4SrcAddr, ipv4DstAddr, srcPort, dstPort, ipv4HeadProto)
				if natFlow == nil {
					// nat端口分配失败
					return true
				}
			}
			natFlow.LastAliveTime = i.Engine.TimeNow
			ethPayload = protocol.NatChangeSrc(ethPayload, protocol.UToIpAddr(natFlow.WanIpAddr), natFlow.WanPort)
		} else {
			ethPayload = protocol.NatChangeSrc(ethPayload, outNetIf.IpAddr, natPortMappingEntry.WanPort)
		}
	}
	// 二层封装
	var arpCache *ArpCache = nil
	if nextHopIpAddr != nil {
		arpCache = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		arpCache = outNetIf.GetArpCache(ipv4DstAddr)
	}
	if arpCache == nil {
		// 二层地址查询失败
		return true
	}
	outNetIf.SendEthernet(ethPayload, arpCache.MacAddr[:], outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
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
	if lastMatch == nil {
		return nil
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
	ret := make([]*RouteEntry, 0)
	if node.RouteList != nil {
		ret = append(ret, node.RouteList...)
	}
	routeList := r.foreachNode(node.Left)
	for _, route := range routeList {
		ret = append(ret, route)
	}
	routeList = r.foreachNode(node.Right)
	for _, route := range routeList {
		ret = append(ret, route)
	}
	return ret
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

func (h NatFlowHash) GetHashCode() uint64 {
	data := make([]byte, 13)
	binary.LittleEndian.PutUint32(data[0:4], h.RemoteIpAddr)
	binary.LittleEndian.PutUint16(data[4:6], h.RemotePort)
	binary.LittleEndian.PutUint32(data[6:10], h.LanHostIpAddr)
	binary.LittleEndian.PutUint16(data[10:12], h.LanHostPort)
	data[12] = h.Ipv4HeadProto
	return hashmap.GetHashCode(data)
}

// NatWanFlowHash WAN口NAT流摘要
type NatWanFlowHash struct {
	RemoteIpAddr  uint32 // 远程ip地址
	RemotePort    uint16 // 远程端口
	WanIpAddr     uint32 // wan口ip地址
	WanPort       uint16 // wan口端口
	Ipv4HeadProto uint8  // ip头部协议
}

func (h NatWanFlowHash) GetHashCode() uint64 {
	data := make([]byte, 13)
	binary.LittleEndian.PutUint32(data[0:4], h.RemoteIpAddr)
	binary.LittleEndian.PutUint16(data[4:6], h.RemotePort)
	binary.LittleEndian.PutUint32(data[6:10], h.WanIpAddr)
	binary.LittleEndian.PutUint16(data[10:12], h.WanPort)
	data[12] = h.Ipv4HeadProto
	return hashmap.GetHashCode(data)
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
	UsePortMap *hashmap.HashMap[PortHash, struct{}] // 已使用端口集合
}

func (i *NetIf) NatGetFlowByHash(remoteIpAddr []byte, remotePort uint16, lanHostIpAddr []byte, lanHostPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteIpAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.Config.NatType == NatTypeSymmetric {
		_remoteIpAddrU = protocol.IpAddrToU(remoteIpAddr)
		_remotePort = remotePort
	} else if i.Config.NatType == NatTypeFullCone {
		_remoteIpAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	i.NatLock.RLock()
	natFlow, exist := i.NatFlowTable.Get(NatFlowHash{
		RemoteIpAddr:  _remoteIpAddrU,
		RemotePort:    _remotePort,
		LanHostIpAddr: protocol.IpAddrToU(lanHostIpAddr),
		LanHostPort:   lanHostPort,
		Ipv4HeadProto: ipv4HeadProto,
	})
	i.NatLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatGetFlowByWan(remoteIpAddr []byte, remotePort uint16, wanIpAddr []byte, wanPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteIpAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.Config.NatType == NatTypeSymmetric {
		_remoteIpAddrU = protocol.IpAddrToU(remoteIpAddr)
		_remotePort = remotePort
	} else if i.Config.NatType == NatTypeFullCone {
		_remoteIpAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	i.NatLock.RLock()
	natFlow, exist := i.NatWanFlowTable.Get(NatWanFlowHash{
		RemoteIpAddr:  _remoteIpAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(wanIpAddr),
		WanPort:       wanPort,
		Ipv4HeadProto: ipv4HeadProto,
	})
	i.NatLock.RUnlock()
	if !exist {
		return nil
	}
	return natFlow
}

func (i *NetIf) NatAddFlow(lanHostIpAddr []byte, remoteIpAddr []byte, lanHostPort uint16, remotePort uint16, ipv4HeadProto uint8) *NatFlow {
	if lanHostPort == 0 || remotePort == 0 {
		return nil
	}
	_remoteIpAddrU := uint32(0)
	_remotePort := uint16(0)
	if i.Config.NatType == NatTypeSymmetric {
		_remoteIpAddrU = protocol.IpAddrToU(remoteIpAddr)
		_remotePort = remotePort
	} else if i.Config.NatType == NatTypeFullCone {
		_remoteIpAddrU = 0
		_remotePort = 0
	}
	if ipv4HeadProto == protocol.IPH_PROTO_ICMP {
		_remotePort = 0
	}
	i.NatLock.Lock()
	defer i.NatLock.Unlock()
	// nat端口分配
	portAlloc, exist := i.NatPortAlloc.Get(IpAddrHash(_remoteIpAddrU))
	if !exist {
		portAlloc = mem.MallocType[PortAlloc](i.StaticHeap, 1)
		if portAlloc == nil {
			return nil
		}
		portAlloc.UsePortMap = hashmap.NewHashMap[PortHash, struct{}](i.StaticHeap)
		if portAlloc.UsePortMap == nil {
			mem.FreeType[PortAlloc](i.StaticHeap, portAlloc)
			return nil
		}
		ok := i.NatPortAlloc.Set(IpAddrHash(_remoteIpAddrU), portAlloc)
		if !ok {
			portAlloc.UsePortMap.Free()
			mem.FreeType[PortAlloc](i.StaticHeap, portAlloc)
			return nil
		}
	}
	wanPort := uint16(32768)
	for {
		_, use := portAlloc.UsePortMap.Get(PortHash(wanPort))
		if !use {
			break
		}
		wanPort++
		if wanPort == 0 {
			break
		}
	}
	if wanPort == 0 {
		return nil
	}
	ok := portAlloc.UsePortMap.Set(PortHash(wanPort), struct{}{})
	if !ok {
		return nil
	}
	natFlowHash := NatFlowHash{
		RemoteIpAddr:  _remoteIpAddrU,
		RemotePort:    _remotePort,
		LanHostIpAddr: protocol.IpAddrToU(lanHostIpAddr),
		LanHostPort:   lanHostPort,
		Ipv4HeadProto: ipv4HeadProto,
	}
	natFlow := mem.MallocType[NatFlow](i.StaticHeap, 1)
	if natFlow == nil {
		portAlloc.UsePortMap.Del(PortHash(wanPort))
		return nil
	}
	natFlow.NatFlowHash = natFlowHash
	natFlow.RemoteIpAddr = _remoteIpAddrU
	natFlow.RemotePort = _remotePort
	natFlow.WanIpAddr = protocol.IpAddrToU(i.IpAddr)
	natFlow.WanPort = wanPort
	natFlow.LanHostIpAddr = protocol.IpAddrToU(lanHostIpAddr)
	natFlow.LanHostPort = lanHostPort
	natFlow.Ipv4HeadProto = ipv4HeadProto
	natFlow.LastAliveTime = i.Engine.TimeNow
	ok = i.NatFlowTable.Set(natFlowHash, natFlow)
	if !ok {
		portAlloc.UsePortMap.Del(PortHash(wanPort))
		return nil
	}
	ok = i.NatWanFlowTable.Set(NatWanFlowHash{
		RemoteIpAddr:  _remoteIpAddrU,
		RemotePort:    _remotePort,
		WanIpAddr:     protocol.IpAddrToU(i.IpAddr),
		WanPort:       wanPort,
		Ipv4HeadProto: ipv4HeadProto,
	}, natFlow)
	if !ok {
		portAlloc.UsePortMap.Del(PortHash(wanPort))
		return nil
	}
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

func (i *NetIf) ListNat() []*NatFlow {
	i.NatLock.Lock()
	defer i.NatLock.Unlock()
	ret := make([]*NatFlow, 0)
	i.NatFlowTable.For(func(key NatFlowHash, value *NatFlow) (next bool) {
		v := *value
		ret = append(ret, &v)
		return true
	})
	return ret
}

func (i *NetIf) NatTableClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Engine.Stop.Load() {
			break
		}
		i.NatLock.Lock()
		i.NatFlowTable.For(func(natFlowHash NatFlowHash, natFlow *NatFlow) (next bool) {
			if i.Engine.TimeNow-natFlow.LastAliveTime > 60 {
				i.NatFlowTable.Del(natFlowHash)
				i.NatWanFlowTable.Del(NatWanFlowHash{
					RemoteIpAddr: natFlow.RemoteIpAddr,
					RemotePort:   natFlow.RemotePort,
					WanIpAddr:    natFlow.WanIpAddr,
					WanPort:      natFlow.WanPort,
				})
				mem.FreeType[NatFlow](i.StaticHeap, natFlow)
				portAlloc, exist := i.NatPortAlloc.Get(IpAddrHash(natFlow.RemoteIpAddr))
				if !exist {
					return true
				}
				portAlloc.UsePortMap.Del(PortHash(natFlow.WanPort))
				if portAlloc.UsePortMap.Len() == 0 {
					portAlloc.UsePortMap.Free()
					i.NatPortAlloc.Del(IpAddrHash(natFlow.RemoteIpAddr))
					mem.FreeType[PortAlloc](i.StaticHeap, portAlloc)
				}
			}
			return true
		})
		i.NatLock.Unlock()
	}
	i.Engine.StopWaitGroup.Done()
}

func (i *NetIf) SendUdpPktByFlow(natFlowHash NatFlowHash, dir int, udpPayload []byte) {
	natFlowHash.Ipv4HeadProto = protocol.IPH_PROTO_UDP
	remoteIpAddr := protocol.UToIpAddr(natFlowHash.RemoteIpAddr)
	lanHostIpAddr := protocol.UToIpAddr(natFlowHash.LanHostIpAddr)
	natFlow := i.NatGetFlowByHash(
		protocol.UToIpAddr(natFlowHash.RemoteIpAddr),
		natFlowHash.RemotePort,
		protocol.UToIpAddr(natFlowHash.LanHostIpAddr),
		natFlowHash.LanHostPort,
		natFlowHash.Ipv4HeadProto,
	)
	if natFlow == nil {
		natFlow = i.NatAddFlow(lanHostIpAddr, remoteIpAddr, natFlowHash.LanHostPort, natFlowHash.RemotePort, natFlowHash.Ipv4HeadProto)
		if natFlow == nil {
			return
		}
	}
	natFlow.LastAliveTime = i.Engine.TimeNow
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
		arpCache := i.GetArpCache(nextHopIpAddr)
		if arpCache == nil {
			return
		}
		i.SendEthernet(ipv4Pkt, arpCache.MacAddr[:], i.MacAddr, protocol.ETH_PROTO_IPV4)
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
		arpCache := outNetIf.GetArpCache(lanHostIpAddr)
		if arpCache == nil {
			return
		}
		outNetIf.SendEthernet(ipv4Pkt, arpCache.MacAddr[:], outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
	default:
	}
}
