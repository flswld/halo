package protocol

import (
	"encoding/binary"
	"errors"
)

/*
												TCP报文头部
0				3			7						15													32(位)
+-------------------------------------------------------------------------------------------------------+
|						源端口						|						目的端口						|
+-------------------------------------------------------------------------------------------------------+
|													序号													|
+-------------------------------------------------------------------------------------------------------+
|													确认号												|
+-------------------------------------------------------------------------------------------------------+
|	数据偏移		|	保留		|URG|ACK|PSH|RST|SYN|FIN|						窗口大小						|
+-------------------------------------------------------------------------------------------------------+
|						校验和						|						紧急指针						|
+-------------------------------------------------------------------------------------------------------+
|											选项												|	(填充)	|
+-------------------------------------------------------------------------------------------------------+
*/

const (
	TCP_FLAGS_URG = 0x20
	TCP_FLAGS_ACK = 0x10
	TCP_FLAGS_PSH = 0x08
	TCP_FLAGS_RST = 0x04
	TCP_FLAGS_SYN = 0x02
	TCP_FLAGS_FIN = 0x01
)

func ParseTcpPkt(pkt []byte, srcAddr []byte, dstAddr []byte) (payload []byte, srcPort uint16, dstPort uint16, seqNum uint32, ackNum uint32, flags uint8, err error) {
	if len(pkt) < 20 || len(pkt) > 1480 {
		return nil, 0, 0, 0, 0, 0, errors.New("tcp packet len must >= 20 and <= 1480 bytes")
	}
	// 源端口
	srcPort = binary.BigEndian.Uint16([]byte{pkt[0], pkt[1]})
	// 目标端口
	dstPort = binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]})
	// 序号
	seqNum = binary.BigEndian.Uint32([]byte{pkt[4], pkt[5], pkt[6], pkt[7]})
	// 确认号
	ackNum = binary.BigEndian.Uint32([]byte{pkt[8], pkt[9], pkt[10], pkt[11]})
	// 数据偏移+保留+FLAGS
	headerLen := int(pkt[12] >> 4)
	flags = pkt[13]
	// 检查校验和
	if pkt[16] != 0x00 && pkt[17] != 0x00 {
		totalLen := len(pkt)
		fakeHeader := make([]byte, 0)
		fakeHeader = append(fakeHeader, srcAddr...)
		fakeHeader = append(fakeHeader, dstAddr...)
		fakeHeader = append(fakeHeader, 0x00, 0x06)
		fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
		sumData := make([]byte, 0)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt...)
		sum := GetCheckSum(sumData)
		if binary.BigEndian.Uint16(sum) != 0 {
			return nil, 0, 0, 0, 0, 0, errors.New("check sum error")
		}
	}
	// 数据
	payload = pkt[headerLen:]
	return payload, srcPort, dstPort, seqNum, ackNum, flags, nil
}

func BuildTcpPkt(payload []byte, srcPort uint16, dstPort uint16, srcAddr []byte, dstAddr []byte, seqNum uint32, ackNum uint32, flags uint8) (pkt []byte, err error) {
	if len(srcAddr) != 4 || len(dstAddr) != 4 {
		return nil, errors.New("src ip addr or dst ip addr len is not 4 bytes")
	}
	pkt = make([]byte, 0, 20)
	// 源端口
	pkt = append(pkt, byte(srcPort>>8), byte(srcPort))
	// 目的端口
	pkt = append(pkt, byte(dstPort>>8), byte(dstPort))
	// 序号
	pkt = append(pkt, byte(seqNum>>24), byte(seqNum>>16), byte(seqNum>>8), byte(seqNum))
	// 确认号
	pkt = append(pkt, byte(ackNum>>24), byte(ackNum>>16), byte(ackNum>>8), byte(ackNum))
	// 数据偏移+保留+FLAGS为头部长度20字节的TCP包
	pkt = append(pkt, 0x05, flags)
	// 窗口大小 256
	pkt = append(pkt, 0x01, 0x00)
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 紧急指针
	pkt = append(pkt, 0x00, 0x00)
	// 数据
	pkt = append(pkt, payload...)
	// 计算校验和
	fakeHeader := make([]byte, 0)
	fakeHeader = append(fakeHeader, srcAddr...)
	fakeHeader = append(fakeHeader, dstAddr...)
	// 保留字节0x00+TCP协议号0x06
	fakeHeader = append(fakeHeader, 0x00, 0x06)
	// TCP报文总长度
	totalLen := 20 + len(payload)
	fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
	checkSumSrc := make([]byte, 0)
	checkSumSrc = append(checkSumSrc, fakeHeader...)
	checkSumSrc = append(checkSumSrc, pkt...)
	sum := GetCheckSum(checkSumSrc)
	pkt[16] = sum[0]
	pkt[17] = sum[1]
	return pkt, nil
}
