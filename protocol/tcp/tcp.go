package tcp

import (
	"errors"
	"time"

	"github.com/flswld/halo/protocol"
)

type state uint8

const (
	closed state = iota
	synSent
	synReceived
	established
	finWait1
	finWait2
	closeWait
	closing
	lastAck
	timeWait
)

// 序号按模 2^32 比较 有效距离小于 2^31
func seqLess(a, b uint32) bool { return int32(a-b) < 0 }

func (c *Conn) emit(flags uint8, seq uint32, data []byte, track bool) {
	addr := protocol.Ipv4AddrPair{SrcAddr: c.local.Ip, DstAddr: c.remote.Ip}
	p := protocol.TcpPkt{SrcPort: c.local.Port, DstPort: c.remote.Port, SeqNum: seq,
		AckNum: c.rcvNxt, Flags: flags, Window: uint16(len(c.rx.data) - c.rx.size), Payload: data}
	if flags&protocol.TCP_FLAGS_SYN != 0 {
		p.Mss = mss
	}
	raw, err := protocol.BuildTcpPkt(nil, p, addr)
	if err != nil {
		c.fail(err, false)
		return
	}
	end := seq + uint32(len(data))
	if flags&protocol.TCP_FLAGS_SYN != 0 {
		end++
	}
	if flags&protocol.TCP_FLAGS_FIN != 0 {
		end++
	}
	c.stack.queue(outbound{packet: Packet{Addr: addr, Data: raw}, conn: c, end: end, track: track})
}

func (c *Conn) ack() { c.emit(protocol.TCP_FLAGS_ACK, c.sndNxt, nil, false) }

func (s *Stack) reset(key connKey, p protocol.TcpPkt) {
	reset := protocol.TcpPkt{SrcPort: key.local.Port, DstPort: key.remote.Port, Flags: protocol.TCP_FLAGS_RST}
	if p.Flags&protocol.TCP_FLAGS_ACK != 0 {
		reset.SeqNum = p.AckNum
	} else {
		reset.Flags |= protocol.TCP_FLAGS_ACK
		reset.AckNum = p.SeqNum + uint32(len(p.Payload))
		if p.Flags&protocol.TCP_FLAGS_SYN != 0 {
			reset.AckNum++
		}
		if p.Flags&protocol.TCP_FLAGS_FIN != 0 {
			reset.AckNum++
		}
	}
	addr := protocol.Ipv4AddrPair{SrcAddr: key.local.Ip, DstAddr: key.remote.Ip}
	raw, err := protocol.BuildTcpPkt(nil, reset, addr)
	if err != nil {
		return
	}
	s.queue(outbound{packet: Packet{Addr: addr, Data: raw}})
}

func (c *Conn) fail(err error, reset bool) {
	if c.state == closed {
		return
	}
	if reset && c.state != synSent && c.state != timeWait && !c.stack.closed {
		// 借用 ACK 分支生成序号为 sndNxt 的 RST
		c.stack.reset(connKey{c.local, c.remote}, protocol.TcpPkt{Flags: protocol.TCP_FLAGS_ACK, AckNum: c.sndNxt})
	}
	if c.listener != nil && c.state == synReceived {
		c.listener.incomplete--
	}
	if c.listener != nil && c.state != synReceived {
		l := c.listener
		for n, pending := range l.pending {
			if pending == c {
				copy(l.pending[n:], l.pending[n+1:])
				l.pending[len(l.pending)-1] = nil
				l.pending = l.pending[:len(l.pending)-1]
				break
			}
		}
	}
	c.state = closed
	c.err = err
	c.pending = nil
	c.tx = byteBuffer{}
	delete(c.stack.conns, connKey{c.local, c.remote})
	c.stack.release(c.local)
	c.notify()
}

func (c *Conn) enterTimeWait(now time.Time) {
	c.state = timeWait
	c.timeWaitEnd = now.Add(timeWaitDuration)
	c.pending = nil
	c.tx = byteBuffer{}
	c.notify()
}

func (c *Conn) pump(now time.Time) {
	if c.state != established && c.state != closeWait {
		return
	}
	if c.inflight != 0 {
		return
	}
	if c.peerWindow == 0 {
		if (c.tx.size != 0 || c.writeClosed) && c.probeAt.IsZero() {
			c.probeInterval = c.rto
			c.probeAt = now.Add(c.probeInterval)
		}
		return
	}
	if c.tx.size != 0 {
		c.inflight = min(c.tx.size, c.peerMss, mss, int(c.peerWindow))
		seq := c.sndNxt
		c.sndNxt += uint32(c.inflight)
		c.sentAt, c.rttValid = now, true
		c.retransmitAt = now.Add(c.rto)
		c.emit(protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_PSH, seq, c.tx.peek(c.inflight), true)
	} else if c.writeClosed && !c.finSent {
		c.finSent = true
		c.finSeq = c.sndNxt
		c.sndNxt++
		if c.state == closeWait {
			c.state = lastAck
		} else {
			c.state = finWait1
		}
		c.retransmitAt = now.Add(c.rto)
		c.emit(protocol.TCP_FLAGS_FIN|protocol.TCP_FLAGS_ACK, c.finSeq, nil, true)
	}
}

