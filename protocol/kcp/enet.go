package kcp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
)

// Halo 扩展 Enet 连接控制协议
// MM MM MM MM | SS SS SS SS | CC CC CC CC | EE EE EE EE | MM MM MM MM
// MM 为表示连接状态的幻数 位于开头 4 字节和结尾 4 字节
// SS 为会话 ID 占 4 字节
// CC 为 KCP 会话号 占 4 字节
// EE 为 Enet 事件类型 占 4 字节

// Enet 保存 Halo 扩展的连接控制事件
type Enet struct {
	Addr      net.Addr // 对端地址
	SessionId uint32   // 服务端分配的会话 ID
	Conv      uint32   // KCP 会话号
	ConnType  string   // 连接控制类型
	EnetType  uint32   // 连接事件原因或校验键
}

// Enet 连接状态类型
const (
	ConnEnetSyn  = "ConnEnetSyn"  // 客户端前置握手获取会话号
	ConnEnetEst  = "ConnEnetEst"  // 连接建立
	ConnEnetFin  = "ConnEnetFin"  // 连接断开
	ConnEnetPing = "ConnEnetPing" // 网络检查
)

// Enet 连接状态类型幻数
var (
	MagicEnetSynHead, _  = hex.DecodeString("000000ff")
	MagicEnetSynTail, _  = hex.DecodeString("ffffffff")
	MagicEnetEstHead, _  = hex.DecodeString("00000145")
	MagicEnetEstTail, _  = hex.DecodeString("14514545")
	MagicEnetFinHead, _  = hex.DecodeString("00000194")
	MagicEnetFinTail, _  = hex.DecodeString("19419494")
	MagicEnetPingHead, _ = hex.DecodeString("00000227")
	MagicEnetPingTail, _ = hex.DecodeString("22722727")
)

// Enet 事件类型
const (
	EnetTimeout                = 0
	EnetClientClose            = 1
	EnetClientRebindFail       = 2
	EnetClientShutdown         = 3
	EnetServerRelogin          = 4
	EnetServerKick             = 5
	EnetServerShutdown         = 6
	EnetNotFoundSession        = 7
	EnetLoginUnfinished        = 8
	EnetPacketFreqTooHigh      = 9
	EnetPingTimeout            = 10
	EnetTransferFailed         = 11
	EnetServerKillClient       = 12
	EnetCheckMoveSpeed         = 13
	EnetAccountPasswordChange  = 14
	EnetSecurityKick           = 15
	EnetLuaShellTimeout        = 16
	EnetSDKFailKick            = 17
	EnetPacketCostTime         = 18
	EnetPacketUnionFreq        = 19
	EnetWaitSndMax             = 20
	EnetClientEditorConnectKey = 987654321
	EnetClientConnectKey       = 1234567890
)

// BuildEnet 构建固定 20 字节的 Enet 连接控制报文
func BuildEnet(connType string, enetType uint32, sessionId uint32, conv uint32) []byte {
	data := make([]byte, 20)
	// 首尾幻数共同标识控制类型 降低普通 KCP 数据被误判的概率
	if connType == ConnEnetSyn {
		copy(data[0:4], MagicEnetSynHead)
		copy(data[16:20], MagicEnetSynTail)
	} else if connType == ConnEnetEst {
		copy(data[0:4], MagicEnetEstHead)
		copy(data[16:20], MagicEnetEstTail)
	} else if connType == ConnEnetFin {
		copy(data[0:4], MagicEnetFinHead)
		copy(data[16:20], MagicEnetFinTail)
	} else if connType == ConnEnetPing {
		copy(data[0:4], MagicEnetPingHead)
		copy(data[16:20], MagicEnetPingTail)
	} else {
		return nil
	}
	binary.BigEndian.PutUint32(data[4:8], sessionId)
	binary.BigEndian.PutUint32(data[8:12], conv)
	binary.BigEndian.PutUint32(data[12:16], enetType)
	return data
}

// ParseEnet 解析固定 20 字节的 Enet 连接控制报文
func ParseEnet(data []byte) (connType string, enetType uint32, sessionId uint32, conv uint32, rawConv uint64, err error) {
	// 会话 ID 与 KCP 会话号在控制报文中分别使用大端编码
	sessionId = binary.BigEndian.Uint32(data[4:8])
	conv = binary.BigEndian.Uint32(data[8:12])
	// rawConv 保留组合字段的底层 64 位表示供兼容调用方使用
	rawConv = binary.LittleEndian.Uint64(data[4:12])
	// 提取 Enet 协议头部和尾部幻数
	udpPayloadEnetHead := data[:4]
	udpPayloadEnetTail := data[len(data)-4:]
	// 提取 Enet 协议类型
	enetTypeData := data[12:16]
	enetTypeDataBuffer := bytes.NewBuffer(enetTypeData)
	enetType = uint32(0)
	_ = binary.Read(enetTypeDataBuffer, binary.BigEndian, &enetType)

	// 仅首尾幻数同时匹配时才接受对应控制类型
	equalHead := bytes.Equal(udpPayloadEnetHead, MagicEnetSynHead)
	equalTail := bytes.Equal(udpPayloadEnetTail, MagicEnetSynTail)
	if equalHead && equalTail {
		connType = ConnEnetSyn
		return connType, enetType, sessionId, conv, rawConv, nil
	}

	equalHead = bytes.Equal(udpPayloadEnetHead, MagicEnetEstHead)
	equalTail = bytes.Equal(udpPayloadEnetTail, MagicEnetEstTail)
	if equalHead && equalTail {
		connType = ConnEnetEst
		return connType, enetType, sessionId, conv, rawConv, nil
	}

	equalHead = bytes.Equal(udpPayloadEnetHead, MagicEnetFinHead)
	equalTail = bytes.Equal(udpPayloadEnetTail, MagicEnetFinTail)
	if equalHead && equalTail {
		connType = ConnEnetFin
		return connType, enetType, sessionId, conv, rawConv, nil
	}

	equalHead = bytes.Equal(udpPayloadEnetHead, MagicEnetPingHead)
	equalTail = bytes.Equal(udpPayloadEnetTail, MagicEnetPingTail)
	if equalHead && equalTail {
		connType = ConnEnetPing
		return connType, enetType, sessionId, conv, rawConv, nil
	}

	return "", 0, 0, 0, 0, errors.New("unknown conn type")
}
