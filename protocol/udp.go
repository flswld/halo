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

// UdpPkt 保存 UDP 报文的编解码字段
// Parse 返回的切片引用输入缓冲区 使用期间不得复用该缓冲区
type UdpPkt struct {
	Payload []byte // UDP 载荷
	SrcPort uint16 // 源端口
	DstPort uint16 // 目的端口
}

// ParseUdpPkt 解析 UDP 报文并按配置验证校验和
func ParseUdpPkt(pkt []byte, addr Ipv4AddrPair) (result UdpPkt, err error) {
	if len(pkt) < 8 || len(pkt) > 1480 {
		return UdpPkt{}, errors.New("udp packet len must >= 8 and <= 1480 bytes")
	}
	// 源端口
	result.SrcPort = binary.BigEndian.Uint16([]byte{pkt[0], pkt[1]})
	// 目标端口
	result.DstPort = binary.BigEndian.Uint16([]byte{pkt[2], pkt[3]})
	// 总长度
	totalLen := int(binary.BigEndian.Uint16([]byte{pkt[4], pkt[5]}))
	if totalLen < 8 || totalLen > len(pkt) {
		return UdpPkt{}, errors.New("invalid udp total length")
	}
	// 检查校验和
	if CheckSumEnable {
		// UDP 校验和覆盖 IPv4 伪首部和完整 UDP 报文
		fakeHeader := make([]byte, 0, 12)
		fakeHeader = append(fakeHeader, addr.SrcAddr[:]...)
		fakeHeader = append(fakeHeader, addr.DstAddr[:]...)
		fakeHeader = append(fakeHeader, 0x00, 0x11)
		fakeHeader = append(fakeHeader, byte(totalLen>>8), byte(totalLen))
		sumData := make([]byte, 0, 12+1500)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt[:totalLen]...)
		if GetCheckSum(sumData) != 0 {
			return UdpPkt{}, errors.New("check sum error")
		}
	}
	// 数据
	result.Payload = pkt[8:totalLen]
	return result, nil
}

// BuildUdpPkt 构建 UDP 报文并按配置计算校验和
// pkt 应为 nil 或长度为 0 的可复用缓冲区 返回值持有构建后的报文字节
func BuildUdpPkt(pkt []byte, packet UdpPkt, addr Ipv4AddrPair) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 8)
	}
	if len(packet.Payload) > 1472 {
		return nil, errors.New("payload len must <= 1472")
	}
	// 源端口
	pkt = append(pkt, byte(packet.SrcPort>>8), byte(packet.SrcPort))
	// 目标端口
	pkt = append(pkt, byte(packet.DstPort>>8), byte(packet.DstPort))
	// 总长度
	udpPktLen := uint16(len(packet.Payload) + 8)
	pkt = append(pkt, byte(udpPktLen>>8), byte(udpPktLen))
	// 校验和(填充零)
	pkt = append(pkt, 0x00, 0x00)
	// 上层数据
	pkt = append(pkt, packet.Payload...)
	// 计算校验和
	if CheckSumEnable {
		// IPv4 伪首部参与校验但不会写入实际报文
		fakeHeader := make([]byte, 0, 12)
		fakeHeader = append(fakeHeader, addr.SrcAddr[:]...)
		fakeHeader = append(fakeHeader, addr.DstAddr[:]...)
		// 保留字节0x00+UDP协议号0x11
		fakeHeader = append(fakeHeader, 0x00, 0x11)
		// UDP报文总长度
		fakeHeader = append(fakeHeader, byte(udpPktLen>>8), byte(udpPktLen))
		sumData := make([]byte, 0, 12+1500)
		sumData = append(sumData, fakeHeader...)
		sumData = append(sumData, pkt...)
		sum := GetCheckSum(sumData)
		pkt[6] = byte(sum >> 8)
		pkt[7] = byte(sum)
	} else {
		pkt[6] = 0x00
		pkt[7] = 0x00
	}
	return pkt, nil
}
