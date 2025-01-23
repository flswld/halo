package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxIcmp(ipv4Payload []byte, ipv4SrcAddr []byte) {
	icmpPayload, icmpType, icmpId, icmpSeq, err := protocol.ParseIcmpPkt(ipv4Payload)
	if err != nil {
		Log(fmt.Sprintf("parse icmp packet error: %v\n", err))
		return
	}
	if icmpType == protocol.ICMP_REQUEST {
		// 构造ICMP响应包
		icmpPkt, err := protocol.BuildIcmpPkt(icmpPayload, protocol.ICMP_REPLY, icmpId, icmpSeq)
		if err != nil {
			Log(fmt.Sprintf("build icmp packet error: %v\n", err))
			return
		}
		i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4SrcAddr)
	}
}

func (i *NetIf) TxIcmp(icmpPayload []byte, seq uint16, ipv4DstAddr []byte) []byte {
	icmpPkt, err := protocol.BuildIcmpPkt(icmpPayload, protocol.ICMP_REQUEST, []byte{0x00, 0x01}, []byte{uint8(seq >> 8), uint8(seq)})
	if err != nil {
		Log(fmt.Sprintf("build icmp packet error: %v\n", err))
		return nil
	}
	return i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4DstAddr)
}
