package tcp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/flswld/halo/protocol"
)

// ErrTimeout 表示握手或传输超时 连接不可继续使用
var ErrTimeout error = transportTimeout{}

type transportTimeout struct{}

func (transportTimeout) Error() string   { return "tcp: transport timeout" }
func (transportTimeout) Timeout() bool   { return true }
func (transportTimeout) Temporary() bool { return false }

// Addr 表示 IPv4 TCP 端点
type Addr struct {
	Ip   protocol.Ipv4Addr // IPv4 地址
	Port uint16            // TCP 端口
}

// Network 返回传输协议名称
func (a Addr) Network() string { return "tcp" }

// String 返回 IPv4 地址与端口
func (a Addr) String() string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", a.Ip[0], a.Ip[1], a.Ip[2], a.Ip[3], a.Port)
}

// Packet 保存 TCP 段及其 IPv4 地址
type Packet struct {
	Addr protocol.Ipv4AddrPair // 源地址和目的地址
	Data []byte                // 完整 TCP 段 不含 IPv4 头
}

// OutputFunc 发送 TCP 段 true 表示下层已接收 false 表示未发送
type OutputFunc func(Packet) bool

const (
	mss               = 1200 // 路径 MTU 须至少为 MSS+40
	sendBufferSize    = 16 * 1024
	receiveBufferSize = 16 * 1024
	maxConnections    = 1024 // 含半连接及 TIME_WAIT
	listenBacklog     = 128  // 每端口待接受连接数 含半连接
	handshakeTimeout  = 5 * time.Second
	userTimeout       = 2 * time.Minute // 待发送数据无进展的期限
	closeTimeout      = 30 * time.Second
	timeWaitDuration  = 4 * time.Minute
)

type connKey struct {
	local  Addr
	remote Addr
}

type outbound struct {
	packet Packet
	conn   *Conn
	end    uint32
	track  bool
}

// Stack 管理 TCP 连接和监听器 支持并发调用
type Stack struct {
	mu        sync.Mutex
	output    OutputFunc
	conns     map[connKey]*Conn
	listeners map[Addr]*Listener
	ports     map[Addr]int
	secret    [32]byte
	nextPort  uint16
	tx        []outbound
	wake      chan struct{}
	done      chan struct{}
	finished  chan struct{}
	closed    bool
}

// NewStack 创建协议栈并启动后台任务
func NewStack() (*Stack, error) {
	s := &Stack{conns: make(map[connKey]*Conn),
		listeners: make(map[Addr]*Listener), ports: make(map[Addr]int), wake: make(chan struct{}, 1),
		done: make(chan struct{}), finished: make(chan struct{})}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, err
	}
	s.nextPort = 49152 + binary.BigEndian.Uint16(s.secret[:2])%16384
	go s.run()
	return s, nil
}

// SetOutput 设置发送回调 nil 时按未发送处理
func (s *Stack) SetOutput(output OutputFunc) {
	s.mu.Lock()
	s.output = output
	s.mu.Unlock()
}

func unicast(ip protocol.Ipv4Addr) bool {
	return ip[0] != 0 && ip[0] < 224 && ip != protocol.BROADCAST_IPV4_ADDR
}

func (s *Stack) reserve(local Addr) bool {
	if s.ports[local] != 0 {
		return false
	}
	s.ports[local] = 1
	return true
}

func (s *Stack) release(local Addr) {
	s.ports[local]--
	if s.ports[local] == 0 {
		delete(s.ports, local)
	}
}

