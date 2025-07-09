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
	handleFunc, exist := i.TcpServiceMap[tcpDstPort]
	if !exist {
		return
	}
	if flags&protocol.TCP_FLAGS_SYN != 0 && flags&protocol.TCP_FLAGS_ACK == 0 {
		i.TxTcp(nil, tcpDstPort, tcpSrcPort, ipv4SrcAddr, 1234567890, seqNum+1, protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK)
	} else if flags&protocol.TCP_FLAGS_SYN != 0 && flags&protocol.TCP_FLAGS_ACK != 0 {
		i.TxTcp(nil, tcpDstPort, tcpSrcPort, ipv4SrcAddr, 1234567891, seqNum+1, protocol.TCP_FLAGS_ACK)
	}
	handleFunc(TcpSession{RemoteIp: protocol.IpAddrToU(ipv4SrcAddr), RemotePort: tcpSrcPort}, tcpPayload, seqNum, ackNum, flags)
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

type TcpSession struct {
	RemoteIp   uint32
	RemotePort uint16
}

type TcpHandleFunc func(session TcpSession, payload []byte, seqNum uint32, ackNum uint32, flags uint8)

func (i *NetIf) RecvTcp(tcpPort uint16, handleFunc TcpHandleFunc) {
	i.TcpServiceMap[tcpPort] = handleFunc
}

func (i *NetIf) SendTcp(tcpPort uint16, session TcpSession, payload []byte, seqNum uint32, ackNum uint32, flags uint8) {
	i.TxTcp(payload, tcpPort, session.RemotePort, protocol.UToIpAddr(session.RemoteIp), seqNum, ackNum, flags)
}

func (i *NetIf) DialTcp(ipv4DstAddr []byte, tcpDstPort uint16, tcpSrcPort uint16) {
	i.TxTcp(nil, tcpSrcPort, tcpDstPort, ipv4DstAddr, 1234567890, 0, protocol.TCP_FLAGS_SYN)
}
