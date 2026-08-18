package engine

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"sync"
	"time"

	"github.com/flswld/halo/hashcode"
	"github.com/flswld/halo/hashmap"
	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

// RxIpv4 接收并分发或转发 IPv4 报文
func (i *NetIf) RxIpv4(ethPayload []byte) {
	ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse ip packet error: %v\n", err))
		return
	}
	if ipv4DstAddr[3] == 255 {
		// 广播 UDP 仅进入 DHCP 分发路径 不参与普通路由转发
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

// TxIpv4 构建 IPv4 报文并按路由结果发送
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
		// 路由结果中的下一跳为空表示目标与出接口直连
		_nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
		if _nextHopIpAddr == nil && outNetIfName == "" {
			Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
			return false
		}
		nextHopIpAddr = _nextHopIpAddr
		outNetIf = i.Router.NetIfMap[outNetIfName]
		dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
		outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
		if dstIpAddrU == outNetIfIpAddrU {
			// 本地回环
			// 复制报文后写入回环管道 避免后续复用发送缓冲区覆盖数据
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
		return outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	} else if arpCache != nil {
		return outNetIf.TxEthernet(ipv4Pkt, arpCache.MacAddr[:], protocol.ETH_PROTO_IPV4)
	} else {
		return false
	}
}

// 流方向
const (
	LanToWan = iota
	WanToLan
)

