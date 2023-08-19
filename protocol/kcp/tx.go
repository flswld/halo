package kcp

import (
	"sync/atomic"
)

func (s *UDPSession) tx(txqueue []Message) {
	nbytes := 0
	npkts := 0
	for k := range txqueue {
		if n, err := s.conn.WriteTo(txqueue[k].Buffers[0], txqueue[k].Addr); err == nil {
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

func (s *UDPSession) SendEnetNotifyToPeer(enet *Enet) {
	s.conn.SendEnetNotifyToPeer(enet)
}

func (l *Listener) SendEnetNotifyToPeer(enet *Enet) {
	l.conn.SendEnetNotifyToPeer(enet)
}
