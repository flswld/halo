package engine

import (
	"fmt"

	"github.com/flswld/halo/protocol"
)

// RxTcp 接收 TCP 段并分发给端口处理函数
func (i *NetIf) RxTcp(ipv4Payload []byte, ipv4SrcAddr protocol.Ipv4Addr) {
	tcp, err := protocol.ParseTcpPkt(ipv4Payload, protocol.Ipv4AddrPair{SrcAddr: ipv4SrcAddr, DstAddr: i.IpAddr})
	if err != nil {
		Log(fmt.Sprintf("parse tcp packet error: %v\n", err))
		return
	}
	handleFunc := i.TcpServiceMap[tcp.DstPort]
	if handleFunc == nil {
		return
	}
	handleFunc(ipv4SrcAddr, ipv4Payload)
}

// TcpHandleFunc 处理完整 TCP 段 回调返回后 packet 可被复用
type TcpHandleFunc func(remoteIp protocol.Ipv4Addr, packet []byte)

// RecvTcp 注册端口处理函数 nil 取消注册
func (i *NetIf) RecvTcp(tcpPort uint16, handleFunc TcpHandleFunc) {
	if handleFunc == nil {
		delete(i.TcpServiceMap, tcpPort)
	} else {
		i.TcpServiceMap[tcpPort] = handleFunc
	}
}

// SendTcp 发送完整 TCP 段 返回下层是否接受
func (i *NetIf) SendTcp(remoteIp protocol.Ipv4Addr, packet []byte) bool {
	if _, err := protocol.ParseTcpPkt(packet, protocol.Ipv4AddrPair{SrcAddr: i.IpAddr, DstAddr: remoteIp}); err != nil {
		Log(fmt.Sprintf("invalid tcp output packet: %v\n", err))
		return false
	}
	return i.TxIpv4(packet, protocol.IPH_PROTO_TCP, remoteIp)
}