func (c *Conn) input(p protocol.TcpPkt, now time.Time) {
	if c.state == synSent {
		if p.Flags&protocol.TCP_FLAGS_ACK != 0 && p.AckNum != c.sndNxt {
			if p.Flags&protocol.TCP_FLAGS_RST == 0 {
				c.stack.reset(connKey{c.local, c.remote}, p)
			}
			return
		}
		if p.Flags&protocol.TCP_FLAGS_RST != 0 {
			if p.Flags&protocol.TCP_FLAGS_ACK != 0 {
				c.fail(errors.New("tcp: connection refused"), false)
			}
			return
		}
		if p.Flags&(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_FIN) != protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK {
			return
		}
		c.sndUna = p.AckNum
		c.irs, c.rcvNxt = p.SeqNum, p.SeqNum+1
		if p.Mss != 0 {
			c.peerMss = int(p.Mss)
		}
		c.peerWindow, c.wlSeq, c.wlAck = uint32(p.Window), p.SeqNum, p.AckNum
		c.state = established
		c.pending = nil
		c.ack()
		c.notify()
		return
	}
	if c.state == synReceived && p.Flags&(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_RST) == protocol.TCP_FLAGS_SYN && p.SeqNum == c.irs {
		c.emit(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK, c.iss, nil, true)
		return
	}
	if c.state == timeWait {
		if p.Flags&protocol.TCP_FLAGS_RST != 0 {
			return
		}
		if p.Flags&protocol.TCP_FLAGS_FIN != 0 && p.SeqNum+uint32(len(p.Payload))+1 == c.rcvNxt {
			c.timeWaitEnd = now.Add(timeWaitDuration)
			c.ack()
		} else if p.Flags&protocol.TCP_FLAGS_ACK == 0 || len(p.Payload) != 0 {
			c.ack()
		}
		return
	}
	if p.Flags&(protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_RST) == protocol.TCP_FLAGS_SYN|protocol.TCP_FLAGS_ACK && p.SeqNum == c.irs && p.AckNum == c.iss+1 {
		c.ack()
		return
	}
	window := len(c.rx.data) - c.rx.size
	receiveRight := c.rcvNxt + uint32(window)
	offset := int64(int32(p.SeqNum - c.rcvNxt))
	segLen := len(p.Payload)
	if p.Flags&protocol.TCP_FLAGS_SYN != 0 {
		segLen++
	}
	if p.Flags&protocol.TCP_FLAGS_FIN != 0 {
		segLen++
	}
	acceptable := (segLen == 0 && offset == 0) || (window > 0 && offset < int64(window) &&
		((segLen == 0 && offset >= 0) || (segLen > 0 && offset+int64(segLen) > 0)))
	if p.Flags&protocol.TCP_FLAGS_RST != 0 {
		if p.SeqNum == c.rcvNxt {
			c.fail(errors.New("tcp: connection reset by peer"), false)
		} else if acceptable {
			c.ack()
		}
		return
	}
	if !acceptable || p.Flags&protocol.TCP_FLAGS_SYN != 0 {
		c.ack()
		return
	}
	if p.Flags&protocol.TCP_FLAGS_ACK == 0 {
		return
	}
	if seqLess(c.sndNxt, p.AckNum) {
		c.ack()
		return
	}
	if c.state == synReceived {
		if p.AckNum != c.sndNxt || p.SeqNum != c.rcvNxt {
			return
		}
		c.state = established
		c.sndUna = p.AckNum
		c.pending = nil
		l := c.listener
		l.incomplete--
		l.pending = append(l.pending, c)
		close(l.event)
		l.event = make(chan struct{})
	}
	if !seqLess(p.AckNum, c.sndUna) && (seqLess(c.wlSeq, p.SeqNum) || (c.wlSeq == p.SeqNum && !seqLess(p.AckNum, c.wlAck))) {
		oldWindow := c.peerWindow
		c.peerWindow, c.wlSeq, c.wlAck = uint32(p.Window), p.SeqNum, p.AckNum
		if c.peerWindow > 0 {
			c.probeAt = time.Time{}
			if oldWindow == 0 && c.inflight > 0 {
				c.retransmitAt = now
			}
		}
	}
	if seqLess(c.sndUna, p.AckNum) {
		n := int(p.AckNum - c.sndUna)
		if c.inflight > 0 {
			consumed := min(n, c.inflight)
			c.tx.discard(consumed)
			c.inflight -= consumed
			if c.rttValid {
				rtt := max(now.Sub(c.sentAt), time.Microsecond)
				if c.srtt == 0 {
					c.srtt, c.rttvar = rtt, rtt/2
				} else {
					delta := c.srtt - rtt
					if delta < 0 {
						delta = -delta
					}
					c.rttvar = (3*c.rttvar + delta) / 4
					c.srtt = (7*c.srtt + rtt) / 8
				}
				c.rto = min(max(c.srtt+max(20*time.Millisecond, 4*c.rttvar), time.Second), 60*time.Second)
				c.rttValid = false
			}
		}
		c.sndUna, c.progress = p.AckNum, now
		c.retransmitAt = now.Add(c.rto)
		c.pending = nil
		c.notify()
		if c.finSent && p.AckNum == c.finSeq+1 {
			switch c.state {
			case finWait1:
				c.state = finWait2
			case closing:
				c.enterTimeWait(now)
			case lastAck:
				c.fail(nil, false)
				return
			default:
			}
		}
	}
	finSeq := p.SeqNum + uint32(len(p.Payload))
	data := p.Payload
	if offset < 0 {
		trim := min(int(-offset), len(data))
		data = data[trim:]
		offset += int64(trim)
	}
	if offset == 0 && len(data) != 0 && !c.recvFin {
		if c.appClosed {
			c.fail(errors.New("tcp: data received after close"), true)
			return
		}
		n := c.rx.write(data)
		c.rcvNxt += uint32(n)
		c.notify()
	}
	if p.Flags&protocol.TCP_FLAGS_FIN != 0 && !c.recvFin && finSeq == c.rcvNxt && window > 0 && seqLess(finSeq, receiveRight) {
		c.recvFin = true
		c.rcvNxt++
		switch c.state {
		case established:
			c.state = closeWait
		case finWait1:
			c.state = closing
		case finWait2:
			c.enterTimeWait(now)
		default:
		}
		c.notify()
	}
	if len(p.Payload) != 0 || p.Flags&protocol.TCP_FLAGS_FIN != 0 {
		c.ack()
	}
	c.pump(now)
}

