package protocol

import (
	"encoding/binary"
	"errors"
)

/*
				以太网帧头部
0				6				12			14(字节)
+-------------------------------------------+
|	目的MAC地址	|	源MAC地址	|	类型		|
+-------------------------------------------+
*/

const (
	IEEE_802_3        uint16 = 0x05dc
	ETH_PROTO_IPV4    uint16 = 0x0800
	ETH_PROTO_ARP     uint16 = 0x0806
	ETH_PROTO_IPV6    uint16 = 0x86dd
	ETH_PROTO_UNKNOWN uint16 = 0xffff
)

var (
	BROADCAST_MAC_ADDR = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
)

func ParseEthFrm(frm []byte) (payload []byte, dstMac []byte, srcMac []byte, ethProto uint16, err error) {
	if len(frm) < 42 || len(frm) > 1514 {
		return nil, nil, nil, ETH_PROTO_UNKNOWN, errors.New("ethernet frame len must >= 42 and <= 1514 bytes")
	}
	// 目的MAC地址
	dstMac = frm[0:6]
	// 源MAC地址
	srcMac = frm[6:12]
	// 类型
	switch binary.BigEndian.Uint16([]byte{frm[12], frm[13]}) {
	case IEEE_802_3:
		ethProto = IEEE_802_3
	case ETH_PROTO_IPV4:
		ethProto = ETH_PROTO_IPV4
	case ETH_PROTO_ARP:
		ethProto = ETH_PROTO_ARP
	case ETH_PROTO_IPV6:
		ethProto = ETH_PROTO_IPV6
	default:
		return nil, nil, nil, ETH_PROTO_UNKNOWN, errors.New("unknown ethernet protocol")
	}
	// 数据
	payload = frm[14:]
	// 若数据小于46字节则返回的数据会包含末尾的填充字节
	return payload, dstMac, srcMac, ethProto, nil
}

func BuildEthFrm(frm []byte, payload []byte, dstMac []byte, srcMac []byte, ethProto uint16) ([]byte, error) {
	if frm == nil {
		frm = make([]byte, 0, 60)
	}
	if len(payload) > 1500 {
		return nil, errors.New("payload len must <= 1500 bytes")
	}
	if len(dstMac) != 6 || len(srcMac) != 6 {
		return nil, errors.New("dst mac addr or src mac addr len is not 6 bytes")
	}
	// 目的MAC地址
	frm = append(frm, dstMac...)
	// 源MAC地址
	frm = append(frm, srcMac...)
	// 协议类型
	frm = append(frm, byte(ethProto>>8), byte(ethProto))
	// 上层数据
	frm = append(frm, payload...)
	// 小于60字节填充0
	n := 60 - len(frm)
	for i := 0; i < n; i++ {
		frm = append(frm, 0x00)
	}
	return frm, nil
}
