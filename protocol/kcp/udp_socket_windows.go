//go:build windows
// +build windows

package kcp

import (
	"golang.org/x/net/ipv4"
)

func (s *UDPSession) rx() {
	s.defaultRx()
}

func (l *Listener) rx() {
	l.defaultRx()
}

func (s *UDPSession) tx(txqueue []ipv4.Message) {
	s.defaultTx(txqueue)
}

func (l *Listener) sendEnetNotifyToPeer(enet *Enet) {
	l.defaultSendEnetNotifyToPeer(enet)
}

func (s *UDPSession) sendEnetNotifyToPeer(enet *Enet) {
	s.defaultSendEnetNotifyToPeer(enet)
}
