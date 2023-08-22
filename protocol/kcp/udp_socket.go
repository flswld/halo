package kcp

import (
	"encoding/binary"
)

func (s *UDPSession) rx() {
	buf := make([]byte, mtuLimit)
	for {
		if n, err := s.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			if n == 20 {
				connType, enetType, sessionId, conv, _, err := ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				if sessionId != s.GetSessionId() || conv != s.GetConv() {
					continue
				}
				if connType == ConnEnetFin {
					s.SendEnetNotifyToPeer(&Enet{
						SessionId: s.GetSessionId(),
						Conv:      s.GetConv(),
						ConnType:  ConnEnetFin,
						EnetType:  enetType,
					})
					_ = s.Close()
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

func (l *Listener) rx() {
	buf := make([]byte, mtuLimit)
	for {
		if n, err := l.conn.ReadFrom(buf); err == nil {
			udpPayload := buf[:n]
			var sessionId uint32 = 0
			var conv uint32 = 0
			var rawConv uint64 = 0
			if n == 20 {
				// 连接控制协议
				var connType = ""
				var enetType uint32 = 0
				connType, enetType, sessionId, conv, rawConv, err = ParseEnet(udpPayload)
				if err != nil {
					continue
				}
				switch connType {
				case ConnEnetSyn:
					// 客户端前置握手获取conv
					l.enetNotifyChan <- &Enet{SessionId: sessionId, Conv: conv, ConnType: ConnEnetSyn, EnetType: enetType}
				case ConnEnetEst:
					// 连接建立
					l.enetNotifyChan <- &Enet{SessionId: sessionId, Conv: conv, ConnType: ConnEnetEst, EnetType: enetType}
				case ConnEnetFin:
					// 连接断开
					l.enetNotifyChan <- &Enet{SessionId: sessionId, Conv: conv, ConnType: ConnEnetFin, EnetType: enetType}
				default:
					continue
				}
			} else {
				// 正常KCP包
				sessionId = binary.LittleEndian.Uint32(udpPayload[0:4])
				conv = binary.LittleEndian.Uint32(udpPayload[4:8])
				rawConv = binary.LittleEndian.Uint64(udpPayload[0:8])
			}
			l.packetInput(udpPayload, rawConv)
		} else {
			l.notifyReadError(err)
			return
		}
	}
}

func (s *UDPSession) tx(txqueue []Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		var n = 0
		var err error = nil
		n, err = s.conn.WriteTo(txqueue[k].Buffers[0])
		if err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(err)
			break
		}
	}
}

func (l *Listener) SendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	_, _ = l.conn.WriteTo(data)
}

func (s *UDPSession) SendEnetNotifyToPeer(enet *Enet) {
	data := BuildEnet(enet.ConnType, enet.EnetType, s.GetSessionId(), s.GetConv())
	if data == nil {
		return
	}
	_, _ = s.conn.WriteTo(data)
}
