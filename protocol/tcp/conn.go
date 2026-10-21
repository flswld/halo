package tcp

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

var _ net.Conn = (*Conn)(nil)

// Conn 实现 net.Conn
type Conn struct {
	stack                                   *Stack
	local, remote                           Addr
	readMu, writeMu                         sync.Mutex
	event                                   chan struct{}
	rd, wd                                  time.Time
	err                                     error
	appClosed, writeClosed                  bool
	listener                                *Listener
	state                                   state
	iss, irs, sndUna, sndNxt, rcvNxt        uint32
	wlSeq, wlAck, peerWindow                uint32
	peerMss                                 int
	tx, rx                                  byteBuffer
	inflight                                int
	finSent, recvFin                        bool
	finSeq                                  uint32
	handshakeEnd, closeEnd, timeWaitEnd     time.Time
	progress, retransmitAt, sentAt, probeAt time.Time
	rto, srtt, rttvar, probeInterval        time.Duration
	rttValid                                bool
	pending                                 *outbound
	pendingAt                               time.Time
}

func (c *Conn) notify() { close(c.event); c.event = make(chan struct{}) }

func waitEvent(event <-chan struct{}, deadline time.Time) {
	if deadline.IsZero() {
		<-event
		return
	}
	delay := time.Until(deadline)
	if delay <= 0 {
		return
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-event:
	case <-timer.C:
	}
}

// Read 读取字节流 对端 FIN 前的数据读完后返回 io.EOF
func (c *Conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	s := c.stack
	for {
		s.mu.Lock()
		if c.appClosed {
			s.mu.Unlock()
			return 0, net.ErrClosed
		}
		if !c.rd.IsZero() && !time.Now().Before(c.rd) {
			s.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		if len(p) == 0 {
			s.mu.Unlock()
			return 0, nil
		}
		if c.rx.size > 0 {
			n := c.rx.read(p)
			if c.state != closed && c.state != timeWait {
				c.ack()
			}
			s.mu.Unlock()
			return n, nil
		}
		if c.err != nil {
			err := c.err
			s.mu.Unlock()
			return 0, err
		}
		if c.recvFin {
			s.mu.Unlock()
			return 0, io.EOF
		}
		if c.state == closed {
			s.mu.Unlock()
			return 0, net.ErrClosed
		}
		event, deadline := c.event, c.rd
		s.mu.Unlock()
		waitEvent(event, deadline)
	}
}

// Write 复制数据到发送缓存 缓存满时阻塞 超时可部分写入
func (c *Conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	s := c.stack
	n := 0
	for {
		s.mu.Lock()
		if c.appClosed || c.writeClosed {
			s.mu.Unlock()
			return n, net.ErrClosed
		}
		if c.err != nil {
			err := c.err
			s.mu.Unlock()
			return n, err
		}
		if c.state != established && c.state != closeWait {
			s.mu.Unlock()
			return n, net.ErrClosed
		}
		now := time.Now()
		if !c.wd.IsZero() && !now.Before(c.wd) {
			s.mu.Unlock()
			return n, os.ErrDeadlineExceeded
		}
		if n == len(p) {
			s.mu.Unlock()
			return n, nil
		}
		if c.tx.size == 0 {
			c.progress = now
		}
		n += c.tx.write(p[n:])
		c.pump(now)
		if n == len(p) {
			s.mu.Unlock()
			return n, nil
		}
		event, deadline := c.event, c.wd
		s.mu.Unlock()
		waitEvent(event, deadline)
	}
}

// CloseWrite 关闭写入 已缓存数据发送后发送 FIN
func (c *Conn) CloseWrite() error {
	s := c.stack
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.appClosed || c.state == closed {
		return net.ErrClosed
	}
	if !c.writeClosed {
		c.writeClosed = true
		now := time.Now()
		c.closeEnd = now.Add(closeTimeout)
		c.notify()
		c.pump(now)
	}
	return nil
}

// Close 关闭应用侧读写 后台在超时前继续发送和挥手
func (c *Conn) Close() error {
	s := c.stack
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.appClosed {
		return net.ErrClosed
	}
	c.appClosed = true
	c.writeClosed = true
	c.notify()
	if c.rx.size != 0 {
		c.fail(net.ErrClosed, true)
		return nil
	}
	if c.state != closed && c.state != timeWait {
		now := time.Now()
		c.closeEnd = now.Add(closeTimeout)
		c.pump(now)
	}
	return nil
}

// LocalAddr 返回本地端点
func (c *Conn) LocalAddr() net.Addr { return c.local }

// RemoteAddr 返回远端端点
func (c *Conn) RemoteAddr() net.Addr { return c.remote }

// SetDeadline 设置读写绝对期限 零值清除
func (c *Conn) SetDeadline(t time.Time) error {
	c.stack.mu.Lock()
	defer c.stack.mu.Unlock()
	if c.appClosed || c.state == closed {
		return net.ErrClosed
	}
	c.rd, c.wd = t, t
	c.notify()
	return nil
}

// SetReadDeadline 设置读取期限
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.stack.mu.Lock()
	defer c.stack.mu.Unlock()
	if c.appClosed || c.state == closed {
		return net.ErrClosed
	}
	c.rd = t
	c.notify()
	return nil
}

// SetWriteDeadline 设置写入期限
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.stack.mu.Lock()
	defer c.stack.mu.Unlock()
	if c.appClosed || c.state == closed {
		return net.ErrClosed
	}
	c.wd = t
	c.notify()
	return nil
}
