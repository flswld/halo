// Package kcp-go is a Reliable-UDP library for golang.
//
// This library intents to provide a smooth, resilient, ordered,
// error-checked and anonymous delivery of streams over UDP packets.
//
// The interfaces of this package aims to be compatible with
// net.Conn in standard library, but offers powerful features for advanced users.
package kcp

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maximum packet size
	mtuLimit = 1500

	// accept backlog
	acceptBacklog = 128
)

var (
	errInvalidOperation = errors.New("invalid operation")
	errTimeout          = errors.New("timeout")
)

var (
	// a system-wide packet buffer shared among sending, receiving
	// to mitigate high-frequency memory allocation for packets, bytes from xmitBuf
	// is aligned to 64bit
	xmitBuf sync.Pool
)

func init() {
	xmitBuf.New = func() interface{} {
		return make([]byte, mtuLimit)
	}
}

type Message struct {
	Buffers [][]byte
}

var ErrConnAlreadyClose = errors.New("conn already close")

type Conn struct {
	RxChan  chan []byte
	TxChan  chan []byte
	IsClose bool
}

func (c *Conn) ReadFrom(buf []byte) (int, error) {
	if c.IsClose {
		return 0, ErrConnAlreadyClose
	}
	pkt := <-c.RxChan
	copy(buf, pkt)
	return len(pkt), nil
}

func (c *Conn) WriteTo(buf []byte) (int, error) {
	if c.IsClose {
		return 0, ErrConnAlreadyClose
	}
	pkt := make([]byte, len(buf))
	copy(pkt, buf)
	c.TxChan <- pkt
	return len(pkt), nil
}

func (c *Conn) Close() error {
	if c.IsClose {
		return ErrConnAlreadyClose
	}
	c.IsClose = true
	close(c.RxChan)
	close(c.TxChan)
	return nil
}

type (
	// UDPSession defines a KCP session implemented by UDP
	UDPSession struct {
		conn    *Conn     // the underlying packet connection
		ownConn bool      // true if we created conn internally, false if provided by caller
		kcp     *KCP      // KCP ARQ protocol
		l       *Listener // pointing to the Listener object if it's been accepted by a Listener

		// kcp receiving is based on packets
		// recvbuf turns packets into stream
		recvbuf []byte
		bufptr  []byte

		// settings
		rd         time.Time // read deadline
		wd         time.Time // write deadline
		headerSize int       // the header size additional to a KCP frame
		ackNoDelay bool      // send ack immediately for each incoming packet(testing purpose)
		writeDelay bool      // delay kcp.flush() for Write() for bulk transfer
		dup        int       // duplicate udp packets(testing purpose)

		// notifications
		die          chan struct{} // notify current session has Closed
		dieOnce      sync.Once
		chReadEvent  chan struct{} // notify Read() can be called without blocking
		chWriteEvent chan struct{} // notify Write() can be called without blocking

		// socket error handling
		socketReadError      atomic.Value
		socketWriteError     atomic.Value
		chSocketReadError    chan struct{}
		chSocketWriteError   chan struct{}
		socketReadErrorOnce  sync.Once
		socketWriteErrorOnce sync.Once

		// packets waiting to be sent on wire
		txqueue []Message

		mu sync.Mutex
	}
)

// newUDPSession create a new udp session for client or server
func newUDPSession(conv uint64, l *Listener, conn *Conn, ownConn bool) *UDPSession {
	sess := new(UDPSession)
	sess.die = make(chan struct{})
	sess.chReadEvent = make(chan struct{}, 1)
	sess.chWriteEvent = make(chan struct{}, 1)
	sess.chSocketReadError = make(chan struct{})
	sess.chSocketWriteError = make(chan struct{})
	sess.conn = conn
	sess.ownConn = ownConn
	sess.l = l
	sess.recvbuf = make([]byte, mtuLimit)

	sess.kcp = NewKCP(conv, func(buf []byte, size int) {
		if size >= IKCP_OVERHEAD+sess.headerSize {
			sess.output(buf[:size])
		}
	})
	sess.kcp.ReserveBytes(sess.headerSize)

	if sess.l == nil { // it's a client connection
		go sess.rx()
	}

	// start per-session updater
	SystemTimedSched.Put(sess.update, time.Now())

	return sess
}

