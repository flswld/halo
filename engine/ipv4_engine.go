package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxIpv4(ethPayload []byte) {
	ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ethPayload)
	if err != nil {
		fmt.Printf("parse ip packet error: %v\n", err)
		return
	}
	if ipv4DstAddr[3] == 255 {
		// 暂不处理ipv4广播包
		return
	}
	if !bytes.Equal(ipv4DstAddr, i.IpAddr) || i.NatEnable {
		// 三层路由
		if i.NatEnable {
			// DNAT 公网地址 -> 私网地址
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			natFlow := i.NatGetFlowByWan(ipv4SrcAddr, srcPort, ipv4DstAddr, dstPort)
			if natFlow == nil {
				return
			}
			ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
		}
		nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
		if nextHopIpAddr == nil && outNetIfName == "" {
			fmt.Printf("no route found for: %v\n", ipv4DstAddr)
			return
		}
		outNetIf := i.Engine.NetIfMap[outNetIfName]
		dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
		outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
		if dstIpAddrU == outNetIfIpAddrU && !i.NatEnable {
			// 本地回环
			outNetIf.LoChan <- ethPayload
			return
		}
		alive := false
		ethPayload, alive = protocol.HandleIpv4PktTtl(ethPayload)
		if !alive {
			return
		}
		if outNetIf.Engine.Ipv4PktFwdHook != nil {
			drop, mod := outNetIf.Engine.Ipv4PktFwdHook(ethPayload)
			if drop {
				return
			}
			ethPayload = mod
		}
		if outNetIf.NatEnable {
			// SNAT 私网地址 -> 公网地址
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			natFlow := outNetIf.NatAddFlow(ipv4SrcAddr, ipv4DstAddr, srcPort, dstPort)
			if natFlow == nil {
				return
			}
			ethPayload = protocol.NatChangeSrc(ethPayload, protocol.UToIpAddr(natFlow.WanIpAddr), natFlow.WanPort)
		}
		var ethDstMac []byte = nil
		if nextHopIpAddr != nil {
			ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
		} else {
			ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
		}
		if ethDstMac == nil {
			return
		}
		outNetIf.TxEthernet(ethPayload, ethDstMac, protocol.ETH_PROTO_IPV4)
		return
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

func (i *NetIf) TxIpv4(ipv4Payload []byte, ipv4HeadProto uint8, ipv4DstAddr []byte) []byte {
	ipv4Pkt, err := protocol.BuildIpv4Pkt(ipv4Payload, ipv4HeadProto, i.IpAddr, ipv4DstAddr)
	if err != nil {
		fmt.Printf("build ip packet error: %v\n", err)
		return nil
	}
	// 三层路由
	nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
	if nextHopIpAddr == nil && outNetIfName == "" {
		fmt.Printf("no route found for: %v\n", ipv4DstAddr)
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
