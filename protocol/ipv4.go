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
	switch pkt[9] {
	case IPH_PROTO_ICMP:
		ipHeadProto = IPH_PROTO_ICMP
	case IPH_PROTO_TCP:
		ipHeadProto = IPH_PROTO_TCP
	case IPH_PROTO_UDP:
		ipHeadProto = IPH_PROTO_UDP
	default:
		return nil, IPH_PROTO_UNKNOWN, nil, nil, errors.New("unknown ip protocol")
	}
	// 检查首部校验和
	if GetCheckSum(pkt[0:20]) != 0 {
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

func BuildIpv4Pkt(pkt []byte, payload []byte, ipHeadProto uint8, srcAddr []byte, dstAddr []byte) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 20)
	}
	if len(payload) > 1480 {
		return nil, errors.New("payload len must <= 1480 bytes")
	}
	if len(srcAddr) != 4 || len(dstAddr) != 4 {
		return nil, errors.New("src ip addr or dst ip addr len is not 4 bytes")
	}
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
	sum := GetCheckSum(pkt)
	pkt[10] = byte(sum >> 8)
	pkt[11] = byte(sum)
	// 上层数据
	pkt = append(pkt, payload...)
	return pkt, nil
}

func HandleIpv4PktTtl(pkt []byte) ([]byte, bool) {
	if pkt[8] <= 1 {
		return pkt, false
	}
	pkt[8] -= 0x01
	pkt = ReCalcIpv4CheckSum(pkt)
	return pkt, true
}

func ReCalcIpv4CheckSum(pkt []byte) []byte {
	pkt[10] = 0x00
	pkt[11] = 0x00
	sum := GetCheckSum(pkt[:20])
	pkt[10] = byte(sum >> 8)
	pkt[11] = byte(sum)
	return pkt
}

func ReCalcIcmpCheckSum(pkt []byte) []byte {
	pkt[22] = 0x00
	pkt[23] = 0x00
	sum := GetCheckSum(pkt)
	pkt[22] = byte(sum >> 8)
	pkt[23] = byte(sum)
	return pkt
}

func ReCalcTcpCheckSum(pkt []byte) []byte {
	pkt[36] = 0x00
	pkt[37] = 0x00
	fakeHeader := make([]byte, 0, 12)
	fakeHeader = append(fakeHeader, pkt[12:16]...)
	fakeHeader = append(fakeHeader, pkt[16:20]...)
	fakeHeader = append(fakeHeader, 0x00, 0x06)
	totalLen := int(binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]}))
	fakeHeader = append(fakeHeader, byte((totalLen-20)>>8), byte(totalLen-20))
	sumData := make([]byte, 0, 12+1500)
	sumData = append(sumData, fakeHeader...)
	sumData = append(sumData, pkt[20:]...)
	sum := GetCheckSum(sumData)
	pkt[36] = byte(sum >> 8)
	pkt[37] = byte(sum)
	return pkt
}

func ReCalcUdpCheckSum(pkt []byte) []byte {
	pkt[26] = 0x00
	pkt[27] = 0x00
	fakeHeader := make([]byte, 0, 12)
	fakeHeader = append(fakeHeader, pkt[12:16]...)
	fakeHeader = append(fakeHeader, pkt[16:20]...)
	fakeHeader = append(fakeHeader, 0x00, 0x11)
	totalLen := int(binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]}))
	fakeHeader = append(fakeHeader, byte((totalLen-20)>>8), byte(totalLen-20))
	sumData := make([]byte, 0, 12+1500)
	sumData = append(sumData, fakeHeader...)
	sumData = append(sumData, pkt[20:]...)
	sum := GetCheckSum(sumData)
	pkt[26] = byte(sum >> 8)
	pkt[27] = byte(sum)
	return pkt
}

func NatGetSrcDstPort(pkt []byte) (srcPort uint16, dstPort uint16) {
	switch pkt[9] {
	case IPH_PROTO_ICMP:
		srcPort = binary.BigEndian.Uint16(pkt[24:26])
		dstPort = binary.BigEndian.Uint16(pkt[24:26])
	case IPH_PROTO_TCP:
		srcPort = binary.BigEndian.Uint16(pkt[20:22])
		dstPort = binary.BigEndian.Uint16(pkt[22:24])
	case IPH_PROTO_UDP:
		srcPort = binary.BigEndian.Uint16(pkt[20:22])
		dstPort = binary.BigEndian.Uint16(pkt[22:24])
	}
	return srcPort, dstPort
}

func NatChangeSrc(pkt []byte, ipAddr []byte, port uint16) []byte {
	pkt[12] = ipAddr[0]
	pkt[13] = ipAddr[1]
	pkt[14] = ipAddr[2]
	pkt[15] = ipAddr[3]
	pkt = ReCalcIpv4CheckSum(pkt)
	switch pkt[9] {
	case IPH_PROTO_ICMP:
		pkt[24] = byte(port >> 8)
		pkt[25] = byte(port)
		pkt = ReCalcIcmpCheckSum(pkt)
	case IPH_PROTO_TCP:
		pkt[20] = byte(port >> 8)
		pkt[21] = byte(port)
		pkt = ReCalcTcpCheckSum(pkt)
	case IPH_PROTO_UDP:
		pkt[20] = byte(port >> 8)
		pkt[21] = byte(port)
		pkt = ReCalcUdpCheckSum(pkt)
	}
	return pkt
}

func NatChangeDst(pkt []byte, ipAddr []byte, port uint16) []byte {
	pkt[16] = ipAddr[0]
	pkt[17] = ipAddr[1]
	pkt[18] = ipAddr[2]
	pkt[19] = ipAddr[3]
	pkt = ReCalcIpv4CheckSum(pkt)
	switch pkt[9] {
	case IPH_PROTO_ICMP:
		pkt[24] = byte(port >> 8)
		pkt[25] = byte(port)
		pkt = ReCalcIcmpCheckSum(pkt)
	case IPH_PROTO_TCP:
		pkt[22] = byte(port >> 8)
		pkt[23] = byte(port)
		pkt = ReCalcTcpCheckSum(pkt)
	case IPH_PROTO_UDP:
		pkt[22] = byte(port >> 8)
		pkt[23] = byte(port)
		pkt = ReCalcUdpCheckSum(pkt)
	}
	return pkt
}
