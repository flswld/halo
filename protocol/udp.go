package protocol

import (
	"encoding/binary"
	"errors"
)

/*
					UDP报文
0						2						4(字节)
+-----------------------------------------------+
|			源端口		|		目标端口			|
+-----------------------------------------------+
|			总长度		|		校验和			|
+-----------------------------------------------+
|						数据						|
+-----------------------------------------------+
*/

func ParseUdpPkt(pkt []byte, srcAddr []byte, dstAddr []byte) (payload []byte, srcPort uint16, dstPort uint16, err error) {
	if len(pkt) < 8 || len(pkt) > 1480 {
		return nil, 0, 0, errors.New("udp packet len must >= 8 and <= 1480 bytes")
	}
	// 源端口
	srcPort = binary.BigEndian.Uint16([]byte{pkt[0], pkt[1]})
	// 目标端口
	dstPort = binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]})
	// 总长度
	totalLen := int(binary.BigEndian.Uint16([]byte{pkt[4], pkt[5]}))
	// 检查校验和
	if pkt[6] != 0x00 && pkt[7] != 0x00 {
		fakeHeader := make([]byte, 0)
		fakeHeader = append(fakeHeader, srcAddr...)
		fakeHeader = append(fakeHeader, dstAddr...)
		fakeHeader = append(fakeHeader, 0x00, 0x11)
		fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
		sumData := make([]byte, 0)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt...)
		sum := GetCheckSum(sumData)
		if binary.BigEndian.Uint16(sum) != 0 {
			return nil, 0, 0, errors.New("check sum error")
		}
	}
	// 数据
	payload = pkt[8:totalLen]
	return payload, srcPort, dstPort, nil
}

func BuildUdpPkt(payload []byte, srcPort uint16, dstPort uint16, srcAddr []byte, dstAddr []byte) (pkt []byte, err error) {
	if len(payload) > 1472 {
		return nil, errors.New("payload len must <= 1472")
	}
	pkt = make([]byte, 0, 8)
	// 源端口
	pkt = append(pkt, byte(srcPort>>8), byte(srcPort))
	// 目标端口
	pkt = append(pkt, byte(dstPort>>8), byte(dstPort))
	// 总长度
	udpPktLen := uint16(len(payload) + 8)
	pkt = append(pkt, byte(udpPktLen>>8), byte(udpPktLen))
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 上层数据
	pkt = append(pkt, payload...)
	// 计算校验和
	fakeHeader := make([]byte, 0)
	fakeHeader = append(fakeHeader, srcAddr...)
	fakeHeader = append(fakeHeader, dstAddr...)
	// 保留字节0x00+UDP协议号0x11
	fakeHeader = append(fakeHeader, 0x00, 0x11)
	// UDP报文总长度
	fakeHeader = append(fakeHeader, byte(udpPktLen>>8), byte(udpPktLen))
	sumData := make([]byte, 0)
	sumData = append(sumData, fakeHeader...)
	sumData = append(sumData, pkt...)
	sum := GetCheckSum(sumData)
	pkt[6] = sum[0]
	pkt[7] = sum[1]
	return pkt, nil
}
