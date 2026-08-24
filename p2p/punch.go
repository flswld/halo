package p2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"syscall"
	"time"
)

// punchPacketSize 表示打洞控制报文的固定字节数
const punchPacketSize = 15

// punchPacketType 表示打洞控制报文类型
type punchPacketType uint8

const (
	// punchCheck 标识路径探测请求
	punchCheck punchPacketType = 1
	// punchCheckAck 标识路径探测响应
	punchCheckAck punchPacketType = 2
	// punchConfirm 标识路径确认请求
	punchConfirm punchPacketType = 3
	// punchConfirmAck 标识路径确认响应
	punchConfirmAck punchPacketType = 4
)

// punchPacket 保存解析后的打洞控制字段
type punchPacket struct {
	packetType    punchPacketType // 打洞控制报文类型
	conv          uint64          // 双方约定并可直接交给 KCP 的会话标识
	transactionID uint32          // 单次探测与确认事务的标识
}

const (
	// defaultCandidatePortCount 表示 IPv4 默认递增探测的候选端口数量
	defaultCandidatePortCount = 5
	// defaultBurstCount 表示默认 CHECK 探测批次数量
	defaultBurstCount = 8
	// maxInboundTransactions 限制单轮保存的入站事务数量
	maxInboundTransactions = 128
)

const (
	// defaultBurstInterval 表示默认 CHECK 批次间隔
	defaultBurstInterval = 10 * time.Millisecond
	// defaultPunchTimeout 表示默认单轮打洞超时时间
	defaultPunchTimeout = 5 * time.Second
	// defaultConfirmInterval 表示默认 CONFIRM 重发间隔
	defaultConfirmInterval = 10 * time.Millisecond
	// defaultSuccessLinger 表示双方确认完成后的默认收尾等待时间
	defaultSuccessLinger = 30 * time.Millisecond
)

// ErrPunchTimeout 表示当前单轮打洞未在固定时间内完成
var ErrPunchTimeout = errors.New("p2p: punch timeout")

// punchConfig 保存内部打洞时序参数并允许测试缩短等待时间
type punchConfig struct {
	candidatePortCount int           // IPv4 从信令端口开始递增探测的端口数量
	burstCount         int           // CHECK 探测批次数量
	burstInterval      time.Duration // 相邻 CHECK 批次间隔
	timeout            time.Duration // 单轮打洞最长等待时间
	confirmInterval    time.Duration // CONFIRM 重发间隔
	successLinger      time.Duration // 双方确认完成后继续响应重复 CONFIRM 的时间
}

var defaultPunchConfig = punchConfig{
	candidatePortCount: defaultCandidatePortCount,
	burstCount:         defaultBurstCount,
	burstInterval:      defaultBurstInterval,
	timeout:            defaultPunchTimeout,
	confirmInterval:    defaultConfirmInterval,
	successLinger:      defaultSuccessLinger,
}

// Punch 使用调用方提供的 PacketConn 执行一轮 IPv4 或 IPv6 对称 UDP 打洞
func Punch(ctx context.Context, conn net.PacketConn, conv uint64, remote *net.UDPAddr) (*net.UDPAddr, error) {
	return punch(ctx, conn, conv, remote, defaultPunchConfig)
}

