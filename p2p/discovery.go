package p2p

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
)

const (
	// contextPollInterval 控制无 deadline context 的取消轮询间隔
	contextPollInterval = 100 * time.Millisecond
	// discoveryRetryInterval 控制发现请求的重发间隔
	discoveryRetryInterval = 500 * time.Millisecond
	// discoveryRequestSize 表示发现请求的固定字节数
	discoveryRequestSize = 7
	// discoveryIPv4ResponseSize 表示 IPv4 发现响应的固定字节数
	discoveryIPv4ResponseSize = 13
	// discoveryIPv6ResponseSize 表示 IPv6 发现响应的固定字节数
	discoveryIPv6ResponseSize = 25
	// discoveryRequestType 标识发现请求报文
	discoveryRequestType uint8 = 1
	// discoveryResponseType 标识发现响应报文
	discoveryResponseType uint8 = 2
)

var (
	errNilContext    = errors.New("p2p: context is nil")
	errInvalidPacket = errors.New("p2p: invalid packet")
)

// discoveryPacket 保存解析后的公网端点发现控制字段
type discoveryPacket struct {
	packetType    uint8  // 报文类型
	transactionID uint32 // 请求与响应共用的事务标识
	publicIP      net.IP // 发现服务器观察到的公网 IPv4 或 IPv6
	publicPort    uint16 // 发现服务器观察到的公网端口
}

// ServeDiscovery 在调用方提供的 IPv4 或 IPv6 PacketConn 上运行无状态公网端点发现服务
func ServeDiscovery(ctx context.Context, conn net.PacketConn) (err error) {
	if ctx == nil {
		return errNilContext
	}
	if conn == nil {
		return fmt.Errorf("p2p: discovery connection is nil")
	}
	defer func() {
		resetErr := conn.SetReadDeadline(time.Time{})
		if err == nil && resetErr != nil {
			err = fmt.Errorf("p2p: reset discovery read deadline: %w", resetErr)
		}
	}()

	buffer := make([]byte, 64)
	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if setErr := conn.SetReadDeadline(nextReadDeadline(ctx)); setErr != nil {
			return fmt.Errorf("p2p: set discovery read deadline: %w", setErr)
		}

		n, source, readErr := conn.ReadFrom(buffer)
		if readErr != nil {
			if isTimeoutError(readErr) {
				continue
			}
			return fmt.Errorf("p2p: read discovery request: %w", readErr)
		}

		request, parseErr := parseDiscoveryPacket(buffer[:n])
		if parseErr != nil || request.packetType != discoveryRequestType {
			continue
		}
		sourceUDP := source.(*net.UDPAddr)

		response := buildDiscoveryResponse(request.transactionID, sourceUDP.IP, uint16(sourceUDP.Port))
		if response == nil {
			continue
		}
		if _, writeErr := conn.WriteTo(response, source); writeErr != nil {
			return fmt.Errorf("p2p: write discovery response: %w", writeErr)
		}
	}
}

