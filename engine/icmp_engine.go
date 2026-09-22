package engine

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/flswld/halo/protocol"
)

// RxIcmp 接收 ICMP 报文并回应回显请求
func (i *NetIf) RxIcmp(ipv4Payload []byte, ipv4SrcAddr protocol.Ipv4Addr) {
	icmp, err := protocol.ParseIcmpPkt(ipv4Payload)
	if err != nil {
		Log(fmt.Sprintf("parse icmp packet error: %v\n", err))
		return
	}
	switch icmp.IcmpType {
	case protocol.ICMP_REQUEST:
		// 构造ICMP响应包
		i.TxIcmp(icmp.Payload, protocol.ICMP_REPLY, icmp.IcmpId, icmp.IcmpSeq, ipv4SrcAddr)
	}
}

// TxIcmp 构建并发送 ICMP 报文
func (i *NetIf) TxIcmp(icmpPayload []byte, icmpType uint8, icmpId []byte, icmpSeq uint16, ipv4DstAddr protocol.Ipv4Addr) bool {
	icmpPkt := make([]byte, 0, 1480)
	icmpPkt, err := protocol.BuildIcmpPkt(icmpPkt, protocol.IcmpPkt{
		Payload:  icmpPayload,
		IcmpType: icmpType,
		IcmpId:   icmpId,
		IcmpSeq:  icmpSeq,
	})
	if err != nil {
		Log(fmt.Sprintf("build icmp packet error: %v\n", err))
		return false
	}
	return i.TxIpv4(icmpPkt, protocol.IPH_PROTO_ICMP, ipv4DstAddr)
}

// Ping 向目标 IPv4 地址发送指定次数的 ICMP 回显请求
func (i *NetIf) Ping(ipv4DstAddr protocol.Ipv4Addr, count int) {
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
	ipv4, err := protocol.ParseIpv4Pkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse ip packet error: %v\n", err))
		return ethPayload, false
	}
	if ipv4.IpHeadProto != protocol.IPH_PROTO_ICMP {
		return ethPayload, false
	}
	icmp, err := protocol.ParseIcmpPkt(ipv4.Payload)
	if err != nil {
		Log(fmt.Sprintf("parse icmp packet error: %v\n", err))
		return ethPayload, false
	}
	if icmp.IcmpType != protocol.ICMP_TTL {
		return ethPayload, false
	}
	if len(icmp.Payload) < 28 {
		return ethPayload, false
	}
	_ipv4HeadProto := icmp.Payload[9]
	wanIpAddr := protocol.Ipv4Addr(icmp.Payload[12:16])
	remoteIpAddr := protocol.Ipv4Addr(icmp.Payload[16:20])
	wanPort, remotePort := protocol.NatGetSrcDstPort(icmp.Payload)
	natFlow, exist := i.NatGetFlowByWan(remoteIpAddr, remotePort, wanIpAddr, wanPort, _ipv4HeadProto)
	if !exist {
		return ethPayload, false
	}
	icmp.Payload = protocol.NatChangeSrc(icmp.Payload, protocol.UToIpAddr(natFlow.LanHostIpAddr), natFlow.LanHostPort)
	ethPayload = protocol.NatChangeDst(ethPayload, protocol.UToIpAddr(natFlow.LanHostIpAddr), 0)
	return ethPayload, true
}
