package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxTcp(ipv4Payload []byte, ipv4SrcAddr []byte) {
	if i.CheckSumDisable {
		ipv4Payload[16] = 0x00
		ipv4Payload[17] = 0x00
	}
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

func (i *NetIf) TxTcp(tcpPayload []byte, tcpSrcPort uint16, tcpDstPort uint16, ipv4DstAddr []byte, seqNum uint32, ackNum uint32, flags uint8) bool {
	tcpPkt := make([]byte, 0, 1480)
	tcpPkt, err := protocol.BuildTcpPkt(tcpPkt, tcpPayload, tcpSrcPort, tcpDstPort, i.IpAddr, ipv4DstAddr, seqNum, ackNum, flags)
	if err != nil {
		Log(fmt.Sprintf("build tcp packet error: %v\n", err))
		return false
	}
	return i.TxIpv4(tcpPkt, protocol.IPH_PROTO_TCP, ipv4DstAddr)
}
