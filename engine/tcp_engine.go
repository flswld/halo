package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxTcp(ipv4Payload []byte, ipv4SrcAddr []byte) {
	tcpPayload, tcpSrcPort, tcpDstPort, seqNum, ackNum, flags, err := protocol.ParseTcpPkt(ipv4Payload, ipv4SrcAddr, i.IpAddr)
	if err != nil {
		Log(fmt.Sprintf("parse tcp packet error: %v\n", err))
		return
	}
	if i.HandleTcp != nil {
		i.HandleTcp(tcpPayload, tcpSrcPort, tcpDstPort, seqNum, ackNum, flags, ipv4SrcAddr)
		return
	}
}

func (i *NetIf) TxTcp(tcpPayload []byte, tcpSrcPort uint16, tcpDstPort uint16, ipv4DstAddr []byte, seqNum uint32, ackNum uint32, flags uint8) []byte {
	tcpPkt, err := protocol.BuildTcpPkt(tcpPayload, tcpSrcPort, tcpDstPort, i.IpAddr, ipv4DstAddr, seqNum, ackNum, flags)
	if err != nil {
		Log(fmt.Sprintf("build tcp packet error: %v\n", err))
		return nil
	}
	return i.TxIpv4(tcpPkt, protocol.IPH_PROTO_TCP, ipv4DstAddr)
}
