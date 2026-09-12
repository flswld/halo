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

// TcpPkt 保存 TCP 报文的编解码字段
// Parse 返回的切片引用输入缓冲区 使用期间不得复用该缓冲区
type TcpPkt struct {
	Payload []byte // TCP 载荷
	SrcPort uint16 // 源端口
	DstPort uint16 // 目的端口
	SeqNum  uint32 // 序号
	AckNum  uint32 // 确认号
	Flags   uint8  // TCP 标志位
}

// ParseTcpPkt 解析 TCP 报文并按配置验证校验和
func ParseTcpPkt(pkt []byte, addr Ipv4AddrPair) (result TcpPkt, err error) {
	if len(pkt) < 20 || len(pkt) > 1480 {
		return TcpPkt{}, errors.New("tcp packet len must >= 20 and <= 1480 bytes")
	}
	// 源端口
	result.SrcPort = binary.BigEndian.Uint16([]byte{pkt[0], pkt[1]})
	// 目标端口
	result.DstPort = binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]})
	// 序号
	result.SeqNum = binary.BigEndian.Uint32([]byte{pkt[4], pkt[5], pkt[6], pkt[7]})
	// 确认号
	result.AckNum = binary.BigEndian.Uint32([]byte{pkt[8], pkt[9], pkt[10], pkt[11]})
	// 数据偏移以 4 字节为单位 包含固定头部和可能的选项
	headerLen := int(pkt[12]>>4) * 4
	if headerLen < 20 || headerLen > len(pkt) {
		return TcpPkt{}, errors.New("invalid tcp header length")
	}
	result.Flags = pkt[13]
	// 检查校验和
	if CheckSumEnable {
		// TCP 校验和覆盖 IPv4 伪首部和完整 TCP 报文
		totalLen := len(pkt)
		fakeHeader := make([]byte, 0, 12)
		fakeHeader = append(fakeHeader, addr.SrcAddr[:]...)
		fakeHeader = append(fakeHeader, addr.DstAddr[:]...)
		fakeHeader = append(fakeHeader, 0x00, 0x06)
		fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
		sumData := make([]byte, 0, 12+1500)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt...)
		if GetCheckSum(sumData) != 0 {
			return TcpPkt{}, errors.New("check sum error")
		}
	}
	// 数据
	result.Payload = pkt[headerLen:]
	return result, nil
}

// BuildTcpPkt 构建固定 20 字节头部的 TCP 报文
// pkt 应为 nil 或长度为 0 的可复用缓冲区 返回值持有构建后的报文字节
func BuildTcpPkt(pkt []byte, packet TcpPkt, addr Ipv4AddrPair) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 20)
	}
	if len(packet.Payload) > 1460 {
		return nil, errors.New("payload len must <= 1460")
	}
	// 源端口
	pkt = append(pkt, byte(packet.SrcPort>>8), byte(packet.SrcPort))
	// 目的端口
	pkt = append(pkt, byte(packet.DstPort>>8), byte(packet.DstPort))
	// 序号
	pkt = append(pkt, byte(packet.SeqNum>>24), byte(packet.SeqNum>>16), byte(packet.SeqNum>>8), byte(packet.SeqNum))
	// 确认号
	pkt = append(pkt, byte(packet.AckNum>>24), byte(packet.AckNum>>16), byte(packet.AckNum>>8), byte(packet.AckNum))
	// 数据偏移+保留+FLAGS为头部长度20字节的TCP包
	pkt = append(pkt, 0x50, packet.Flags)
	// 窗口大小 256
	pkt = append(pkt, 0x01, 0x00)
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 紧急指针
	pkt = append(pkt, 0x00, 0x00)
	// 数据
	pkt = append(pkt, packet.Payload...)
	// 计算校验和
	if CheckSumEnable {
		// IPv4 伪首部参与校验但不会写入实际报文
		fakeHeader := make([]byte, 0, 12)
		fakeHeader = append(fakeHeader, addr.SrcAddr[:]...)
		fakeHeader = append(fakeHeader, addr.DstAddr[:]...)
		// 保留字节0x00+TCP协议号0x06
		fakeHeader = append(fakeHeader, 0x00, 0x06)
		// TCP报文总长度
		totalLen := 20 + len(packet.Payload)
		fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
		sumData := make([]byte, 0, 12+1500)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt...)
		sum := GetCheckSum(sumData)
		pkt[16] = byte(sum >> 8)
		pkt[17] = byte(sum)
	} else {
		pkt[16] = 0x00
		pkt[17] = 0x00
	}
	return pkt, nil
}
