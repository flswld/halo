package kcp

import (
	"net"
	"sync/atomic"

	"golang.org/x/net/ipv4"
)

// 客户端收包循环
func (s *UDPSession) defaultRx() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				s.remote = addr
				src = addr.String()
			}
			if n == 20 {
				connType, enetType, sessionId, conv, _, err := ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				if sessionId != s.GetSessionId() || conv != s.GetConv() {
					continue
				}
				if connType == ConnEnetFin {
					_ = s.CloseReason(enetType)
					continue
				}
			}
			s.packetInput(udpPayload)
		} else {
			s.notifyReadError(err)
			return
		}
	}
}

// 服务器全局收包循环
func (l *Listener) defaultRx() {
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			l.packetInput(buf[:n], from)
		} else {
			l.notifyReadError(err)
			return
		}
	}
}

// 公共发包接口
func (s *UDPSession) defaultTx(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		var n = 0
		var err error = nil
		if s.l != nil {
			n, err = s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr)
		} else {
			n, err = s.conn.(*net.UDPConn).Write(txqueue[k].Buffers[0])
		}
		if err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(err)
			break
		}
	}
	atomic.AddUint64(&DefaultSnmp.OutPkts, uint64(npkts))
	atomic.AddUint64(&DefaultSnmp.OutBytes, uint64(nbytes))
}

// 服务器Enet事件发送接口
func (l *Listener) defaultSendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", enet.Addr)
	if err != nil {
		return
	}
	_, _ = l.conn.WriteTo(data, remoteAddr)
}

// 客户端Enet事件发送接口
func (s *UDPSession) defaultSendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, s.GetSessionId(), s.GetConv())
	if data == nil {
		return
	}
	if s.l != nil {
		_, _ = s.conn.WriteTo(data, s.remote)
	} else {
		_, _ = s.conn.(*net.UDPConn).Write(data)
	}
}

func (s *UDPSession) rxChanConn() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				s.remote = addr
				src = addr.String()
			}
			if n == 20 {
				connType, enetType, sessionId, conv, _, err := ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				if sessionId != s.GetSessionId() || conv != s.GetConv() {
					continue
				}
				if connType == ConnEnetFin {
					_ = s.CloseReason(enetType)
					continue
				}
			}
			s.packetInput(udpPayload)
		} else {
			s.notifyReadError(err)
			return
		}
	}
}

func (l *Listener) rxChanConn() {
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			l.packetInput(buf[:n], from)
		} else {
			l.notifyReadError(err)
			return
		}
	}
}

func (s *UDPSession) txChanConn(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		var n = 0
		var err error = nil
		n, err = s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr)
		if err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(err)
			break
		}
	}
	atomic.AddUint64(&DefaultSnmp.OutPkts, uint64(npkts))
	atomic.AddUint64(&DefaultSnmp.OutBytes, uint64(nbytes))
}

func (l *Listener) sendEnetNotifyToPeerChanConn(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	_, _ = l.conn.WriteTo(data, &ChanConnAddr{})
}

func (s *UDPSession) sendEnetNotifyToPeerChanConn(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, s.GetSessionId(), s.GetConv())
	if data == nil {
		return
	}
	_, _ = s.conn.WriteTo(data, &ChanConnAddr{})
}
