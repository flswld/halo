package protocol

import (
	"bytes"
	"errors"
)

const (
	DhcpClientPort = 68 // DHCP 客户端 UDP 端口
	DhcpServerPort = 67 // DHCP 服务端 UDP 端口

	DhcpOptionSubnetMask       = 1  // 子网掩码选项
	DhcpOptionRouter           = 3  // 网关选项
	DhcpOptionDomainNameServer = 6  // DNS 服务器选项
	DhcpOptionHostName         = 12 // 主机名选项
	DhcpOptionReqIpAddr        = 50 // 请求地址选项
	DhcpOptionIpAddrLeaseTime  = 51 // 租期选项

	DhcpOptionMsgType         = 53 // 消息类型选项
	DhcpOptionMsgTypeDiscover = 1  // 地址发现消息
	DhcpOptionMsgTypeOffer    = 2  // 地址提供消息
	DhcpOptionMsgTypeRequest  = 3  // 地址请求消息
	DhcpOptionMsgTypeAck      = 5  // 确认消息
	DhcpOptionMsgTypeNak      = 6  // 拒绝消息
	DhcpOptionMsgTypeRelease  = 7  // 释放租约消息

	DhcpOptionServerIdentifier     = 54 // 服务器标识选项
	DhcpOptionParameterRequestList = 55 // 参数请求列表选项
	DhcpOptionRenewalTimeValue     = 58 // 续租时间选项
	DhcpOptionRebindingTimeValue   = 59 // 重绑定时间选项
	DhcpOptionClientIdentifier     = 61 // 客户端标识选项

	DhcpBootMsgTypeRequest = 1 // BOOTP 请求类型
	DhcpBootMsgTypeReply   = 2 // BOOTP 响应类型
)

var (
	DhcpMagicCookie = []byte{0x63, 0x82, 0x53, 0x63} // DHCP 选项区魔术字
)

// DhcpOption 表示一个 DHCP 选项及其解析值
type DhcpOption struct {
	Type         uint8    // 选项类型
	MsgType      uint8    // DHCP 消息类型
	IpAddr       Ipv4Addr // 解析的地址或构包时的请求地址
	SubnetMask   Ipv4Addr // 子网掩码
	ServerIpAddr Ipv4Addr // 构包时的网关 DNS 或服务器标识地址
	TimeValue    uint32   // 时间值
	HostName     string   // 主机名
	MacAddr      MacAddr  // MAC 地址
}

// ParseDhcpOption 解析 DHCP 选项数据
// 未知或长度非法的选项被忽略 截断时返回此前已解析的选项
func ParseDhcpOption(optionData []byte) map[uint8]*DhcpOption {
	dhcpOptionMap := make(map[uint8]*DhcpOption)
	i := 0
	for i < len(optionData) {
		// 每个选项由类型 长度和值组成 结束标记不带长度字段
		if optionData[i] == 0xff {
			break
		}
		if optionData[i] == 0 {
			i++
			continue
		}
		if i+1 >= len(optionData) {
			break
		}
		code := optionData[i]
		length := int(optionData[i+1])
		if i+2+length > len(optionData) {
			break
		}
		data := optionData[i+2 : i+2+length]
		switch code {
		case DhcpOptionSubnetMask:
			if len(data) != 4 {
				break
			}
			dhcpOptionMap[code] = &DhcpOption{
				Type:       code,
				SubnetMask: Ipv4Addr(data),
			}
		case DhcpOptionRouter:
			if len(data) < 4 || len(data)%4 != 0 {
				break
			}
			// 仅保留列表中的第一个网关
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: Ipv4Addr(data[:4]),
			}
		case DhcpOptionDomainNameServer:
			if len(data) < 4 || len(data)%4 != 0 {
				break
			}
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: Ipv4Addr(data[:4]),
			}
		case DhcpOptionHostName:
			dhcpOptionMap[code] = &DhcpOption{
				Type:     code,
				HostName: string(data),
			}
		case DhcpOptionReqIpAddr:
			if len(data) != 4 {
				break
			}
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: Ipv4Addr(data),
			}
		case DhcpOptionMsgType:
			if len(data) != 1 {
				break
			}
			dhcpOptionMap[code] = &DhcpOption{
				Type:    code,
				MsgType: data[0],
			}
		}
		i += 2 + length
	}
	return dhcpOptionMap
}

