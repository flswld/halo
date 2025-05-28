package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxUdp(ipv4Payload []byte, ipv4SrcAddr []byte) {
	if i.CheckSumDisable {
		ipv4Payload[6] = 0x00
		ipv4Payload[7] = 0x00
	}
	udpPayload, udpSrcPort, udpDstPort, err := protocol.ParseUdpPkt(ipv4Payload, ipv4SrcAddr, i.IpAddr)
	if err != nil {
		Log(fmt.Sprintf("parse udp packet error: %v\n", err))
		return
	}
	if i.HandleUdp != nil {
		i.HandleUdp(udpPayload, udpSrcPort, udpDstPort, ipv4SrcAddr)
		return
	}
	if udpDstPort == DhcpClientPort || udpDstPort == DhcpServerPort {
		i.RxDhcp(udpPayload, udpSrcPort, udpDstPort, ipv4SrcAddr)
	}
}

func (i *NetIf) TxUdp(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4DstAddr []byte) bool {
	udpPkt := make([]byte, 0, 1480)
	udpPkt, err := protocol.BuildUdpPkt(udpPkt, udpPayload, udpSrcPort, udpDstPort, i.IpAddr, ipv4DstAddr)
	if err != nil {
		Log(fmt.Sprintf("build udp packet error: %v\n", err))
		return false
	}
	return i.TxIpv4(udpPkt, protocol.IPH_PROTO_UDP, ipv4DstAddr)
}
