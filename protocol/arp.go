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

func ParseArpPkt(pkt []byte) (option uint16, srcMac []byte, srcAddr []byte, dstMac []byte, dstAddr []byte, err error) {
	if len(pkt) < 28 {
		return ARP_UNKNOWN, nil, nil, nil, nil, errors.New("arp packet len < 28 bytes")
	}
	// 操作类型
	switch binary.BigEndian.Uint16([]byte{pkt[6], pkt[7]}) {
	case ARP_REQUEST:
		option = ARP_REQUEST
	case ARP_REPLY:
		option = ARP_REPLY
	default:
		return ARP_UNKNOWN, nil, nil, nil, nil, errors.New("unknown arp option")
	}
	// 地址
	srcMac = pkt[8:14]
	srcAddr = pkt[14:18]
	dstMac = pkt[18:24]
	dstAddr = pkt[24:28]
	return option, srcMac, srcAddr, dstMac, dstAddr, nil
}

func BuildArpPkt(option uint16, srcMac []byte, srcAddr []byte, dstMac []byte, dstAddr []byte) (pkt []byte, err error) {
	if len(srcMac) != 6 || len(dstMac) != 6 {
		return nil, errors.New("src mac addr or dst mac addr len is not 6 bytes")
	}
	if len(srcAddr) != 4 || len(dstAddr) != 4 {
		return nil, errors.New("src ip addr or dst ip addr len is not 4 bytes")
	}
	pkt = make([]byte, 0, 28)
	// 硬件类型+协议类型+硬件地址长度+协议长度
	pkt = append(pkt, 0x00, 0x01, 0x08, 0x00, 0x06, 0x04)
	// 操作类型
	pkt = append(pkt, byte(option>>8), byte(option))
	// 地址
	pkt = append(pkt, srcMac...)
	pkt = append(pkt, srcAddr...)
	pkt = append(pkt, dstMac...)
	pkt = append(pkt, dstAddr...)
	return pkt, nil
}
