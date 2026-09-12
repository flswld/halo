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
	BROADCAST_MAC_ADDR = MacAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
)

// EthFrm 保存以太网帧的编解码字段
// 地址按值保存 Payload 引用输入缓冲区 使用载荷期间不得复用该缓冲区
type EthFrm struct {
	Payload  []byte  // 上层载荷 包含可能的以太网填充
	DstMac   MacAddr // 目的 MAC 地址
	SrcMac   MacAddr // 源 MAC 地址
	EthProto uint16  // 以太网协议类型
}

// ParseEthFrm 解析以太网帧头部和载荷
func ParseEthFrm(frm []byte) (result EthFrm, err error) {
	// 当前引擎只接受不带 VLAN 标签的标准以太网帧
	if len(frm) < 42 || len(frm) > 1514 {
		return EthFrm{EthProto: ETH_PROTO_UNKNOWN}, errors.New("ethernet frame len must >= 42 and <= 1514 bytes")
	}
	// 目的MAC地址
	result.DstMac = MacAddr(frm[0:6])
	// 源MAC地址
	result.SrcMac = MacAddr(frm[6:12])
	// 类型
	switch binary.BigEndian.Uint16([]byte{frm[12], frm[13]}) {
	case IEEE_802_3:
		result.EthProto = IEEE_802_3
	case ETH_PROTO_IPV4:
		result.EthProto = ETH_PROTO_IPV4
	case ETH_PROTO_ARP:
		result.EthProto = ETH_PROTO_ARP
	case ETH_PROTO_IPV6:
		result.EthProto = ETH_PROTO_IPV6
	default:
		return EthFrm{EthProto: ETH_PROTO_UNKNOWN}, errors.New("unknown ethernet protocol")
	}
	// 数据
	result.Payload = frm[14:]
	// 若数据小于46字节则返回的数据会包含末尾的填充字节
	return result, nil
}

// BuildEthFrm 构建以太网帧并按最小帧长填充
// frm 应为 nil 或长度为 0 的可复用缓冲区 返回值持有构建后的报文字节
func BuildEthFrm(frm []byte, packet EthFrm) ([]byte, error) {
	if frm == nil {
		frm = make([]byte, 0, 60)
	}
	if len(packet.Payload) > 1500 {
		return nil, errors.New("payload len must <= 1500 bytes")
	}
	// 目的MAC地址
	frm = append(frm, packet.DstMac[:]...)
	// 源MAC地址
	frm = append(frm, packet.SrcMac[:]...)
	// 协议类型
	frm = append(frm, byte(packet.EthProto>>8), byte(packet.EthProto))
	// 上层数据
	frm = append(frm, packet.Payload...)
	// 不含 FCS 的帧不足 60 字节时补零到最小长度
	n := 60 - len(frm)
	for i := 0; i < n; i++ {
		frm = append(frm, 0x00)
	}
	return frm, nil
}
