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
			// 公网地址 -> 私网地址
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			i.NatTableLock.RLock()
			inNatEntry, exist := i.NatTable[OutNatEntry{
				DstIpAddr: protocol.IpAddrToU(ipv4SrcAddr),
				DstPort:   srcPort,
				SrcIpAddr: protocol.IpAddrToU(ipv4DstAddr),
				SrcPort:   dstPort,
			}]
			i.NatTableLock.RUnlock()
			if !exist {
				return
			}
			ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(inNatEntry.IpAddr), inNatEntry.Port)
		}
		dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
		var nextHopIpAddr []byte = nil
		outNetIfName := ""
		for _, routingEntry := range i.Engine.RoutingTable {
			// TODO 最长前缀匹配
			routeDstIpAddrU := protocol.IpAddrToU(routingEntry.DstIpAddr)
			routeNetworkMaskU := protocol.IpAddrToU(routingEntry.NetworkMask)
			if dstIpAddrU&routeNetworkMaskU != routeDstIpAddrU {
				continue
			}
			nextHopIpAddr = routingEntry.NextHop
			outNetIfName = routingEntry.NetIf
			break
		}
		if nextHopIpAddr == nil && outNetIfName == "" {
			fmt.Printf("no route found for: %v\n", ipv4DstAddr)
			return
		}
		outNetIf := i.Engine.NetIfMap[outNetIfName]
		outNetIfIpAddrU := protocol.IpAddrToU(outNetIf.IpAddr)
		if dstIpAddrU == outNetIfIpAddrU && !i.NatEnable {
			// 本地回环
			outNetIf.LoChan <- ethPayload
			return
		}
		var ethDstMac []byte = nil
		if nextHopIpAddr != nil {
			ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
		} else {
			ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
		}
		alive := false
		ethPayload, alive = protocol.HandleIpv4PktTtl(ethPayload)
		if !alive {
			return
		}
		if outNetIf.NatEnable {
			// 私网地址 -> 公网地址
			srcPort, dstPort := protocol.NatGetSrcDstPort(ethPayload)
			newSrcAddr := outNetIf.IpAddr
			// TODO 支持多个私网地址访问同一个公网地址
			newSrcPort := srcPort
			outNetIf.NatTableLock.Lock()
			outNetIf.NatTable[OutNatEntry{
				DstIpAddr: protocol.IpAddrToU(ipv4DstAddr),
				DstPort:   dstPort,
				SrcIpAddr: protocol.IpAddrToU(newSrcAddr),
				SrcPort:   newSrcPort,
			}] = &InNatEntry{
				IpAddr:        protocol.IpAddrToU(ipv4SrcAddr),
				Port:          srcPort,
				LastAliveTime: uint32(time.Now().Unix()),
			}
			outNetIf.NatTableLock.Unlock()
			ethPayload = protocol.NatChangeSrc(ethPayload, newSrcAddr, newSrcPort)
		}
		if outNetIf.Engine.Ipv4PktFwdHook != nil {
			ethPayload = outNetIf.Engine.Ipv4PktFwdHook(ethPayload)
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
	dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
	var nextHopIpAddr []byte = nil
	outNetIfName := ""
	for _, routingEntry := range i.Engine.RoutingTable {
		// TODO 最长前缀匹配
		routeDstIpAddrU := protocol.IpAddrToU(routingEntry.DstIpAddr)
		routeNetworkMaskU := protocol.IpAddrToU(routingEntry.NetworkMask)
		if dstIpAddrU&routeNetworkMaskU != routeDstIpAddrU {
			continue
		}
		nextHopIpAddr = routingEntry.NextHop
		outNetIfName = routingEntry.NetIf
		break
	}
	if nextHopIpAddr == nil && outNetIfName == "" {
		fmt.Printf("no route found for: %v\n", ipv4DstAddr)
		return nil
	}
	outNetIf := i.Engine.NetIfMap[outNetIfName]
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
	return outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
}