// punch 使用指定内部参数执行一轮打洞并供快速测试缩短时序
func punch(ctx context.Context, conn net.PacketConn, conv uint64, remote *net.UDPAddr, config punchConfig) (endpoint *net.UDPAddr, err error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if conn == nil {
		return nil, errors.New("p2p: punch connection is nil")
	}
	remote, err = normalizePunchAddr(remote)
	if err != nil {
		return nil, fmt.Errorf("p2p: invalid remote endpoint: %w", err)
	}
	defer func() {
		resetErr := conn.SetReadDeadline(time.Time{})
		if err == nil && resetErr != nil {
			endpoint = nil
			err = fmt.Errorf("p2p: reset punch read deadline: %w", resetErr)
		}
	}()

	candidatePortCount := config.candidatePortCount
	if remote.IP.To4() == nil {
		candidatePortCount = 1
	}
	candidates := make([]*net.UDPAddr, 0, candidatePortCount)
	for offset := 0; offset < candidatePortCount && remote.Port+offset <= 65535; offset++ {
		candidates = append(candidates, &net.UDPAddr{
			IP:   remote.IP,
			Port: remote.Port + offset,
			Zone: remote.Zone,
		})
	}
	nextTransactionID := rand.Uint32()

	outboundTransactions := make(map[uint32]struct{}, len(candidates)*config.burstCount)
	inboundTransactions := make(map[uint32]string, len(candidates)*config.burstCount)
	buffer := make([]byte, 64)
	nextBurst := time.Now()
	hardDeadline := nextBurst.Add(config.timeout)
	burst := 0

	var selectedTransactionID uint32
	var selectedRemote *net.UDPAddr
	var nextConfirm time.Time
	var successDeadline time.Time
	localConfirmed := false
	peerConfirmed := false

	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		now := time.Now()
		if !successDeadline.IsZero() && !now.Before(successDeadline) {
			return selectedRemote, nil
		}
		if !now.Before(hardDeadline) {
			return nil, ErrPunchTimeout
		}

		if burst < config.burstCount && !now.Before(nextBurst) {
			for _, candidate := range candidates {
				transactionID := nextTransactionID
				nextTransactionID++
				outboundTransactions[transactionID] = struct{}{}
				if _, writeErr := conn.WriteTo(buildPunchPacket(punchCheck, conv, transactionID), candidate); writeErr != nil {
					return nil, fmt.Errorf("p2p: write CHECK: %w", writeErr)
				}
			}
			burst++
			nextBurst = time.Now().Add(config.burstInterval)
			now = time.Now()
		}

		if selectedRemote != nil && !localConfirmed && !now.Before(nextConfirm) {
			if _, writeErr := conn.WriteTo(buildPunchPacket(punchConfirm, conv, selectedTransactionID), selectedRemote); writeErr != nil {
				return nil, fmt.Errorf("p2p: write CONFIRM: %w", writeErr)
			}
			nextConfirm = time.Now().Add(config.confirmInterval)
		}

		readDeadline := nextReadDeadline(ctx, hardDeadline)
		if burst < config.burstCount && nextBurst.Before(readDeadline) {
			readDeadline = nextBurst
		}
		if selectedRemote != nil && !localConfirmed && nextConfirm.Before(readDeadline) {
			readDeadline = nextConfirm
		}
		if !successDeadline.IsZero() && successDeadline.Before(readDeadline) {
			readDeadline = successDeadline
		}
		if setErr := conn.SetReadDeadline(readDeadline); setErr != nil {
			return nil, fmt.Errorf("p2p: set punch read deadline: %w", setErr)
		}

		n, source, readErr := conn.ReadFrom(buffer)
		if readErr != nil {
			if isTimeoutError(readErr) || errors.Is(readErr, syscall.ECONNREFUSED) || errors.Is(readErr, syscall.ECONNRESET) {
				continue
			}
			return nil, fmt.Errorf("p2p: read punch packet: %w", readErr)
		}
		sourceUDP := source.(*net.UDPAddr)
		if !sourceUDP.IP.Equal(remote.IP) {
			continue
		}
		control, parseErr := parsePunchPacket(buffer[:n])
		if parseErr != nil || control.conv != conv {
			continue
		}
		sourceEndpoint := sourceUDP.String()

		switch control.packetType {
		case punchCheck:
			// 丢弃候选窗口命中自身端口后反射回来的本端事务
			if _, reflected := outboundTransactions[control.transactionID]; reflected {
				continue
			}
			transactionSource, exists := inboundTransactions[control.transactionID]
			if exists && transactionSource != sourceEndpoint {
				continue
			}
			if !exists {
				if len(inboundTransactions) >= maxInboundTransactions {
					continue
				}
				inboundTransactions[control.transactionID] = sourceEndpoint
			}
			if _, writeErr := conn.WriteTo(buildPunchPacket(punchCheckAck, conv, control.transactionID), sourceUDP); writeErr != nil {
				return nil, fmt.Errorf("p2p: write CHECK_ACK: %w", writeErr)
			}

		case punchCheckAck:
			if selectedRemote != nil {
				continue
			}
			if _, exists := outboundTransactions[control.transactionID]; !exists {
				continue
			}
			selectedTransactionID = control.transactionID
			selectedRemote = sourceUDP
			if _, writeErr := conn.WriteTo(buildPunchPacket(punchConfirm, conv, selectedTransactionID), selectedRemote); writeErr != nil {
				return nil, fmt.Errorf("p2p: write CONFIRM: %w", writeErr)
			}
			nextConfirm = time.Now().Add(config.confirmInterval)

		case punchConfirm:
			transactionSource, exists := inboundTransactions[control.transactionID]
			if !exists || transactionSource != sourceEndpoint {
				continue
			}
			if _, writeErr := conn.WriteTo(buildPunchPacket(punchConfirmAck, conv, control.transactionID), sourceUDP); writeErr != nil {
				return nil, fmt.Errorf("p2p: write CONFIRM_ACK: %w", writeErr)
			}
			peerConfirmed = true

		case punchConfirmAck:
			if selectedRemote == nil || control.transactionID != selectedTransactionID || selectedRemote.String() != sourceEndpoint {
				continue
			}
			localConfirmed = true
		}

		if localConfirmed && peerConfirmed && successDeadline.IsZero() {
			successDeadline = time.Now().Add(config.successLinger)
			if hardDeadline.Before(successDeadline) {
				successDeadline = hardDeadline
			}
		}
	}
}

