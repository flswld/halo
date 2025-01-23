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
		// DNAT 公网地址 -> 私网地址
		if i.NatEnable {
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			var natPortMappingEntry *NatPortMappingEntry = nil
			for _, entry := range i.NatPortMappingTable {
				if entry.WanIpAddr == protocol.IpAddrToU(i.IpAddr) && entry.WanPort == dstPort {
					natPortMappingEntry = entry
					break
				}
			}
			if natPortMappingEntry == nil {
				natFlow := i.NatGetFlowByWan(ipv4SrcAddr, srcPort, ipv4DstAddr, dstPort)
				if natFlow == nil {
					return
				}
				natFlow.LastAliveTime = uint32(time.Now().Unix())
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
			} else {
				ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natPortMappingEntry.LanHostIpAddr), natPortMappingEntry.LanHostPort)
			}
		}
		// 三层路由
		nextHopIpAddr, outNetIfName := i.FindRoute(ipv4DstAddr)
		if nextHopIpAddr == nil && outNetIfName == "" {
			Log(fmt.Sprintf("no route found for: %v\n", ipv4DstAddr))
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
			icmpPkt, err := protocol.BuildIcmpPkt(ethPayload, protocol.ICMP_TTL, []byte{0x00, 0x00}, []byte{0x00, 0x00})
			if err != nil {
				Log(fmt.Sprintf("build icmp packet error: %v\n", err))
				return
			}
			i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4SrcAddr)
			return
		}
		// 外部钩子回调
		if outNetIf.Engine.Ipv4PktFwdHook != nil {
			dir := 0
			if i.NatEnable {
				dir = WanToLan
			} else {
				dir = LanToWan
			}
			drop, mod := outNetIf.Engine.Ipv4PktFwdHook(ethPayload, dir)
			if drop {
				return
			}
			ethPayload = mod
		}
		// SNAT 私网地址 -> 公网地址
		if outNetIf.NatEnable {
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			var natPortMappingEntry *NatPortMappingEntry = nil
			for _, entry := range outNetIf.NatPortMappingTable {
				if entry.LanHostIpAddr == protocol.IpAddrToU(ipv4SrcAddr) && entry.LanHostPort == srcPort {
					natPortMappingEntry = entry
					break
				}
			}
			if natPortMappingEntry == nil {
				natFlow := outNetIf.NatGetFlowByHash(NatFlowHash{
					RemoteIpAddr:  protocol.IpAddrToU(ipv4DstAddr),
					RemotePort:    dstPort,
					LanHostIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
					LanHostPort:   srcPort,
				})
				if natFlow == nil {
					natFlow = outNetIf.NatAddFlow(ipv4SrcAddr, ipv4DstAddr, srcPort, dstPort)
					if natFlow == nil {
						return
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