// Read implements net.Conn
func (s *UDPSession) Read(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if len(s.bufptr) > 0 { // copy from buffer into b
			n = copy(b, s.bufptr)
			s.bufptr = s.bufptr[n:]
			s.mu.Unlock()
			return n, nil
		}

		if size := s.kcp.PeekSize(); size > 0 { // peek data size from kcp
			if len(b) >= size { // receive data into 'b' directly
				s.kcp.Recv(b)
				s.mu.Unlock()
				return size, nil
			}

			// if necessary resize the stream buffer to guarantee a sufficient buffer space
			if cap(s.recvbuf) < size {
				s.recvbuf = make([]byte, size)
			}

			// resize the length of recvbuf to correspond to data size
			s.recvbuf = s.recvbuf[:size]
			s.kcp.Recv(s.recvbuf)
			n = copy(b, s.recvbuf)   // copy to 'b'
			s.bufptr = s.recvbuf[n:] // pointer update
			s.mu.Unlock()
			return n, nil
		}

		// deadline for current reading operation
		var timeout *time.Timer
		var c <-chan time.Time
		if !s.rd.IsZero() {
			if time.Now().After(s.rd) {
				s.mu.Unlock()
				return 0, errTimeout
			}

			delay := time.Until(s.rd)
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		// wait for read event or timeout or error
		select {
		case <-s.chReadEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errTimeout
		case <-s.chSocketReadError:
			return 0, s.socketReadError.Load().(error)
		case <-s.die:
			return 0, io.ErrClosedPipe
		}
	}
}

// GetMaxPayloadLen 获取应用层最大包长度
func (s *UDPSession) GetMaxPayloadLen() int {
	return 256 * int(s.kcp.mss)
}

// Write implements net.Conn
func (s *UDPSession) Write(b []byte) (n int, err error) {
	if len(b) > s.GetMaxPayloadLen() {
		return 0, errors.New("send payload above 256*mss")
	}

	for {
		select {
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, io.ErrClosedPipe
		default:
		}

		s.mu.Lock()

		// make sure write do not overflow the max sliding window on both side
		waitsnd := s.kcp.WaitSnd()
		if waitsnd < int(s.kcp.snd_wnd) && waitsnd < int(s.kcp.rmt_wnd) {
			n += len(b)
			s.kcp.Send(b)

			waitsnd = s.kcp.WaitSnd()
			if waitsnd >= int(s.kcp.snd_wnd) || waitsnd >= int(s.kcp.rmt_wnd) || !s.writeDelay {
				s.kcp.flush(false)
				s.uncork()
			}
			s.mu.Unlock()
			return n, nil
		}

		var timeout *time.Timer
		var c <-chan time.Time
		if !s.wd.IsZero() {
			if time.Now().After(s.wd) {
				s.mu.Unlock()
				return 0, errTimeout
			}
			delay := time.Until(s.wd)
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		select {
		case <-s.chWriteEvent:
			if timeout != nil {
				timeout.Stop()
			}
		case <-c:
			return 0, errTimeout
		case <-s.chSocketWriteError:
			return 0, s.socketWriteError.Load().(error)
		case <-s.die:
			return 0, io.ErrClosedPipe
		}
	}
}

// uncork sends data in txqueue if there is any
func (s *UDPSession) uncork() {
	if len(s.txqueue) > 0 {
		s.tx(s.txqueue)
		// recycle
		for k := range s.txqueue {
			xmitBuf.Put(s.txqueue[k].Buffers[0])
			s.txqueue[k].Buffers = nil
		}
		s.txqueue = s.txqueue[:0]
	}
}

// Close closes the connection.
func (s *UDPSession) Close() error {
	var once bool
	s.dieOnce.Do(func() {
		close(s.die)
		once = true
	})

	if once {
		// try best to send all queued messages
		s.mu.Lock()
		s.kcp.flush(false)
		s.uncork()
		// release pending segments
		s.kcp.ReleaseTX()
		s.mu.Unlock()

		if s.l != nil { // belongs to listener
			s.l.closeSession(s.kcp.conv)
			return nil
		} else if s.ownConn { // client socket close
			return s.conn.Close()
		} else {
			return nil
		}
	} else {
		return io.ErrClosedPipe
	}
}

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
func (s *UDPSession) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.wd = t
	s.notifyReadEvent()
	s.notifyWriteEvent()
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (s *UDPSession) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.notifyReadEvent()
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (s *UDPSession) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wd = t
	s.notifyWriteEvent()
	return nil
}

// SetWriteDelay delays write for bulk transfer until the next update interval
func (s *UDPSession) SetWriteDelay(delay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDelay = delay
}

// SetWindowSize set maximum window size
func (s *UDPSession) SetWindowSize(sndwnd, rcvwnd int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.WndSize(sndwnd, rcvwnd)
}

