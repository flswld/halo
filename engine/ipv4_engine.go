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
	if !bytes.Equal(ipv4DstAddr, i.IpAddr) {
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
	// ip路由
	var ethDstMac []byte = nil
	localIpUint32 := protocol.ConvIpAddrToUint32(i.IpAddr)
	dstIpUint32 := protocol.ConvIpAddrToUint32(ipv4DstAddr)
	networkMaskUint32 := protocol.ConvIpAddrToUint32(i.NetworkMask)
	if localIpUint32 == dstIpUint32 {
		// 本地回环
		ethFrm, err := protocol.BuildEthFrm(ipv4Pkt, i.MacAddr, i.MacAddr, protocol.ETH_PROTO_IPV4)
		if err != nil {
			fmt.Printf("build ethernet frame error: %v\n", err)
			return nil
		}
		i.EthRxChan <- ethFrm
		return nil
	} else if localIpUint32&networkMaskUint32 == dstIpUint32&networkMaskUint32 {
		// 同一子网
		ethDstMac = i.GetArpCache(ipv4DstAddr)
		if ethDstMac == nil {
			return nil
		}
	} else {
		// 不同子网
		ethDstMac = i.GetArpCache(i.GatewayIpAddr)
		if ethDstMac == nil {
			return nil
		}
	}
	ethFrm, err := protocol.BuildEthFrm(ipv4Pkt, ethDstMac, i.MacAddr, protocol.ETH_PROTO_IPV4)
	if err != nil {
		fmt.Printf("build ethernet frame error: %v\n", err)
		return nil
	}
	if i.Engine.DebugLog {
		fmt.Printf("tx ip pkt, eth frm len: %v, eth frm data: %v\n", len(ethFrm), ethFrm)
	}
	i.EthTxChan <- ethFrm
	return ethFrm
}
