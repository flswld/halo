package kcp

import (
	"encoding/binary"
)

func (s *UDPSession) readLoop() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]

			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				// atomic.AddUint64(&DefaultSnmp.InErrs, 1)
				// continue
				s.remote = addr
				src = addr.String()
			}

			if n == 20 {
				connType, _, sessionId, conv, _, err := ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				if sessionId != s.GetSessionId() || conv != s.GetConv() {
					continue
				}
				if connType == ConnEnetFin {
					s.Close()
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

func (l *Listener) monitor() {
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			var sessionId uint32 = 0
			var conv uint32 = 0
			var rawConv uint64 = 0
			if n == 20 {
				// 连接控制协议
				var connType uint8 = 0
				var enetType uint32 = 0
				connType, enetType, sessionId, conv, rawConv, err = ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				switch connType {
				case ConnEnetSyn:
					// 客户端前置握手获取conv
					l.EnetNotify <- &Enet{
						Addr:      from.String(),
						SessionId: sessionId,
						Conv:      conv,
						ConnType:  ConnEnetSyn,
						EnetType:  enetType,
					}
				case ConnEnetEst:
					// 连接建立
					l.EnetNotify <- &Enet{
						Addr:      from.String(),
						SessionId: sessionId,
						Conv:      conv,
						ConnType:  ConnEnetEst,
						EnetType:  enetType,
					}
				case ConnEnetFin:
					// 连接断开
					l.EnetNotify <- &Enet{
						Addr:      from.String(),
						SessionId: sessionId,
						Conv:      conv,
						ConnType:  ConnEnetFin,
						EnetType:  enetType,
					}
				default:
					continue
				}
			} else {
				// 正常KCP包
				sessionId = binary.LittleEndian.Uint32(udpPayload[0:4])
				conv = binary.LittleEndian.Uint32(udpPayload[4:8])
				rawConv = binary.LittleEndian.Uint64(udpPayload[0:8])
			}
			l.sessionLock.RLock()
			conn, exist := l.sessions[rawConv]
			l.sessionLock.RUnlock()
			if exist {
				if conn.remote.String() != from.String() {
					conn.remote = from
					// 连接地址改变
					l.EnetNotify <- &Enet{
						Addr:      conn.remote.String(),
						SessionId: sessionId,
						Conv:      conv,
						ConnType:  ConnEnetAddrChange,
					}
				}
			}
			l.packetInput(udpPayload, from, rawConv)
		} else {
			l.notifyReadError(err)
			return
		}
	}
}