// SetMtu sets the maximum transmission unit(not including UDP header)
func (s *UDPSession) SetMtu(mtu int) bool {
	if mtu > mtuLimit {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.SetMtu(mtu)
	return true
}

// SetStreamMode toggles the stream mode on/off
func (s *UDPSession) SetStreamMode(enable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enable {
		s.kcp.stream = 1
	} else {
		s.kcp.stream = 0
	}
}

// SetACKNoDelay changes ack flush option, set true to flush ack immediately,
func (s *UDPSession) SetACKNoDelay(nodelay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackNoDelay = nodelay
}

// (deprecated)
//
// SetDUP duplicates udp packets for kcp output.
func (s *UDPSession) SetDUP(dup int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dup = dup
}

// SetNoDelay calls nodelay() of kcp
// https://github.com/skywind3000/kcp/blob/master/README.en.md#protocol-configuration
func (s *UDPSession) SetNoDelay(nodelay, interval, resend, nc int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.NoDelay(nodelay, interval, resend, nc)
}

// post-processing for sending a packet from kcp core
func (s *UDPSession) output(buf []byte) {
	var ecc [][]byte

	var msg Message
	for i := 0; i < s.dup+1; i++ {
		bts := xmitBuf.Get().([]byte)[:len(buf)]
		copy(bts, buf)
		msg.Buffers = [][]byte{bts}
		s.txqueue = append(s.txqueue, msg)
	}

	for k := range ecc {
		bts := xmitBuf.Get().([]byte)[:len(ecc[k])]
		copy(bts, ecc[k])
		msg.Buffers = [][]byte{bts}
		s.txqueue = append(s.txqueue, msg)
	}
}

// sess update to trigger protocol
func (s *UDPSession) update() {
	select {
	case <-s.die:
	default:
		s.mu.Lock()
		interval := s.kcp.flush(false)
		waitsnd := s.kcp.WaitSnd()
		if waitsnd < int(s.kcp.snd_wnd) && waitsnd < int(s.kcp.rmt_wnd) {
			s.notifyWriteEvent()
		}
		s.uncork()
		s.mu.Unlock()
		// self-synchronized timed scheduling
		SystemTimedSched.Put(s.update, time.Now().Add(time.Duration(interval)*time.Millisecond))
	}
}

// GetRawConv gets conversation id of a session
// 获取KCP组合会话id
func (s *UDPSession) GetRawConv() uint64 {
	return s.kcp.conv
}

// GetSessionId 获取会话id
func (s *UDPSession) GetSessionId() uint32 {
	rawConvData := make([]byte, 8)
	binary.LittleEndian.PutUint64(rawConvData, s.kcp.conv)
	return binary.LittleEndian.Uint32(rawConvData[0:4])
}

// GetConv 获取KCP会话id
func (s *UDPSession) GetConv() uint32 {
	rawConvData := make([]byte, 8)
	binary.LittleEndian.PutUint64(rawConvData, s.kcp.conv)
	return binary.LittleEndian.Uint32(rawConvData[4:8])
}

// GetRTO gets current rto of the session
func (s *UDPSession) GetRTO() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kcp.rx_rto
}

// GetSRTT gets current srtt of the session
func (s *UDPSession) GetSRTT() int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kcp.rx_srtt
}

// GetSRTTVar gets current rtt variance of the session
func (s *UDPSession) GetSRTTVar() int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kcp.rx_rttvar
}

