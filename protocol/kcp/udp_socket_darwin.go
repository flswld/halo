//go:build darwin
// +build darwin

package kcp

import (
	"golang.org/x/net/ipv4"
)

// rx 在 Darwin 上使用通用 PacketConn 客户端收包实现
func (s *UDPSession) rx() {
	s.defaultRx()
}

// rx 在 Darwin 上使用通用 PacketConn 服务端收包实现
func (l *Listener) rx() {
	l.defaultRx()
}

// tx 在 Darwin 上使用通用 PacketConn 发包实现
func (s *UDPSession) tx(txqueue []ipv4.Message) {
	s.defaultTx(txqueue)
}

// sendEnetNotifyToPeer 在 Darwin 上使用通用服务端 Enet 发包实现
func (l *Listener) sendEnetNotifyToPeer(enet *Enet) {
	l.defaultSendEnetNotifyToPeer(enet)
}

// sendEnetNotifyToPeer 在 Darwin 上使用通用客户端 Enet 发包实现
func (s *UDPSession) sendEnetNotifyToPeer(enet *Enet) {
	s.defaultSendEnetNotifyToPeer(enet)
}