// buildPunchPacket 构建固定长度的打洞控制报文
func buildPunchPacket(packetType punchPacketType, conv uint64, transactionID uint32) []byte {
	packet := make([]byte, punchPacketSize)
	packet[0] = 'H'
	packet[1] = 'P'
	packet[2] = byte(packetType)
	binary.BigEndian.PutUint64(packet[3:11], conv)
	binary.BigEndian.PutUint32(packet[11:15], transactionID)
	return packet
}

// parsePunchPacket 解析并严格校验打洞控制报文
func parsePunchPacket(packet []byte) (punchPacket, error) {
	var result punchPacket
	if len(packet) != punchPacketSize || packet[0] != 'H' || packet[1] != 'P' {
		return result, errInvalidPacket
	}

	result.packetType = punchPacketType(packet[2])
	switch result.packetType {
	case punchCheck, punchCheckAck, punchConfirm, punchConfirmAck:
	default:
		return punchPacket{}, errInvalidPacket
	}

	result.conv = binary.BigEndian.Uint64(packet[3:11])
	result.transactionID = binary.BigEndian.Uint32(packet[11:15])
	return result, nil
}

// normalizePunchAddr 复制并规范化 IPv4 或 IPv6 UDP 端点
func normalizePunchAddr(addr *net.UDPAddr) (*net.UDPAddr, error) {
	if addr == nil {
		return nil, errors.New("p2p: udp address is nil")
	}
	if addr.Port <= 0 || addr.Port > 65535 {
		return nil, errors.New("p2p: UDP port must be between 1 and 65535")
	}

	ip := addr.IP.To4()
	zone := ""
	if ip == nil {
		ip = addr.IP.To16()
		zone = addr.Zone
	}
	if ip == nil {
		return nil, errors.New("p2p: invalid IP address")
	}
	return &net.UDPAddr{IP: append(net.IP(nil), ip...), Port: addr.Port, Zone: zone}, nil
}
