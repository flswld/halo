package engine

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) RxIcmp(ipv4Payload []byte, ipv4SrcAddr []byte) {
	icmpPayload, icmpType, icmpId, icmpSeq, err := protocol.ParseIcmpPkt(ipv4Payload)
	if err != nil {
		Log(fmt.Sprintf("parse icmp packet error: %v\n", err))
		return
	}
	switch icmpType {
	case protocol.ICMP_REQUEST:
		// 构造ICMP响应包
		i.TxIcmp(icmpPayload, protocol.ICMP_REPLY, icmpId, icmpSeq, ipv4SrcAddr)
	}
}

func (i *NetIf) TxIcmp(icmpPayload []byte, icmpType uint8, icmpId []byte, icmpSeq uint16, ipv4DstAddr []byte) []byte {
	icmpPkt, err := protocol.BuildIcmpPkt(icmpPayload, icmpType, icmpId, icmpSeq)
	if err != nil {
		Log(fmt.Sprintf("build icmp packet error: %v\n", err))
		return nil
	}
	return i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4DstAddr)
}

func (i *NetIf) Ping(ipv4DstAddr []byte, count int) {
	randByte := make([]byte, 2)
	_, err := rand.Read(randByte)
	if err != nil {
		randByte[0] = 0x45
		randByte[1] = 0x67
	}
	icmpSeq := uint16(0)
	ticker := time.NewTicker(time.Second)
	for c := 0; c < count; c++ {
		<-ticker.C
		icmpSeq++
		i.TxIcmp(protocol.ICMP_DEFAULT_PAYLOAD, protocol.ICMP_REQUEST, randByte, icmpSeq, ipv4DstAddr)
	}
	ticker.Stop()
}

func (i *NetIf) IcmpTtlDeepNat(ethPayload []byte) ([]byte, bool) {
	ipv4Payload, ipv4HeadProto, _, _, err := protocol.ParseIpv4Pkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse ip packet error: %v\n", err))
		return ethPayload, false
	}
	if ipv4HeadProto != protocol.IPH_PROTO_ICMP {
		return ethPayload, false
	}
	icmpPayload, icmpType, _, _, err := protocol.ParseIcmpPkt(ipv4Payload)
	if err != nil {
		Log(fmt.Sprintf("parse icmp packet error: %v\n", err))
		return ethPayload, false
	}
	if icmpType != protocol.ICMP_TTL {
		return ethPayload, false
	}
	if len(icmpPayload) < 28 {
		return ethPayload, false
	}
	_ipv4HeadProto := icmpPayload[9]
	wanIpAddr := icmpPayload[12:16]
	remoteIpAddr := icmpPayload[16:20]
	wanPort, remotePort := protocol.NatGetSrcDstPort(icmpPayload)
	natFlow := i.NatGetFlowByWan(remoteIpAddr, remotePort, wanIpAddr, wanPort, _ipv4HeadProto)
	if natFlow == nil {
		return ethPayload, false
	}
	icmpPayload = protocol.NatChangeSrc(icmpPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
	ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), 0)
	return ethPayload, true
}