func (s *UDPSession) notifyReadEvent() {
	select {
	case s.chReadEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) notifyWriteEvent() {
	select {
	case s.chWriteEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) notifyReadError(err error) {
	s.socketReadErrorOnce.Do(func() {
		s.socketReadError.Store(err)
		close(s.chSocketReadError)
	})
}

func (s *UDPSession) notifyWriteError(err error) {
	s.socketWriteErrorOnce.Do(func() {
		s.socketWriteError.Store(err)
		close(s.chSocketWriteError)
	})
}

// packet input stage
func (s *UDPSession) packetInput(data []byte) {
	if len(data) >= IKCP_OVERHEAD {
		s.kcpInput(data)
	}
}

func (s *UDPSession) kcpInput(data []byte) {
	var kcpInErrors uint64
	s.mu.Lock()
	if ret := s.kcp.Input(data, true, s.ackNoDelay); ret != 0 {
		kcpInErrors++
	}
	if n := s.kcp.PeekSize(); n > 0 {
		s.notifyReadEvent()
	}
	waitsnd := s.kcp.WaitSnd()
	if waitsnd < int(s.kcp.snd_wnd) && waitsnd < int(s.kcp.rmt_wnd) {
		s.notifyWriteEvent()
	}
	s.uncork()
	s.mu.Unlock()
}

type (
	// Listener defines a server which will be waiting to accept incoming connections
	Listener struct {
		conn *Conn // the underlying packet connection

		// 网络切换会话保持改造 将convId作为会话的唯一标识 不再校验源地址
		sessions    map[uint64]*UDPSession // all sessions accepted by this Listener
		sessionLock sync.RWMutex
		chAccepts   chan *UDPSession // Listen() backlog

		die     chan struct{} // notify the listener has closed
		dieOnce sync.Once

		// socket error handling
		socketReadError     atomic.Value
		chSocketReadError   chan struct{}
		socketReadErrorOnce sync.Once

		rd atomic.Value // read deadline for Accept()

		enetNotifyChan chan *Enet // Enet事件上报管道
	}
)

func (l *Listener) GetEnetNotifyChan() chan *Enet {
	return l.enetNotifyChan
}

// packet input stage
func (l *Listener) packetInput(data []byte, rawConv uint64) {
	if len(data) >= IKCP_OVERHEAD {
		l.sessionLock.RLock()
		s, ok := l.sessions[rawConv]
		l.sessionLock.RUnlock()

		if ok { // existing connection
			if rawConv == s.kcp.conv { // parity data or valid conversation
				s.kcpInput(data)
			}
		}

		if s == nil { // new session
			if len(l.chAccepts) < cap(l.chAccepts) { // do not let the new sessions overwhelm accept queue
				s := newUDPSession(rawConv, l, l.conn, false)
				s.kcpInput(data)
				l.sessionLock.Lock()
				l.sessions[rawConv] = s
				l.sessionLock.Unlock()
				l.chAccepts <- s
			}
		}
	}
}

func (l *Listener) notifyReadError(err error) {
	l.socketReadErrorOnce.Do(func() {
		l.socketReadError.Store(err)
		close(l.chSocketReadError)

		// propagate read error to all sessions
		l.sessionLock.RLock()
		for _, s := range l.sessions {
			s.notifyReadError(err)
		}
		l.sessionLock.RUnlock()
	})
}

// AcceptKCP accepts a KCP connection
func (l *Listener) AcceptKCP() (*UDPSession, error) {
	var timeout <-chan time.Time
	if tdeadline, ok := l.rd.Load().(time.Time); ok && !tdeadline.IsZero() {
		timeout = time.After(time.Until(tdeadline))
	}

	select {
	case <-timeout:
		return nil, errTimeout
	case c := <-l.chAccepts:
		return c, nil
	case <-l.chSocketReadError:
		return nil, l.socketReadError.Load().(error)
	case <-l.die:
		return nil, io.ErrClosedPipe
	}
}

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
func (l *Listener) SetDeadline(t time.Time) error {
	_ = l.SetReadDeadline(t)
	_ = l.SetWriteDeadline(t)
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (l *Listener) SetReadDeadline(t time.Time) error {
	l.rd.Store(t)
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (l *Listener) SetWriteDeadline(t time.Time) error { return errInvalidOperation }

// Close stops listening on the UDP address, and closes the socket
func (l *Listener) Close() error {
	var once bool
	l.dieOnce.Do(func() {
		close(l.die)
		once = true
	})

	var err error
	if once {
		err = l.conn.Close()
	} else {
		err = io.ErrClosedPipe
	}
	return err
}

// closeSession notify the listener that a session has closed
func (l *Listener) closeSession(conv uint64) (ret bool) {
	l.sessionLock.Lock()
	defer l.sessionLock.Unlock()
	if _, ok := l.sessions[conv]; ok {
		delete(l.sessions, conv)
		return true
	}
	return false
}

// Listen 监听
func Listen(conn *Conn) (*Listener, error) {
	l := new(Listener)
	l.conn = conn
	l.sessions = make(map[uint64]*UDPSession)
	l.chAccepts = make(chan *UDPSession, acceptBacklog)
	l.die = make(chan struct{})
	l.chSocketReadError = make(chan struct{})
	l.enetNotifyChan = make(chan *Enet, 1000)
	go l.rx()
	return l, nil
}

// Dial 连接
func Dial(conn *Conn) (*UDPSession, error) {
	enet := &Enet{
		SessionId: 0,
		Conv:      0,
		ConnType:  ConnEnetSyn,
		EnetType:  EnetClientConnectKey,
	}
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	_, err := conn.WriteTo(data)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, mtuLimit)
	n, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}
	udpPayload := buf[:n]
	connType, enetType, sessionId, conv, _, err := ParseEnet(udpPayload)
	if err != nil || connType != ConnEnetEst || enetType != EnetClientConnectKey {
		return nil, errors.New("recv packet format error")
	}

	rawConvData := make([]byte, 8)
	binary.LittleEndian.PutUint32(rawConvData[0:4], sessionId)
	binary.LittleEndian.PutUint32(rawConvData[4:8], conv)
	rawConv := binary.LittleEndian.Uint64(rawConvData)

	return newUDPSession(rawConv, nil, conn, true), nil
}