// Dial 从 local 建立到 remote 的连接 端口零表示自动分配 握手超时 5 秒
func (s *Stack) Dial(remote Addr, local Addr) (*Conn, error) {
	if !unicast(remote.Ip) || remote.Port == 0 {
		return nil, errors.New("tcp: invalid remote address")
	}
	if !unicast(local.Ip) {
		return nil, errors.New("tcp: invalid local address")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, net.ErrClosed
	}
	if len(s.conns) >= maxConnections {
		s.mu.Unlock()
		return nil, errors.New("tcp: connection limit reached")
	}
	if local.Port != 0 && !s.reserve(local) {
		s.mu.Unlock()
		return nil, errors.New("tcp: local port is in use")
	}
	for n := 0; local.Port == 0 && n < 16384; n++ {
		candidate := Addr{Ip: local.Ip, Port: s.nextPort}
		s.nextPort++
		if s.nextPort < 49152 {
			s.nextPort = 49152
		}
		if s.reserve(candidate) {
			local = candidate
			break
		}
	}
	if local.Port == 0 {
		s.mu.Unlock()
		return nil, errors.New("tcp: no available local port")
	}
	c := s.newConn(local, remote, time.Now())
	c.state = synSent
	c.sndNxt++
	c.emit(protocol.TCP_FLAGS_SYN, c.iss, nil, true)
	for c.state == synSent {
		event := c.event
		s.mu.Unlock()
		<-event
		s.mu.Lock()
	}
	err := c.err
	if c.state == closed && err == nil {
		err = net.ErrClosed
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Input 接收完整 TCP 段 返回后可复用 Data
func (s *Stack) Input(packet Packet) error {
	if !unicast(packet.Addr.SrcAddr) || !unicast(packet.Addr.DstAddr) {
		return errors.New("tcp: invalid packet address")
	}
	p, err := protocol.ParseTcpPkt(packet.Data, packet.Addr)
	if err != nil {
		return err
	}
	if p.SrcPort == 0 || p.DstPort == 0 {
		return errors.New("tcp: invalid packet port")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	key := connKey{local: Addr{Ip: packet.Addr.DstAddr, Port: p.DstPort}, remote: Addr{Ip: packet.Addr.SrcAddr, Port: p.SrcPort}}
	now := time.Now()
	if c := s.conns[key]; c != nil {
		c.input(p, now)
		return nil
	}
	if p.Flags&protocol.TCP_FLAGS_RST != 0 {
		return nil
	}
	l := s.listeners[key.local]
	if l == nil || p.Flags&protocol.TCP_FLAGS_ACK != 0 {
		s.reset(key, p)
		return nil
	}
	if p.Flags&(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_FIN) != protocol.TCP_FLAGS_SYN {
		return nil
	}
	if len(s.conns) >= maxConnections || l.incomplete+len(l.pending) >= listenBacklog {
		return nil
	}
	s.ports[key.local]++
	c := s.newConn(key.local, key.remote, now)
	c.listener = l
	l.incomplete++
	c.state = synReceived
	c.irs, c.rcvNxt = p.SeqNum, p.SeqNum+1
	c.peerMss = 536
	if p.Mss != 0 {
		c.peerMss = int(p.Mss)
	}
	c.peerWindow = uint32(p.Window)
	c.wlSeq, c.wlAck = p.SeqNum, p.AckNum
	c.sndNxt++
	c.emit(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK, c.iss, nil, true)
	return nil
}

func (s *Stack) newConn(local, remote Addr, now time.Time) *Conn {
	var tuple [12]byte
	copy(tuple[:4], local.Ip[:])
	copy(tuple[4:8], remote.Ip[:])
	binary.BigEndian.PutUint16(tuple[8:10], local.Port)
	binary.BigEndian.PutUint16(tuple[10:12], remote.Port)
	h := hmac.New(sha256.New, s.secret[:])
	_, _ = h.Write(tuple[:])
	iss := binary.BigEndian.Uint32(h.Sum(nil)) + uint32(now.UnixMicro()/4)
	c := &Conn{stack: s, local: local, remote: remote, iss: iss, sndUna: iss, sndNxt: iss,
		peerMss: 536, event: make(chan struct{}), rto: time.Second, handshakeEnd: now.Add(handshakeTimeout),
		tx: byteBuffer{data: make([]byte, sendBufferSize)}, rx: byteBuffer{data: make([]byte, receiveBufferSize)}}
	s.conns[connKey{local, remote}] = c
	return c
}

func (s *Stack) queue(out outbound) {
	if len(s.tx) >= maxConnections*4+16 {
		return
	}
	s.tx = append(s.tx, out)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Stack) run() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	defer close(s.finished)
	for {
		select {
		case <-s.done:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			if !s.closed {
				for _, c := range s.conns {
					c.tick(now)
				}
			}
			s.mu.Unlock()
		case <-s.wake:
		}
		// 限制发送批次 避免阻塞定时任务
		for n := 0; n < 64; n++ {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return
			}
			if len(s.tx) == 0 {
				s.mu.Unlock()
				break
			}
			out := s.tx[0]
			s.tx[0] = outbound{}
			s.tx = s.tx[1:]
			valid := true
			if out.conn != nil {
				valid = out.conn.state != closed
				if out.track {
					valid = valid && seqLess(out.conn.sndUna, out.end)
				}
			}
			output := s.output
			s.mu.Unlock()
			if !valid {
				continue
			}
			sent := output != nil && output(out.packet)
			if !sent && out.conn != nil && out.track {
				s.mu.Lock()
				c := out.conn
				if c.state != closed && !s.closed && seqLess(c.sndUna, out.end) {
					c.pending = &out
				}
				s.mu.Unlock()
			}
		}
		s.mu.Lock()
		if len(s.tx) != 0 {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		s.mu.Unlock()
	}
}

// Close 关闭所有连接并等待后台任务退出 不关闭下层接口
func (s *Stack) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		for _, c := range s.conns {
			c.fail(net.ErrClosed, false)
		}
		for _, l := range s.listeners {
			l.closed = true
			close(l.event)
			s.release(l.local)
		}
		clear(s.listeners)
		s.tx = nil
	}
	s.mu.Unlock()
	<-s.finished
	return nil
}
