package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxTcp() {
}

func (i *NetIf) TxTcpSyn(tcpSrcPort uint16, tcpDstPort uint16, ipv4DstAddr []byte) []byte {
	tcpSynPkt, err := protocol.BuildTcpSynPkt(tcpSrcPort, tcpDstPort, i.IpAddr, ipv4DstAddr, 0)
	if err != nil {
		fmt.Printf("build tcp syn packet error: %v\n", err)
		return nil
	}
	return i.TxIpv4(tcpSynPkt, protocol.IPH_PROTO_TCP, ipv4DstAddr)
}

func (i *NetIf) TxTcpSynAck(tcpSrcPort uint16, tcpDstPort uint16, ipv4DstAddr []byte) []byte {
	tcpSynAckPkt, err := protocol.BuildTcpSynAckPkt(tcpSrcPort, tcpDstPort, i.IpAddr, ipv4DstAddr, 0, 0)
	if err != nil {
		fmt.Printf("build tcp syn ack packet error: %v\n", err)
		return nil
	}
	return i.TxIpv4(tcpSynAckPkt, protocol.IPH_PROTO_TCP, ipv4DstAddr)
}

func (i *NetIf) TxTcpAck(tcpSrcPort uint16, tcpDstPort uint16, ipv4DstAddr []byte) []byte {
	tcpAckPkt, err := protocol.BuildTcpAckPkt(tcpSrcPort, tcpDstPort, i.IpAddr, ipv4DstAddr, 0, 0)
	if err != nil {
		fmt.Printf("build tcp ack packet error: %v\n", err)
		return nil
	}
	return i.TxIpv4(tcpAckPkt, protocol.IPH_PROTO_TCP, ipv4DstAddr)
}