// DiscoverEndpoint 使用调用方提供的 IPv4 或 IPv6 PacketConn 获取服务器观察到的公网端点
func DiscoverEndpoint(ctx context.Context, conn net.PacketConn, server *net.UDPAddr) (endpoint *net.UDPAddr, err error) {
	if ctx == nil {
		return nil, errNilContext
	}
	if conn == nil {
		return nil, fmt.Errorf("p2p: discovery connection is nil")
	}
	server, err = normalizeDiscoveryAddr(server)
	if err != nil {
		return nil, fmt.Errorf("p2p: invalid discovery server: %w", err)
	}
	defer func() {
		resetErr := conn.SetReadDeadline(time.Time{})
		if err == nil && resetErr != nil {
			endpoint = nil
			err = fmt.Errorf("p2p: reset discovery read deadline: %w", resetErr)
		}
	}()

	transactionID := rand.Uint32()
	request := buildDiscoveryRequest(transactionID)
	buffer := make([]byte, 64)
	nextSend := time.Now()

	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		now := time.Now()
		if !now.Before(nextSend) {
			if _, writeErr := conn.WriteTo(request, server); writeErr != nil {
				return nil, fmt.Errorf("p2p: write discovery request: %w", writeErr)
			}
			nextSend = now.Add(discoveryRetryInterval)
		}

		if setErr := conn.SetReadDeadline(nextReadDeadline(ctx, nextSend)); setErr != nil {
			return nil, fmt.Errorf("p2p: set discovery read deadline: %w", setErr)
		}
		n, source, readErr := conn.ReadFrom(buffer)
		if readErr != nil {
			if isTimeoutError(readErr) {
				continue
			}
			return nil, fmt.Errorf("p2p: read discovery response: %w", readErr)
		}

		if source.String() != server.String() {
			continue
		}
		response, parseErr := parseDiscoveryPacket(buffer[:n])
		if parseErr != nil || response.packetType != discoveryResponseType || response.transactionID != transactionID || response.publicPort == 0 {
			continue
		}
		return &net.UDPAddr{
			IP:   response.publicIP,
			Port: int(response.publicPort),
		}, nil
	}
}

// buildDiscoveryRequest 构建固定长度的公网端点发现请求
func buildDiscoveryRequest(transactionID uint32) []byte {
	packet := make([]byte, discoveryRequestSize)
	packet[0] = 'H'
	packet[1] = 'D'
	packet[2] = discoveryRequestType
	binary.BigEndian.PutUint32(packet[3:7], transactionID)
	return packet
}

// buildDiscoveryResponse 根据地址族构建公网端点发现响应
func buildDiscoveryResponse(transactionID uint32, publicIP net.IP, publicPort uint16) []byte {
	address := publicIP.To4()
	packetSize := discoveryIPv4ResponseSize
	if address == nil {
		address = publicIP.To16()
		packetSize = discoveryIPv6ResponseSize
	}
	if address == nil {
		return nil
	}
	packet := make([]byte, packetSize)
	packet[0] = 'H'
	packet[1] = 'D'
	packet[2] = discoveryResponseType
	binary.BigEndian.PutUint32(packet[3:7], transactionID)
	copy(packet[7:packetSize-2], address)
	binary.BigEndian.PutUint16(packet[packetSize-2:], publicPort)
	return packet
}

// parseDiscoveryPacket 解析并严格校验公网端点发现报文
func parseDiscoveryPacket(packet []byte) (discoveryPacket, error) {
	var result discoveryPacket
	if len(packet) < 3 || packet[0] != 'H' || packet[1] != 'D' {
		return result, errInvalidPacket
	}

	result.packetType = packet[2]
	switch result.packetType {
	case discoveryRequestType:
		if len(packet) != discoveryRequestSize {
			return discoveryPacket{}, errInvalidPacket
		}
	case discoveryResponseType:
		if len(packet) != discoveryIPv4ResponseSize && len(packet) != discoveryIPv6ResponseSize {
			return discoveryPacket{}, errInvalidPacket
		}
	default:
		return discoveryPacket{}, errInvalidPacket
	}

	result.transactionID = binary.BigEndian.Uint32(packet[3:7])
	if result.packetType == discoveryResponseType {
		addressEnd := len(packet) - 2
		result.publicIP = append(net.IP(nil), packet[7:addressEnd]...)
		result.publicPort = binary.BigEndian.Uint16(packet[addressEnd:])
	}
	return result, nil
}

// normalizeDiscoveryAddr 复制并规范化 IPv4 或 IPv6 发现服务器端点
func normalizeDiscoveryAddr(addr *net.UDPAddr) (*net.UDPAddr, error) {
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

// nextReadDeadline 选择下一次事件或 context 轮询之前的最近时间
func nextReadDeadline(ctx context.Context, deadlines ...time.Time) time.Time {
	deadline := time.Now().Add(contextPollInterval)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	for _, candidate := range deadlines {
		if candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline
}

// isTimeoutError 判断 PacketConn 错误是否由 deadline 到期产生
func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
