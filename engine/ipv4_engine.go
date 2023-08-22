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
	if ipv4DstAddr[3] != 255 && !bytes.Equal(ipv4DstAddr, i.IpAddr) {
		// 三层路由
		dstIpAddrU := protocol.IpAddrToU(ipv4DstAddr)
		var nextHopIpAddr []byte = nil
		outNetIfName := ""
		for _, routingEntry := range i.Engine.RoutingTable {
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
		if dstIpAddrU == outNetIfIpAddrU {
			// 本地回环
			ethFrm, err := protocol.BuildEthFrm(ethPayload, outNetIf.MacAddr, outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
			if err != nil {
				fmt.Printf("build ethernet frame error: %v\n", err)
				return
			}
			outNetIf.EthRxChan <- ethFrm
			return
		}
		var ethDstMac []byte = nil
		if nextHopIpAddr != nil {
			ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
		} else {
			ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
		}
		ipv4Pkt, alive := protocol.HandleIpv4PktTtl(ethPayload)
		if !alive {
			return
		}
		if outNetIf.Engine.Ipv4PktFwdHook != nil {
			ipv4Pkt = outNetIf.Engine.Ipv4PktFwdHook(ipv4Pkt)
		}
		outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
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
		ethFrm, err := protocol.BuildEthFrm(ipv4Pkt, outNetIf.MacAddr, outNetIf.MacAddr, protocol.ETH_PROTO_IPV4)
		if err != nil {
			fmt.Printf("build ethernet frame error: %v\n", err)
			return nil
		}
		outNetIf.EthRxChan <- ethFrm
		return ethFrm
	}
	var ethDstMac []byte = nil
	if nextHopIpAddr != nil {
		ethDstMac = outNetIf.GetArpCache(nextHopIpAddr)
	} else {
		ethDstMac = outNetIf.GetArpCache(ipv4DstAddr)
	}
	return outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
}
