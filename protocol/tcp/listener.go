package tcp

import (
	"errors"
	"net"
)

var _ net.Listener = (*Listener)(nil)

// Listener 实现 net.Listener
type Listener struct {
	stack      *Stack
	local      Addr
	incomplete int
	pending    []*Conn
	event      chan struct{}
	closed     bool
}

// Listen 监听本地地址 IP 须为有效单播地址 端口零表示自动分配
func (s *Stack) Listen(local Addr) (*Listener, error) {
	if !unicast(local.Ip) {
		return nil, errors.New("tcp: invalid local address")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	if len(s.listeners) >= maxConnections {
		return nil, errors.New("tcp: listener limit reached")
	}
	if local.Port == 0 {
		for n := 0; n < 16384; n++ {
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
			return nil, errors.New("tcp: no available local port")
		}
	} else if !s.reserve(local) {
		return nil, errors.New("tcp: local port is in use")
	}
	l := &Listener{stack: s, local: local, event: make(chan struct{})}
	s.listeners[local] = l
	return l, nil
}

// Accept 等待并返回已建立的连接
func (l *Listener) Accept() (net.Conn, error) {
	s := l.stack
	for {
		s.mu.Lock()
		if l.closed {
			s.mu.Unlock()
			return nil, net.ErrClosed
		}
		for len(l.pending) != 0 {
			c := l.pending[0]
			l.pending[0] = nil
			l.pending = l.pending[1:]
			if c.err != nil || c.state == closed {
				continue
			}
			c.listener = nil
			s.mu.Unlock()
			return c, nil
		}
		event := l.event
		s.mu.Unlock()
		<-event
	}
}

// Close 停止监听 已接受的连接保持有效
func (l *Listener) Close() error {
	s := l.stack
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.closed {
		return net.ErrClosed
	}
	l.closed = true
	delete(s.listeners, l.local)
	close(l.event)
	for _, c := range s.conns {
		if c.listener == l {
			c.fail(net.ErrClosed, true)
		}
	}
	l.pending = nil
	s.release(l.local)
	return nil
}

// Addr 返回监听地址
func (l *Listener) Addr() net.Addr {
	return l.local
}
