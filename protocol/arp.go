package protocol

import (
	"encoding/binary"
	"errors"
)

/*
								ARP报文
0									2									4(字节)
+-----------------------------------------------------------------------+
|				硬件类型				|				协议类型				|
+-----------------------------------------------------------------------+
|	硬件地址长度	|		协议长度		|				操作类型				|
+-----------------------------------------------------------------------+
|																		|
+			发送方MAC地址				+-----------------------------------+
|									|			发送方IP地址				|
+-----------------------------------+-----------------------------------+
|			发送方IP地址				|									|
+-----------------------------------+			目标MAC地址				+
|																		|
+-----------------------------------------------------------------------+
|								目标IP地址								|
+-----------------------------------------------------------------------+
*/

const (
	ARP_REQUEST uint16 = 0x0001
	ARP_REPLY   uint16 = 0x0002
	ARP_UNKNOWN uint16 = 0xffff
)

// ArpPkt 保存 ARP 报文的编解码字段
// Parse 返回的地址按值保存 不引用输入缓冲区
type ArpPkt struct {
	Option  uint16   // ARP 操作类型
	SrcMac  MacAddr  // 发送方 MAC 地址
	SrcAddr Ipv4Addr // 发送方 IPv4 地址
	DstMac  MacAddr  // 目标 MAC 地址
	DstAddr Ipv4Addr // 目标 IPv4 地址
}

// ParseArpPkt 解析 ARP 报文的操作类型和地址字段
func ParseArpPkt(pkt []byte) (result ArpPkt, err error) {
	if len(pkt) < 28 {
		return ArpPkt{Option: ARP_UNKNOWN}, errors.New("arp packet len < 28 bytes")
	}
	// 操作类型
	switch binary.BigEndian.Uint16([]byte{pkt[6], pkt[7]}) {
	case ARP_REQUEST:
		result.Option = ARP_REQUEST
	case ARP_REPLY:
		result.Option = ARP_REPLY
	default:
		return ArpPkt{Option: ARP_UNKNOWN}, errors.New("unknown arp option")
	}
	// 地址
	result.SrcMac = MacAddr(pkt[8:14])
	result.SrcAddr = Ipv4Addr(pkt[14:18])
	result.DstMac = MacAddr(pkt[18:24])
	result.DstAddr = Ipv4Addr(pkt[24:28])
	return result, nil
}

// BuildArpPkt 构建以太网和 IPv4 使用的 ARP 报文
// pkt 应为 nil 或长度为 0 的可复用缓冲区 返回值持有构建后的报文字节
func BuildArpPkt(pkt []byte, packet ArpPkt) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 28)
	}
	// 固定编码以太网和 IPv4 的硬件类型 协议类型及地址长度
	pkt = append(pkt, 0x00, 0x01, 0x08, 0x00, 0x06, 0x04)
	// 操作类型
	pkt = append(pkt, byte(packet.Option>>8), byte(packet.Option))
	// 地址
	pkt = append(pkt, packet.SrcMac[:]...)
	pkt = append(pkt, packet.SrcAddr[:]...)
	pkt = append(pkt, packet.DstMac[:]...)
	pkt = append(pkt, packet.DstAddr[:]...)
	return pkt, nil
}
