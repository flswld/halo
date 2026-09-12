package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

// RxUdp 接收 UDP 报文并分发给端口处理函数
func (i *NetIf) RxUdp(ipv4Payload []byte, ipv4SrcAddr protocol.Ipv4Addr) {
	udp, err := protocol.ParseUdpPkt(ipv4Payload, protocol.Ipv4AddrPair{SrcAddr: ipv4SrcAddr, DstAddr: i.IpAddr})
	if err != nil {
		Log(fmt.Sprintf("parse udp packet error: %v\n", err))
		return
	}
	handleFunc, exist := i.UdpServiceMap[udp.DstPort]
	if !exist {
		return
	}
	handleFunc(UdpSession{RemoteIp: protocol.IpAddrToU(ipv4SrcAddr), RemotePort: udp.SrcPort}, udp.Payload)
}

// TxUdp 构建并发送 UDP 报文
func (i *NetIf) TxUdp(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4DstAddr protocol.Ipv4Addr) bool {
	udpPkt := make([]byte, 0, 1480)
	udpPkt, err := protocol.BuildUdpPkt(udpPkt, protocol.UdpPkt{
		Payload: udpPayload,
		SrcPort: udpSrcPort,
		DstPort: udpDstPort,
	}, protocol.Ipv4AddrPair{SrcAddr: i.IpAddr, DstAddr: ipv4DstAddr})
	if err != nil {
		Log(fmt.Sprintf("build udp packet error: %v\n", err))
		return false
	}
	return i.TxIpv4(udpPkt, protocol.IPH_PROTO_UDP, ipv4DstAddr)
}

// RxUdpBroadcast 接收广播 UDP 报文并分发 DHCP 消息
func (i *NetIf) RxUdpBroadcast(ipv4Payload []byte, ipv4SrcAddr protocol.Ipv4Addr, ipv4DstAddr protocol.Ipv4Addr) {
	udp, err := protocol.ParseUdpPkt(ipv4Payload, protocol.Ipv4AddrPair{SrcAddr: ipv4SrcAddr, DstAddr: ipv4DstAddr})
	if err != nil {
		Log(fmt.Sprintf("parse udp packet error: %v\n", err))
		return
	}
	if udp.DstPort == protocol.DhcpClientPort || udp.DstPort == protocol.DhcpServerPort {
		i.RxDhcp(udp.Payload, udp.SrcPort, udp.DstPort, ipv4SrcAddr)
	}
}

// UdpSession 描述 UDP 对端会话
type UdpSession struct {
	RemoteIp   uint32 // 对端 IP 地址
	RemotePort uint16 // 对端端口
}

// UdpHandleFunc 定义 UDP 服务处理函数
type UdpHandleFunc func(session UdpSession, payload []byte)

// RecvUdp 注册指定本地端口的 UDP 服务处理函数
func (i *NetIf) RecvUdp(udpPort uint16, handleFunc UdpHandleFunc) {
	i.UdpServiceMap[udpPort] = handleFunc
}

// SendUdp 通过指定本地端口向 UDP 会话对端发送数据
func (i *NetIf) SendUdp(udpPort uint16, session UdpSession, payload []byte) {
	i.TxUdp(payload, udpPort, session.RemotePort, protocol.UToIpAddr(session.RemoteIp))
}