// Ipv4RouteForward 对 IPv4 报文执行路由转发与网络地址转换
func (i *NetIf) Ipv4RouteForward(ethPayload []byte, ipv4SrcAddr []byte, ipv4DstAddr []byte, ipv4HeadProto uint8) bool {
	var inNatPortMappingEntry *NatPortMappingEntry
	// DNAT 公网地址 -> 私网地址
	if i.Config.NatEnable {
		// 入站 NAT 必须先恢复真实目的地址 后续路由才能选择 LAN 出接口
		srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
		isIcmpTtl := false
		ethPayload, isIcmpTtl = i.IcmpTtlDeepNat(ethPayload)
		if !isIcmpTtl {
			inNatPortMappingEntry = i.CheckNatPortMapping(WanToLan, i.IpAddr, dstPort, ipv4HeadProto)
			if inNatPortMappingEntry == nil {
				natFlow := i.NatGetFlowByWan(ipv4SrcAddr, srcPort, ipv4DstAddr, dstPort, ipv4HeadProto)
				if natFlow == nil {
					// 没有nat表项
					// 返回未处理状态 允许目标为路由器本机的 ICMP 报文继续交给本地协议栈
					return false
				}
				natFlow.LastAliveTime = i.Router.TimeNow
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
			} else {
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(inNatPortMappingEntry.LanHostIpAddr), inNatPortMappingEntry.LanHostPort)
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
	if i.Router.Ipv4PktFwdHook != nil {
		// 钩子看到的是已经完成入站 DNAT 和 TTL 更新的报文
		dir := 0
		if i.Config.NatEnable {
			dir = WanToLan
		} else {
			dir = LanToWan
		}
		drop, mod := i.Router.Ipv4PktFwdHook(ethPayload, dir)
		if drop {
			// 外部钩子回调强制丢弃
			return true
		}
		ethPayload = mod
	}
	// 三层路由
	var outNatPortMappingFlow *NatPortMappingFlow
	// 非NAT接口查询端口映射回程流
	if !i.Config.NatEnable {
		// LAN 服务器回包使用反向五元组命中最初记录的 WAN 入口
		srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
		natFlowHash := NatFlowHash{
			RemoteIpAddr:  protocol.IpAddrToU(ipv4DstAddr),
			RemotePort:    dstPort,
			LanHostIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
			LanHostPort:   srcPort,
			Ipv4HeadProto: ipv4HeadProto,
		}
		i.Router.NatPortMappingFlowLock.Lock()
		var exist bool
		outNatPortMappingFlow, exist = i.Router.NatPortMappingFlowTable.Get(natFlowHash)
		if exist {
			outNatPortMappingFlow.LastAliveTime = i.Router.TimeNow
		}
		i.Router.NatPortMappingFlowLock.Unlock()
	}
	nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
	// 回程包改用原入口WAN接口
	if outNatPortMappingFlow != nil && outNetIfName != outNatPortMappingFlow.WanNetIf {
		// 双 WAN 场景强制源进源出 路由不指向原 WAN 时改用该接口当前网关
		outNetIfName = outNatPortMappingFlow.WanNetIf
		nextHopIpAddr = i.Router.NetIfMap[outNetIfName].Gateway
	}
	if nextHopIpAddr == nil && outNetIfName == "" {
		// 没有路由
		Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
		return true
	}
	outNetIf := i.Router.NetIfMap[outNetIfName]
	dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
	outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
	if dstIpAddrU == outNetIfIpAddrU && !i.Config.NatEnable {
		// 本地回环
		_ethPayload := make([]byte, len(ethPayload))
		copy(_ethPayload, ethPayload)
		outNetIf.LoChan <- _ethPayload
		return true
	}
	// SNAT 私网地址 -> 公网地址
	if outNetIf.Config.NatEnable {
		// 端口映射回程恢复公网源地址和端口
		if outNatPortMappingFlow != nil {
			ethPayload = protocol.NatChangeSrc(ethPayload, outNetIf.IpAddr, outNatPortMappingFlow.WanPort)
		} else {
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			outNatPortMappingEntry := outNetIf.CheckNatPortMapping(LanToWan, ipv4SrcAddr, srcPort, ipv4HeadProto)
			if outNatPortMappingEntry == nil {
				natFlow := outNetIf.NatGetFlowByHash(ipv4DstAddr, dstPort, ipv4SrcAddr, srcPort, ipv4HeadProto)
				if natFlow == nil {
					natFlow = outNetIf.NatAddFlow(ipv4SrcAddr, ipv4DstAddr, srcPort, dstPort, ipv4HeadProto)
					if natFlow == nil {
						// nat端口分配失败
						return true
					}
				}
				natFlow.LastAliveTime = i.Router.TimeNow
				ethPayload = protocol.NatChangeSrc(ethPayload, protocol.UToIpAddr(natFlow.WanIpAddr), natFlow.WanPort)
			} else {
				ethPayload = protocol.NatChangeSrc(ethPayload, outNetIf.IpAddr, outNatPortMappingEntry.WanPort)
			}
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
	// 端口映射首包转发前尽量记录回程NAT流
	if inNatPortMappingEntry != nil {
		// 仅在路由和 ARP 均成功后建表 避免为未实际转发的首包留下回程状态
		remotePort, _ := protocol.NatGetSrcDstPort(ethPayload)
		natFlowHash := NatFlowHash{
			RemoteIpAddr:  protocol.IpAddrToU(ipv4SrcAddr),
			RemotePort:    remotePort,
			LanHostIpAddr: inNatPortMappingEntry.LanHostIpAddr,
			LanHostPort:   inNatPortMappingEntry.LanHostPort,
			Ipv4HeadProto: ipv4HeadProto,
		}
		i.Router.NatPortMappingFlowLock.Lock()
		inNatPortMappingFlow, exist := i.Router.NatPortMappingFlowTable.Get(natFlowHash)
		if !exist {
			inNatPortMappingFlow = mem.MallocType[NatPortMappingFlow](i.Router.StaticAllocator, 1)
			if inNatPortMappingFlow == nil {
				i.Router.NatPortMappingFlowLock.Unlock()
				return true
			}
			if !i.Router.NatPortMappingFlowTable.Set(natFlowHash, inNatPortMappingFlow) {
				mem.FreeType[NatPortMappingFlow](i.Router.StaticAllocator, inNatPortMappingFlow)
				i.Router.NatPortMappingFlowLock.Unlock()
				return true
			}
		}
		inNatPortMappingFlow.WanNetIf = i.Config.Name
		inNatPortMappingFlow.WanPort = inNatPortMappingEntry.WanPort
		inNatPortMappingFlow.LastAliveTime = i.Router.TimeNow
		i.Router.NatPortMappingFlowLock.Unlock()
	}
	outNetIf.TxEthernet(ethPayload, arpCache.MacAddr[:], protocol.ETH_PROTO_IPV4)
	return true
}

// RouteTable 路由表
type RouteTable struct {
	Root   *TrieNode    // 根节点
	Lock   sync.RWMutex // 路由表读写锁
	IpHash hash.Hash32  // IP 地址哈希计算器
}

// TrieNode 路由树节点
type TrieNode struct {
	RouteList []*RouteEntry // 路由信息
	Left      *TrieNode     // 零位子节点
	Right     *TrieNode     // 一位子节点
}

// RouteEntry 路由条目
type RouteEntry struct {
	DstIpAddr   []byte // 目的 IP 地址
	NetworkMask []byte // 网络掩码
	NextHop     []byte // 下一跳地址
	NetIf       string // 出接口名称
}

// AddRoute 向路由表添加路由条目
func (r *RouteTable) AddRoute(route *RouteEntry) {
	r.UpdateRoute(route, route)
}

// DeleteRoute 从路由表删除路由条目
func (r *RouteTable) DeleteRoute(route *RouteEntry) {
	r.UpdateRoute(route, nil)
}

// UpdateRoute 使用新路由替换指定的旧路由
func (r *RouteTable) UpdateRoute(oldRoute *RouteEntry, newRoute *RouteEntry) {
	r.Lock.Lock()
	defer r.Lock.Unlock()
	node := r.Root
	maskSize := 0
	networkMaskU := protocol.IpAddrToU(oldRoute.NetworkMask)
	// 掩码中的连续高位一决定前缀树的下探深度
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
	// 同一前缀节点允许保存多条等价路由 删除时按完整路由字段匹配
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

// FindRoute 按最长前缀和流哈希查找路由条目
func (r *RouteTable) FindRoute(ip []byte) *RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	node := r.Root
	var lastMatch []*RouteEntry
	// 沿目标地址位序下探并持续保存最近的非空路由集合
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
	r.IpHash.Reset()
	_, _ = r.IpHash.Write(ip)
	// 相同目的地址稳定落到同一条等价路由
	return lastMatch[r.IpHash.Sum32()%uint32(len(lastMatch))]
}

// ListRoute 返回路由表中的全部路由条目
func (r *RouteTable) ListRoute() []*RouteEntry {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	return r.foreachNode(r.Root)
}

// foreachNode 递归收集指定节点下的路由条目
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

// FindRoute 查找目标 IPv4 地址的下一跳和出接口
func (i *NetIf) FindRoute(ipv4DstAddr []byte) ([]byte, string) {
	route := i.Router.RouteTable.FindRoute(ipv4DstAddr)
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

// NatFlow 保存一条 NAT 流记录
type NatFlow struct {
	NatFlowHash   NatFlowHash // NAT 流唯一标识
	RemoteIpAddr  uint32      // 远程 IP 地址
	RemotePort    uint16      // 远程端口
	WanIpAddr     uint32      // WAN 口 IP 地址
	WanPort       uint16      // WAN 口端口
	LanHostIpAddr uint32      // LAN 侧主机 IP 地址
	LanHostPort   uint16      // LAN 侧主机端口
	Ipv4HeadProto uint8       // IPv4 上层协议
	LastAliveTime uint32      // 上一次活跃时间
}

// NatFlowHash 表示 LAN 侧 NAT 流摘要
type NatFlowHash struct {
	RemoteIpAddr  uint32 // 远程 IP 地址
	RemotePort    uint16 // 远程端口
	LanHostIpAddr uint32 // LAN 侧主机 IP 地址
	LanHostPort   uint16 // LAN 侧主机端口
	Ipv4HeadProto uint8  // IPv4 上层协议
}

// GetHashCode 计算 NAT 流摘要的哈希值
func (h NatFlowHash) GetHashCode() uint64 {
	data := make([]byte, 13)
	binary.LittleEndian.PutUint32(data[0:4], h.RemoteIpAddr)
	binary.LittleEndian.PutUint16(data[4:6], h.RemotePort)
	binary.LittleEndian.PutUint32(data[6:10], h.LanHostIpAddr)
	binary.LittleEndian.PutUint16(data[10:12], h.LanHostPort)
	data[12] = h.Ipv4HeadProto
	return hashcode.GetHashCodeXXH3(data)
}

// NatWanFlowHash 表示 WAN 口 NAT 流摘要
type NatWanFlowHash struct {
	RemoteIpAddr  uint32 // 远程 IP 地址
	RemotePort    uint16 // 远程端口
	WanIpAddr     uint32 // WAN 口 IP 地址
	WanPort       uint16 // WAN 口端口
	Ipv4HeadProto uint8  // IPv4 上层协议
}

// GetHashCode 计算 WAN 口 NAT 流摘要的哈希值
func (h NatWanFlowHash) GetHashCode() uint64 {
	data := make([]byte, 13)
	binary.LittleEndian.PutUint32(data[0:4], h.RemoteIpAddr)
	binary.LittleEndian.PutUint16(data[4:6], h.RemotePort)
	binary.LittleEndian.PutUint32(data[6:10], h.WanIpAddr)
	binary.LittleEndian.PutUint16(data[10:12], h.WanPort)
	data[12] = h.Ipv4HeadProto
	return hashcode.GetHashCodeXXH3(data)
}

// NatPortMappingEntry 保存一条静态 NAT 端口映射
type NatPortMappingEntry struct {
	WanPort       uint16 // WAN 口端口
	LanHostIpAddr uint32 // LAN 侧主机 IP 地址
	LanHostPort   uint16 // LAN 侧主机端口
	Ipv4HeadProto uint8  // IPv4 上层协议
}

// NatPortMappingFlow 记录静态 DNAT 连接的原始 WAN 口和映射端口
// 回包命中后优先从原 WAN 口返回 二层地址由 ARP 模块解析
type NatPortMappingFlow struct {
	WanNetIf      string // WAN 接口名
	WanPort       uint16 // WAN 接口端口
	LastAliveTime uint32 // 上一次活跃时间
}

// NatPortMappingFlowClear 定期清理空闲的端口映射回程 NAT 流
func (r *Router) NatPortMappingFlowClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if r.Stop.Load() {
			break
		}
		r.NatPortMappingFlowLock.Lock()
		r.NatPortMappingFlowTable.For(func(natFlowHash NatFlowHash, natPortMappingFlow *NatPortMappingFlow) (next bool) {
			if r.TimeNow-natPortMappingFlow.LastAliveTime > 60 {
				r.NatPortMappingFlowTable.Del(natFlowHash)
				mem.FreeType[NatPortMappingFlow](r.StaticAllocator, natPortMappingFlow)
			}
			return true
		})
		r.NatPortMappingFlowLock.Unlock()
	}
	r.StopWaitGroup.Done()
}

// PortAlloc 保存 NAT 已分配端口集合
type PortAlloc struct {
	UsePortMap *hashmap.HashMap[PortHash, struct{}] // 已使用端口集合
}

// NatGetFlowByHash 按 LAN 侧五元组查询 NAT 流
func (i *NetIf) NatGetFlowByHash(remoteIpAddr []byte, remotePort uint16, lanHostIpAddr []byte, lanHostPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteIpAddrU := uint32(0)
	_remotePort := uint16(0)
	// 对称型 NAT 将远端地址端口纳入键 完全圆锥型 NAT 则忽略远端
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

// NatGetFlowByWan 按 WAN 侧五元组查询 NAT 流
func (i *NetIf) NatGetFlowByWan(remoteIpAddr []byte, remotePort uint16, wanIpAddr []byte, wanPort uint16, ipv4HeadProto uint8) *NatFlow {
	_remoteIpAddrU := uint32(0)
	_remotePort := uint16(0)
	// 查询键必须与建表时采用相同的 NAT 类型归一化规则
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

// NatAddFlow 创建 NAT 流并分配 WAN 口端口
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
	// 每个归一化远端地址维护独立端口集合 降低不同远端之间的端口竞争
	portAlloc, exist := i.NatPortAlloc.Get(IpAddrHash(_remoteIpAddrU))
	if !exist {
		portAlloc = mem.MallocType[PortAlloc](i.Router.StaticAllocator, 1)
		if portAlloc == nil {
			return nil
		}
		portAlloc.UsePortMap = hashmap.NewHashMap[PortHash, struct{}](i.Router.StaticAllocator)
		if portAlloc.UsePortMap == nil {
			mem.FreeType[PortAlloc](i.Router.StaticAllocator, portAlloc)
			return nil
		}
		ok := i.NatPortAlloc.Set(IpAddrHash(_remoteIpAddrU), portAlloc)
		if !ok {
			portAlloc.UsePortMap.Free()
			mem.FreeType[PortAlloc](i.Router.StaticAllocator, portAlloc)
			return nil
		}
	}
	wanPort := uint16(32768)
	// 从动态端口区起点顺序寻找空闲端口 溢出到零表示耗尽
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
	natFlow := mem.MallocType[NatFlow](i.Router.StaticAllocator, 1)
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
	natFlow.LastAliveTime = i.Router.TimeNow
	// LAN 键和 WAN 键共同指向同一流对象 便于双向报文查找和统一释放
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

// CheckNatPortMapping 按方向和端口查找静态 NAT 端口映射
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

// ListNat 返回当前 NAT 流表的副本
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

// NatTableClear 定期清理空闲 NAT 流并释放对应端口
func (i *NetIf) NatTableClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Router.Stop.Load() {
			break
		}
		i.NatLock.Lock()
		i.NatFlowTable.For(func(natFlowHash NatFlowHash, natFlow *NatFlow) (next bool) {
			if i.Router.TimeNow-natFlow.LastAliveTime > 60 {
				// 删除双向索引后归还端口 最后释放流对象和空端口分配器
				i.NatFlowTable.Del(natFlowHash)
				i.NatWanFlowTable.Del(NatWanFlowHash{
					RemoteIpAddr:  natFlow.RemoteIpAddr,
					RemotePort:    natFlow.RemotePort,
					WanIpAddr:     natFlow.WanIpAddr,
					WanPort:       natFlow.WanPort,
					Ipv4HeadProto: natFlow.Ipv4HeadProto,
				})
				mem.FreeType[NatFlow](i.Router.StaticAllocator, natFlow)
				portAlloc, exist := i.NatPortAlloc.Get(IpAddrHash(natFlow.RemoteIpAddr))
				if !exist {
					return true
				}
				portAlloc.UsePortMap.Del(PortHash(natFlow.WanPort))
				if portAlloc.UsePortMap.Len() == 0 {
					portAlloc.UsePortMap.Free()
					i.NatPortAlloc.Del(IpAddrHash(natFlow.RemoteIpAddr))
					mem.FreeType[PortAlloc](i.Router.StaticAllocator, portAlloc)
				}
			}
			return true
		})
		i.NatLock.Unlock()
	}
	i.Router.StopWaitGroup.Done()
}

// SendUdpPktByFlow 根据 NAT 流信息按指定方向发送 UDP 报文
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
	natFlow.LastAliveTime = i.Router.TimeNow
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
		i.TxEthernet(ipv4Pkt, arpCache.MacAddr[:], protocol.ETH_PROTO_IPV4)
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
		outNetIf := i.Router.NetIfMap[outNetIfName]
		arpCache := outNetIf.GetArpCache(lanHostIpAddr)
		if arpCache == nil {
			return
		}
		outNetIf.TxEthernet(ipv4Pkt, arpCache.MacAddr[:], protocol.ETH_PROTO_IPV4)
	default:
	}
}
