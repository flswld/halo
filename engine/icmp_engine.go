package engine

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/flswld/halo/protocol"
)

// RxIcmp 接收 ICMP 报文并回应回显请求
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

// TxIcmp 构建并发送 ICMP 报文
func (i *NetIf) TxIcmp(icmpPayload []byte, icmpType uint8, icmpId []byte, icmpSeq uint16, ipv4DstAddr []byte) bool {
	icmpPkt := make([]byte, 0, 1480)
	icmpPkt, err := protocol.BuildIcmpPkt(icmpPkt, icmpPayload, icmpType, icmpId, icmpSeq)
	if err != nil {
		Log(fmt.Sprintf("build icmp packet error: %v\n", err))
		return false
	}
	return i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4DstAddr)
}

// Ping 向目标 IPv4 地址发送指定次数的 ICMP 回显请求
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

// IcmpTtlDeepNat 修正 ICMP 超时报文中携带的 NAT 原始流信息
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