func (c *Conn) tick(now time.Time) {
	if c.state == closed {
		return
	}
	if c.state == timeWait {
		if !now.Before(c.timeWaitEnd) {
			c.fail(nil, false)
		}
		return
	}
	if c.state == synSent || c.state == synReceived {
		if !now.Before(c.handshakeEnd) {
			c.fail(ErrTimeout, true)
			return
		}
	} else {
		if !c.closeEnd.IsZero() && !now.Before(c.closeEnd) {
			c.fail(ErrTimeout, true)
			return
		}
		if c.tx.size != 0 && !c.progress.IsZero() && now.Sub(c.progress) >= userTimeout {
			c.fail(ErrTimeout, true)
			return
		}
	}
	if c.pending != nil && !now.Before(c.pendingAt) {
		out := *c.pending
		c.pending = nil
		c.pendingAt = now.Add(100 * time.Millisecond)
		if seqLess(c.sndUna, out.end) {
			c.stack.queue(out)
		}
		return
	}
	if c.state == synSent || c.state == synReceived {
		return
	}
	if c.peerWindow == 0 && (c.tx.size != 0 || (c.writeClosed && !c.finSent)) {
		if c.probeAt.IsZero() {
			c.probeInterval = c.rto
			c.probeAt = now.Add(c.probeInterval)
		}
		if !now.Before(c.probeAt) {
			c.rttValid = false
			if c.tx.size != 0 {
				if c.inflight == 0 {
					c.inflight = 1
					c.sndNxt++
				}
				c.emit(protocol.TCP_FLAGS_ACK, c.sndUna, c.tx.peek(1), true)
			} else {
				c.emit(protocol.TCP_FLAGS_ACK, c.sndNxt-1, []byte{0}, false)
			}
			c.probeInterval = min(c.probeInterval*2, 60*time.Second)
			c.probeAt = now.Add(c.probeInterval)
		}
		return
	}
	if c.sndUna != c.sndNxt && !now.Before(c.retransmitAt) {
		c.rttValid = false
		c.rto = min(c.rto*2, 60*time.Second)
		c.retransmitAt = now.Add(c.rto)
		if c.inflight != 0 {
			c.emit(protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_PSH, c.sndUna, c.tx.peek(c.inflight), true)
		} else if c.finSent {
			c.emit(protocol.TCP_FLAGS_ACK|protocol.TCP_FLAGS_FIN, c.finSeq, nil, true)
		}
	}
	c.pump(now)
}
