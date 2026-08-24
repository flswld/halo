//go:build linux
// +build linux

package kcp

import (
	"net"
	"os"
	"sync/atomic"

	"golang.org/x/net/ipv4"
)

// rx 使用 Linux 批量接口执行客户端收包循环
func (s *UDPSession) rx() {
	// 不具备批量接口时回退到通用 PacketConn 实现
	if s.xconn == nil {
		s.defaultRx()
		return
	}
	// 每个批量槽位长期复用一个最大报文缓冲区
	msgs := make([]ipv4.Message, batchSize)
	for k := range msgs {
		msgs[k].Buffers = [][]byte{make([]byte, mtuLimit)}
	}
	for {
		if count, err := s.xconn.ReadBatch(msgs, 0); err == nil {
			for i := 0; i < count; i++ {
				msg := &msgs[i]
				udpPayload := msg.Buffers[0][:msg.N]
				if s.getRemoteAddr().String() != msg.Addr.String() {
					if !s.remoteAddrChange.Load() {
						// 关闭变更时只接受当前远端地址
						continue
					}
					// Halo 网络切换保持扩展允许已建立会话更新远端地址
					s.setRemoteAddr(msg.Addr)
				}
				if msg.N == 20 {
					// Enet 控制包必须属于当前组合会话标识
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
			}
		} else {
			// compatibility issue:
			// for linux kernel<=2.6.32, support for sendmmsg is not available
			// an error of type os.SyscallError will be returned
			if operr, ok := err.(*net.OpError); ok {
				if se, ok := operr.Err.(*os.SyscallError); ok {
					if se.Syscall == "recvmmsg" {
						s.defaultRx()
						return
					}
				}
			}
			s.notifyReadError(err)
			return
		}
	}
}

// rx 使用 Linux 批量接口执行服务端全局收包循环
func (l *Listener) rx() {
	// 不具备批量接口时回退到通用 PacketConn 实现
	if l.xconn == nil {
		l.defaultRx()
		return
	}
	// Listener 统一读取后按组合会话标识分发
	msgs := make([]ipv4.Message, batchSize)
	for k := range msgs {
		msgs[k].Buffers = [][]byte{make([]byte, mtuLimit)}
	}
	for {
		if count, err := l.xconn.ReadBatch(msgs, 0); err == nil {
			for i := 0; i < count; i++ {
				msg := &msgs[i]
				l.packetInput(msg.Buffers[0][:msg.N], msg.Addr)
			}
		} else {
			// compatibility issue:
			// for linux kernel<=2.6.32, support for sendmmsg is not available
			// an error of type os.SyscallError will be returned
			if operr, ok := err.(*net.OpError); ok {
				if se, ok := operr.Err.(*os.SyscallError); ok {
					if se.Syscall == "recvmmsg" {
						l.defaultRx()
						return
					}
				}
			}
			l.notifyReadError(err)
			return
		}
	}
}

// tx 使用 Linux 批量接口发送 KCP 输出队列
func (s *UDPSession) tx(txqueue []ipv4.Message) {
	// 批量接口不可用或已确认不兼容时回退到逐包发送
	if s.xconn == nil || s.xconnWriteError != nil {
		s.defaultTx(txqueue)
		return
	}
	nbytes := 0
	npkts := 0
	for len(txqueue) > 0 {
		if n, err := s.xconn.WriteBatch(txqueue, 0); err == nil {
			for k := range txqueue[:n] {
				nbytes += len(txqueue[k].Buffers[0])
			}
			npkts += n
			txqueue = txqueue[n:]
		} else {
			// compatibility issue:
			// for linux kernel<=2.6.32, support for sendmmsg is not available
			// an error of type os.SyscallError will be returned
			if operr, ok := err.(*net.OpError); ok {
				if se, ok := operr.Err.(*os.SyscallError); ok {
					if se.Syscall == "sendmmsg" {
						s.xconnWriteError = se
						s.defaultTx(txqueue)
						return
					}
				}
			}
			s.notifyWriteError(err)
			break
		}
	}
	atomic.AddUint64(&DefaultSnmp.OutPkts, uint64(npkts))
	atomic.AddUint64(&DefaultSnmp.OutBytes, uint64(nbytes))
}

// sendEnetNotifyToPeer 使用服务端 Linux 批量接口发送 Enet 控制包
func (l *Listener) sendEnetNotifyToPeer(enet *Enet) {
	// 与 KCP 数据相同地记住批量发送兼容性结果
	if l.xconn == nil || l.xconnWriteError != nil {
		l.defaultSendEnetNotifyToPeer(enet)
		return
	}
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", enet.Addr.String())
	if err != nil {
		return
	}
	_, err = l.xconn.WriteBatch([]ipv4.Message{{Buffers: [][]byte{data}, Addr: remoteAddr}}, 0)
	if err != nil {
		// compatibility issue:
		// for linux kernel<=2.6.32, support for sendmmsg is not available
		// an error of type os.SyscallError will be returned
		if operr, ok := err.(*net.OpError); ok {
			if se, ok := operr.Err.(*os.SyscallError); ok {
				if se.Syscall == "sendmmsg" {
					l.xconnWriteError = se
					l.defaultSendEnetNotifyToPeer(enet)
					return
				}
			}
		}
	}
}

// sendEnetNotifyToPeer 使用客户端 Linux 批量接口发送 Enet 控制包
func (s *UDPSession) sendEnetNotifyToPeer(enet *Enet) {
	// 与 KCP 数据相同地记住批量发送兼容性结果
	if s.xconn == nil || s.xconnWriteError != nil {
		s.defaultSendEnetNotifyToPeer(enet)
		return
	}
	data := BuildEnet(enet.ConnType, enet.EnetType, enet.SessionId, enet.Conv)
	if data == nil {
		return
	}
	_, err := s.xconn.WriteBatch([]ipv4.Message{{Buffers: [][]byte{data}, Addr: s.getRemoteAddr()}}, 0)
	if err != nil {
		// compatibility issue:
		// for linux kernel<=2.6.32, support for sendmmsg is not available
		// an error of type os.SyscallError will be returned
		if operr, ok := err.(*net.OpError); ok {
			if se, ok := operr.Err.(*os.SyscallError); ok {
				if se.Syscall == "sendmmsg" {
					s.xconnWriteError = se
					s.defaultSendEnetNotifyToPeer(enet)
					return
				}
			}
		}
	}
}