// BuildDhcpOption 编码 DHCP 选项数据
// 选项值必须非 nil 仅编码当前支持的选项 不保证保留输入选项顺序
func BuildDhcpOption(dhcpOptionMap map[uint8]*DhcpOption) []byte {
	optionData := make([]byte, 0)
	// 只编码当前支持的 DHCP 选项
	for _, dhcpOption := range dhcpOptionMap {
		switch dhcpOption.Type {
		case DhcpOptionSubnetMask:
			subnetMask := dhcpOption.SubnetMask
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, subnetMask[0], subnetMask[1], subnetMask[2], subnetMask[3]}...)
		case DhcpOptionRouter:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionDomainNameServer:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionReqIpAddr:
			ipAddr := dhcpOption.IpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, ipAddr[0], ipAddr[1], ipAddr[2], ipAddr[3]}...)
		case DhcpOptionIpAddrLeaseTime:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionMsgType:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x01, dhcpOption.MsgType}...)
		case DhcpOptionServerIdentifier:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionParameterRequestList:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x03, 0x01, 0x03, 0x06}...)
		case DhcpOptionRenewalTimeValue:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionRebindingTimeValue:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionClientIdentifier:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x07, 0x01}...)
			optionData = append(optionData, dhcpOption.MacAddr[:]...)
		default:
		}
	}
	optionData = append(optionData, 0xff)
	return optionData
}

// DhcpPkt 保存当前支持的 DHCP 报文字段
// TransactionId 引用输入缓冲区 地址和已解析选项独立保存
type DhcpPkt struct {
	BootMsgType   uint8                 // BOOTP 消息类型
	TransactionId []byte                // 四字节事务 ID
	YourIpAddr    Ipv4Addr              // 分配给客户端的 IPv4 地址
	ClientMacAddr MacAddr               // 客户端 MAC 地址
	OptionMap     map[uint8]*DhcpOption // 已识别的 DHCP 选项
}

// ParseDhcpPkt 解析 DHCP 报文的固定头部和选项
func ParseDhcpPkt(pkt []byte) (result DhcpPkt, err error) {
	if len(pkt) < 240 {
		return DhcpPkt{}, errors.New("dhcp packet len < 240 bytes")
	}
	if !bytes.Equal(pkt[236:240], DhcpMagicCookie) {
		return DhcpPkt{}, errors.New("dhcp magic cookie error")
	}
	result.BootMsgType = pkt[0]
	result.TransactionId = pkt[4:8]
	result.YourIpAddr = Ipv4Addr(pkt[16:20])
	result.ClientMacAddr = MacAddr(pkt[28:34])
	result.OptionMap = ParseDhcpOption(pkt[240:])
	return result, nil
}

// BuildDhcpPkt 构建 DHCP 报文
// pkt 应为 nil 或长度为 0 的可复用缓冲区 未暴露的固定字段保持原有默认值
func BuildDhcpPkt(pkt []byte, packet DhcpPkt) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 240)
	}
	if len(packet.TransactionId) != 4 {
		return nil, errors.New("transaction id len is not 4 bytes")
	}
	pkt = append(pkt, packet.BootMsgType, 0x01, 0x06, 0x00)
	pkt = append(pkt, packet.TransactionId...)
	pkt = append(pkt, 0x00, 0x00)
	pkt = append(pkt, 0x80, 0x00)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, packet.YourIpAddr[:]...)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, packet.ClientMacAddr[:]...)
	pkt = append(pkt, make([]byte, 10)...)
	pkt = append(pkt, make([]byte, 64)...)
	pkt = append(pkt, make([]byte, 128)...)
	pkt = append(pkt, DhcpMagicCookie...)
	optionData := BuildDhcpOption(packet.OptionMap)
	pkt = append(pkt, optionData...)
	return pkt, nil
}
