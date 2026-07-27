package protocol

import (
	"encoding/binary"
	"errors"
)

/*
					ICMP报文
0						2						4(字节)
+-----------------------------------------------+
|	类型		|	代码		|		校验和			|
+-----------------------------------------------+
|			标识			|		序号				|
+-----------------------------------------------+
|						数据						|
+-----------------------------------------------+
*/

var ICMP_DEFAULT_PAYLOAD = []byte{
	0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70,
	0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
}

const (
	ICMP_REQUEST uint8 = 0x08
	ICMP_REPLY   uint8 = 0x00
	ICMP_TTL     uint8 = 0x0b
	ICMP_UNKNOWN uint8 = 0xff
)

// ParseIcmpPkt 解析受支持的 ICMP 报文并验证校验和
func ParseIcmpPkt(pkt []byte) (payload []byte, icmpType uint8, icmpId []byte, icmpSeq uint16, err error) {
	if len(pkt) < 8 || len(pkt) > 1480 {
		return nil, ICMP_UNKNOWN, nil, 0, errors.New("icmp packet len must >= 8 and <= 1480 bytes")
	}
	// 类型
	switch pkt[0] {
	case ICMP_REQUEST:
		icmpType = ICMP_REQUEST
	case ICMP_REPLY:
		icmpType = ICMP_REPLY
	case ICMP_TTL:
		icmpType = ICMP_TTL
	default:
		return nil, ICMP_UNKNOWN, nil, 0, errors.New("not support type of icmp packet")
	}
	// 代码
	if pkt[1] != 0x00 {
		return nil, ICMP_UNKNOWN, nil, 0, errors.New("not support type of icmp packet")
	}
	// 完整报文包含原校验和时计算结果应为零
	if GetCheckSum(pkt) != 0 {
		return nil, ICMP_UNKNOWN, nil, 0, errors.New("check sum error")
	}
	// 标识
	icmpId = pkt[4:6]
	// 序号
	icmpSeq = binary.BigEndian.Uint16(pkt[6:8])
	// 数据
	payload = pkt[8:]
	return payload, icmpType, icmpId, icmpSeq, nil
}

// BuildIcmpPkt 构建 ICMP 报文并计算校验和
func BuildIcmpPkt(pkt []byte, payload []byte, icmpType uint8, icmpId []byte, icmpSeq uint16) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 40)
	}
	if len(payload) > 1472 {
		return nil, errors.New("payload len must <= 1472")
	}
	// 类型
	pkt = append(pkt, icmpType)
	// 代码
	pkt = append(pkt, 0x00)
	// 校验和字段先清零再对完整 ICMP 报文计算
	pkt = append(pkt, 0x00, 0x00)
	// 标识
	pkt = append(pkt, icmpId...)
	// 序号
	pkt = append(pkt, uint8(icmpSeq>>8), uint8(icmpSeq))
	// 数据
	pkt = append(pkt, payload...)
	sum := GetCheckSum(pkt)
	pkt[2] = byte(sum >> 8)
	pkt[3] = byte(sum)
	return pkt, nil
}
