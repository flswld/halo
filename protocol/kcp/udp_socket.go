package kcp

import (
	"net"
	"sync/atomic"

	"golang.org/x/net/ipv4"
)

// defaultRx 使用通用 PacketConn 执行客户端收包循环
func (s *UDPSession) defaultRx() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			// Halo 网络切换保持扩展允许已建立会话更新远端地址
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				s.remote = addr
				src = addr.String()
			}
			if n == 20 {
				// 固定长度报文按 Enet 控制包解析并校验组合会话标识
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

// defaultRx 使用通用 PacketConn 执行服务端全局收包循环
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

// defaultTx 逐包发送 KCP 输出队列并更新统计
func (s *UDPSession) defaultTx(txqueue []ipv4.Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		var n = 0
		var err error = nil
		// 服务端共享 PacketConn 客户端使用已连接 UDP 套接字
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

// defaultSendEnetNotifyToPeer 通过服务端 PacketConn 发送 Enet 控制包
func (l *Listener) defaultSendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", enet.Addr.String())
	if err != nil {
		return
	}
	_, _ = l.conn.WriteTo(data, remoteAddr)
}

// defaultSendEnetNotifyToPeer 通过客户端连接发送 Enet 控制包
func (s *UDPSession) defaultSendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	if s.l != nil {
		_, _ = s.conn.WriteTo(data, s.remote)
	} else {
		_, _ = s.conn.(*net.UDPConn).Write(data)
	}
}

// rxChanConn 执行 Halo 内存管道客户端收包循环
func (s *UDPSession) rxChanConn() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			// 管道端点变化与 UDP 网络切换使用相同的会话保持语义
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				s.remote = addr
				src = addr.String()
			}
			if n == 20 {
				// 仅处理属于当前组合会话标识的 Enet 控制包
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

// rxChanConn 执行 Halo 内存管道服务端全局收包循环
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

// txChanConn 将 KCP 输出队列逐包写入 Halo 内存管道
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

// sendEnetNotifyToPeerChanConn 通过服务端内存管道发送 Enet 控制包
func (l *Listener) sendEnetNotifyToPeerChanConn(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	_, _ = l.conn.WriteTo(data, enet.Addr)
}

// sendEnetNotifyToPeerChanConn 通过客户端内存管道发送 Enet 控制包
func (s *UDPSession) sendEnetNotifyToPeerChanConn(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	_, _ = s.conn.WriteTo(data, enet.Addr)
}
