package kcp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// Enet连接控制协议
// MM MM MM MM | SS SS SS SS | CC CC CC CC | EE EE EE EE | MM MM MM MM
// MM为表示连接状态的幻数 在开头的4字节和结尾的4字节
// SS为sessionId 4字节
// CC为conv 4字节
// EE为Enet事件类型 4字节

// Enet Enet协议上报结构体
type Enet struct {
	Addr      string
	SessionId uint32
	Conv      uint32
	ConnType  string
	EnetType  uint32
}

// Enet连接状态类型
const (
	ConnEnetSyn  = "ConnEnetSyn"  // 客户端前置握手获取conv
	ConnEnetEst  = "ConnEnetEst"  // 连接建立
	ConnEnetFin  = "ConnEnetFin"  // 连接断开
	ConnEnetPing = "ConnEnetPing" // 网络检查
)

// Enet连接状态类型幻数
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

// Enet事件类型
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

func BuildEnet(connType string, enetType uint32, sessionId uint32, conv uint32) []byte {
	data := make([]byte, 20)
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

func ParseEnet(data []byte) (connType string, enetType uint32, sessionId uint32, conv uint32, rawConv uint64, err error) {
	sessionId = binary.BigEndian.Uint32(data[4:8])
	conv = binary.BigEndian.Uint32(data[8:12])
	rawConv = binary.LittleEndian.Uint64(data[4:12])
	// 提取Enet协议头部和尾部幻数
	udpPayloadEnetHead := data[:4]
	udpPayloadEnetTail := data[len(data)-4:]
	// 提取Enet协议类型
	enetTypeData := data[12:16]
	enetTypeDataBuffer := bytes.NewBuffer(enetTypeData)
	enetType = uint32(0)
	_ = binary.Read(enetTypeDataBuffer, binary.BigEndian, &enetType)

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
