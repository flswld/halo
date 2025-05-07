package engine

import (
	"bytes"
	"fmt"
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
		// 暂不处理ipv4广播包
		return
	}
	if !bytes.Equal(ipv4DstAddr, i.IpAddr) || i.NatEnable {
		ok := i.Ipv4RouteForward(ethPayload, ipv4SrcAddr, ipv4DstAddr, ipv4HeadProto)
		if ok {
			return
		}
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

func (i *NetIf) TxIpv4(ipv4Payload []byte, ipv4HeadProto uint8, ipv4DstAddr []byte) []byte {
	ipv4Pkt, err := protocol.BuildIpv4Pkt(ipv4Payload, ipv4HeadProto, i.IpAddr, ipv4DstAddr)
	if err != nil {
		Log(fmt.Sprintf("build ip packet error: %v\n", err))
		return nil
	}
	// 三层路由
	nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
	if nextHopIpAddr == nil && outNetIfName == "" {
		Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
		return nil
	}
	outNetIf := i.Engine.NetIfMap[outNetIfName]
	dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
	outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
	if dstIpAddrU == outNetIfIpAddrU {
		// 本地回环
		outNetIf.LoChan <- ipv4Pkt
		return ipv4Pkt
	}
	// 二层封装
	var ethDstMac []byte = nil
	if nextHopIpAddr != nil {
		ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
	}
	if ethDstMac == nil {
		return nil
	}
	return outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
}

func (i *NetIf) Ipv4RouteForward(ethPayload []byte, ipv4SrcAddr []byte, ipv4DstAddr []byte, ipv4HeadProto uint8) bool {
	// DNAT 公网地址 -> 私网地址
	if i.NatEnable {
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
		if i.NatEnable {
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
	if dstIpAddrU == outNetIfIpAddrU && !i.NatEnable {
		// 本地回环
		outNetIf.LoChan <- ethPayload
		return true
	}
	// SNAT 私网地址 -> 公网地址
	if outNetIf.NatEnable {
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
			ethPayload = protocol.NatChangeSrc(ethPayload, protocol.UToIpAddr(natPortMappingEntry.WanIpAddr), natPortMappingEntry.WanPort)
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
