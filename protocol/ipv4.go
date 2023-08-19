package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
)

/*
										IP报文头部
0			3				7				15			18			23					32(位)
+---------------------------------------------------------------------------------------+
|	版本		|	首部长度		|	服务类型		|					总长度					|
+---------------------------------------------------------------------------------------+
|					标识						|	标志位	|			片偏移				|
+---------------------------------------------------------------------------------------+
|			生存时间			|		协议		|				首部校验和					|
+---------------------------------------------------------------------------------------+
|											源地址										|
+---------------------------------------------------------------------------------------+
|											目的地址										|
+---------------------------------------------------------------------------------------+
|											选项								|	(填充)	|
+---------------------------------------------------------------------------------------+
*/

const (
	IPH_PROTO_ICMP    uint8 = 0x01
	IPH_PROTO_TCP     uint8 = 0x06
	IPH_PROTO_UDP     uint8 = 0x11
	IPH_PROTO_UNKNOWN uint8 = 0xff
)

var iphId uint16 = 0x0000

func SetRandIpHeaderId() {
	randByte := make([]byte, 2)
	_, err := rand.Read(randByte)
	if err != nil {
		randByte[0] = 0x45
		randByte[1] = 0x67
	}
	iphId = binary.BigEndian.Uint16([]byte{randByte[0], randByte[1]})
}

func ParseIpv4Pkt(pkt []byte) (payload []byte, ipHeadProto uint8, srcAddr []byte, dstAddr []byte, err error) {
	if len(pkt) < 20 || len(pkt) > 1500 {
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("ip packet len must >= 20 and <= 1500 bytes")
	}
	if pkt[0] != 0x45 {
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("not support type of ip packet")
	}
	// 总长度
	totalLen := int(binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]}))
	// 不支持分片
	if (pkt[6] != 0x40 && pkt[6] != 0x00) || pkt[7] != 0x00 {
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("not support ip frg")
	}
	// 协议
	protocolParse := pkt[9]
	if protocolParse == IPH_PROTO_ICMP {
		ipHeadProto = IPH_PROTO_ICMP
	} else if protocolParse == IPH_PROTO_TCP {
		ipHeadProto = IPH_PROTO_TCP
	} else if protocolParse == IPH_PROTO_UDP {
		ipHeadProto = IPH_PROTO_UDP
	} else {
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("unknown ip protocol")
	}
	// 检查首部校验和
	headerSum := GetCheckSum(pkt[0:20])
	if binary.BigEndian.Uint16(headerSum) != 0 {
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("header check sum error")
	}
	// 源地址
	srcAddr = pkt[12:16]
	// 目的地址
	dstAddr = pkt[16:20]
	// 数据
	payload = pkt[20:totalLen]
	return payload, ipHeadProto, srcAddr, dstAddr, nil
}

func BuildIpv4Pkt(payload []byte, ipHeadProto uint8, srcAddr []byte, dstAddr []byte) (pkt []byte, err error) {
	if len(payload) > 1480 {
		return nil, errors.New("payload len must <= 1480 bytes")
	}
	if len(srcAddr) != 4 || len(dstAddr) != 4 {
		return nil, errors.New("src ip addr or dst ip addr len is not 4 bytes")
	}
	pkt = make([]byte, 0, 20)
	// 版本(IPV4)+首部长度(20字节)+服务类型(0x00)
	pkt = append(pkt, 0x45, 0x00)
	// 总长度
	ipPktLen := uint16(len(payload) + 20)
	pkt = append(pkt, byte(ipPktLen>>8), byte(ipPktLen))
	// 标识
	iphId++
	pkt = append(pkt, byte(iphId>>8), byte(iphId))
	// 标志位+片偏移(不分片)
	pkt = append(pkt, 0x00, 0x00)
	// 生存时间(128)
	pkt = append(pkt, 0x80)
	// 协议
	pkt = append(pkt, ipHeadProto)
	// 首部校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 源地址
	pkt = append(pkt, srcAddr...)
	// 目的地址
	pkt = append(pkt, dstAddr...)
	// 计算首部校验和
	headerSum := GetCheckSum(pkt)
	pkt[10] = headerSum[0]
	pkt[11] = headerSum[1]
	// 上层数据
	pkt = append(pkt, payload...)
	return pkt, nil
}
