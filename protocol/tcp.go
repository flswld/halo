package protocol

import (
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

func BuildTcpSynPkt(srcPort uint16, dstPort uint16, srcAddr []byte, dstAddr []byte, seqNum uint32) (pkt []byte, err error) {
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
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	// 数据偏移+保留+FLAGS，为头部长度32字节的TCP SYN包
	pkt = append(pkt, 0x80, 0x02)
	// 窗口大小 64240
	pkt = append(pkt, 0xfa, 0xf0)
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 紧急指针
	pkt = append(pkt, 0x00, 0x00)
	// 常规TCP SYN选项数据 MSS1460+WS256+SACK
	pkt = append(pkt, 0x02, 0x04, 0x05, 0xb4, 0x01, 0x03, 0x03, 0x08, 0x01, 0x01, 0x04, 0x02)
	// 计算校验和
	fakeHeader := make([]byte, 0)
	fakeHeader = append(fakeHeader, srcAddr...)
	fakeHeader = append(fakeHeader, dstAddr...)
	// 保留字节0x00+TCP协议号0x06
	fakeHeader = append(fakeHeader, 0x00, 0x06)
	// TCP报文总长度
	fakeHeader = append(fakeHeader, 0x00, 0x20)
	checkSumSrc := make([]byte, 0)
	checkSumSrc = append(checkSumSrc, fakeHeader...)
	checkSumSrc = append(checkSumSrc, pkt...)
	sum := GetCheckSum(checkSumSrc)
	pkt[16] = sum[0]
	pkt[17] = sum[1]
	return pkt, nil
}

func BuildTcpSynAckPkt(srcPort uint16, dstPort uint16, srcAddr []byte, dstAddr []byte, seqNum uint32, ackNum uint32) (pkt []byte, err error) {
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
	// 数据偏移+保留+FLAGS，为头部长度32字节的TCP SYN ACK包
	pkt = append(pkt, 0x80, 0x12)
	// 窗口大小 64240
	pkt = append(pkt, 0xfa, 0xf0)
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 紧急指针
	pkt = append(pkt, 0x00, 0x00)
	// 常规TCP SYN ACK选项数据 MSS1460+WS256+SACK
	pkt = append(pkt, 0x02, 0x04, 0x05, 0xb4, 0x01, 0x03, 0x03, 0x08, 0x01, 0x01, 0x04, 0x02)
	// 计算校验和
	fakeHeader := make([]byte, 0)
	fakeHeader = append(fakeHeader, srcAddr...)
	fakeHeader = append(fakeHeader, dstAddr...)
	// 保留字节0x00+TCP协议号0x06
	fakeHeader = append(fakeHeader, 0x00, 0x06)
	// TCP报文总长度
	fakeHeader = append(fakeHeader, 0x00, 0x20)
	checkSumSrc := make([]byte, 0)
	checkSumSrc = append(checkSumSrc, fakeHeader...)
	checkSumSrc = append(checkSumSrc, pkt...)
	sum := GetCheckSum(checkSumSrc)
	pkt[16] = sum[0]
	pkt[17] = sum[1]
	return pkt, nil
}

func BuildTcpAckPkt(srcPort uint16, dstPort uint16, srcAddr []byte, dstAddr []byte, seqNum uint32, ackNum uint32) (pkt []byte, err error) {
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
	// 数据偏移+保留+FLAGS，为头部长度20字节的TCP ACK包
	pkt = append(pkt, 0x05, 0x10)
	// 窗口大小 1028
	pkt = append(pkt, 0x04, 0x04)
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 紧急指针
	pkt = append(pkt, 0x00, 0x00)
	// 计算校验和
	fakeHeader := make([]byte, 0)
	fakeHeader = append(fakeHeader, srcAddr...)
	fakeHeader = append(fakeHeader, dstAddr...)
	// 保留字节0x00+TCP协议号0x06
	fakeHeader = append(fakeHeader, 0x00, 0x06)
	// TCP报文总长度
	fakeHeader = append(fakeHeader, 0x00, 0x14)
	checkSumSrc := make([]byte, 0)
	checkSumSrc = append(checkSumSrc, fakeHeader...)
	checkSumSrc = append(checkSumSrc, pkt...)
	sum := GetCheckSum(checkSumSrc)
	pkt[16] = sum[0]
	pkt[17] = sum[1]
	return pkt, nil
}
