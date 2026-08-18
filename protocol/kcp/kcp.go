package kcp

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	IKCP_RTO_NDL     = 30  // no delay min rto
	IKCP_RTO_MIN     = 100 // normal min rto
	IKCP_RTO_DEF     = 200
	IKCP_RTO_MAX     = 60000
	IKCP_CMD_PUSH    = 81 // cmd: push data
	IKCP_CMD_ACK     = 82 // cmd: ack
	IKCP_CMD_WASK    = 83 // cmd: window probe (ask)
	IKCP_CMD_WINS    = 84 // cmd: window size (tell)
	IKCP_ASK_SEND    = 1  // need to send IKCP_CMD_WASK
	IKCP_ASK_TELL    = 2  // need to send IKCP_CMD_WINS
	IKCP_WND_SND     = 32
	IKCP_WND_RCV     = 32
	IKCP_MTU_DEF     = 1400
	IKCP_ACK_FAST    = 3
	IKCP_INTERVAL    = 100
	IKCP_DEADLINK    = 20
	IKCP_THRESH_INIT = 2
	IKCP_THRESH_MIN  = 2
	IKCP_PROBE_INIT  = 7000   // 7 secs to probe window size
	IKCP_PROBE_LIMIT = 120000 // up to 120 secs to probe window
	IKCP_SN_OFFSET   = 16

	// ByteCheckModeDisabled 关闭 Halo 的 KCP 载荷校验扩展
	ByteCheckModeDisabled = -1
	// ByteCheckModeZero 固定使用四个零字节填充 KCP 数据分片校验字段
	ByteCheckModeZero = 0
	// ByteCheckModeCRC32 使用 IEEE CRC-32 校验 KCP 数据分片
	ByteCheckModeCRC32 = 1
	// ByteCheckModeXXH3 使用 XXH3 低 32 位校验 KCP 数据分片
	ByteCheckModeXXH3 = 2

	// IKCP_BASE_OVERHEAD 是 Halo 64 位 conv 使用的基础 KCP 头部长度
	IKCP_BASE_OVERHEAD = 28
	// IKCP_BYTE_CHECK_OVERHEAD 是 ByteCheck 扩展字段长度
	IKCP_BYTE_CHECK_OVERHEAD = 4
)

// IKCP_OVERHEAD 保存当前进程配置使用的 KCP 头部长度
var IKCP_OVERHEAD = IKCP_BASE_OVERHEAD

var (
	byteCheckConfigMu       sync.Mutex
	byteCheckConfigFrozen   bool
	byteCheckModeEnable     bool
	byteCheckMode           = ByteCheckModeDisabled
	errByteCheckModeLocked  = errors.New("byte check mode is locked after KCP initialization")
	errByteCheckModeInvalid = errors.New("unsupported byte check mode")
)

// SetByteCheckMode 配置 Halo 扩展的 KCP 载荷校验模式
func SetByteCheckMode(mode int) error {
	if mode != ByteCheckModeDisabled && !byteCheckModeSupported(mode) {
		return errByteCheckModeInvalid
	}

	byteCheckConfigMu.Lock()
	defer byteCheckConfigMu.Unlock()
	if byteCheckConfigFrozen {
		if mode == byteCheckMode {
			return nil
		}
		return errByteCheckModeLocked
	}

	byteCheckMode = mode
	byteCheckModeEnable = mode != ByteCheckModeDisabled
	IKCP_OVERHEAD = IKCP_BASE_OVERHEAD
	if byteCheckModeEnable {
		IKCP_OVERHEAD += IKCP_BYTE_CHECK_OVERHEAD
	}
	return nil
}

// monotonic reference time point
var refTime = time.Now()

// currentMs returns current elapsed monotonic milliseconds since program startup
func currentMs() uint32 { return uint32(time.Since(refTime) / time.Millisecond) }

// output_callback is a prototype which ought capture conn and call conn.Write
type output_callback func(buf []byte, size int)

func _imin_(a, b uint32) uint32 {
	if a <= b {
		return a
	}
	return b
}

func _imax_(a, b uint32) uint32 {
	if a >= b {
		return a
	}
	return b
}

func _ibound_(lower, middle, upper uint32) uint32 {
	return _imin_(_imax_(lower, middle), upper)
}

func _itimediff(later, earlier uint32) int32 {
	return (int32)(later - earlier)
}

// segment defines a KCP segment
type segment struct {
	conv     uint64 // KCP组合会话id
	cmd      uint8
	frg      uint8
	wnd      uint16
	ts       uint32
	sn       uint32
	una      uint32
	rto      uint32
	xmit     uint32
	resendts uint32
	fastack  uint32
	acked    uint32 // mark if the seg has acked
	data     []byte
}

// encode 将 KCP 分片头直接编码到目标缓冲区
func (seg *segment) encode(ptr []byte) []byte {
	_ = ptr[IKCP_OVERHEAD-1]
	// Halo 将会话 ID 和 KCP 会话号组合为 64 位 conv 写入分片头部
	binary.LittleEndian.PutUint64(ptr, seg.conv)
	ptr[8] = seg.cmd
	ptr[9] = seg.frg
	binary.LittleEndian.PutUint16(ptr[10:], seg.wnd)
	binary.LittleEndian.PutUint32(ptr[12:], seg.ts)
	binary.LittleEndian.PutUint32(ptr[16:], seg.sn)
	binary.LittleEndian.PutUint32(ptr[20:], seg.una)
	binary.LittleEndian.PutUint32(ptr[24:], uint32(len(seg.data)))
	if byteCheckModeEnable {
		// 仅数据分片计算载荷校验 控制分片保留零值占位
		if seg.cmd == IKCP_CMD_PUSH {
			binary.LittleEndian.PutUint32(ptr[28:], byte_check_hash(seg.data))
		} else {
			binary.LittleEndian.PutUint32(ptr[28:], 0)
		}
	}
	atomic.AddUint64(&DefaultSnmp.OutSegs, 1)
	return ptr[IKCP_OVERHEAD:]
}

// segmentHeap 按序列号保存尚未连续到达的接收分片
type segmentHeap struct {
	segments []segment
	marks    map[uint32]struct{}
}

// newSegmentHeap 创建带重复序列号索引的最小堆
func newSegmentHeap() *segmentHeap {
	return new(segmentHeap)
}

// Len 返回乱序分片数量
func (h *segmentHeap) Len() int {
	return len(h.segments)
}

// less 使用 KCP 序列号回绕语义比较两个分片
func (h *segmentHeap) less(i, j int) bool {
	return _itimediff(h.segments[j].sn, h.segments[i].sn) > 0
}

// swap 交换堆中的两个分片
func (h *segmentHeap) swap(i, j int) {
	h.segments[i], h.segments[j] = h.segments[j], h.segments[i]
}

// Push 向堆中加入一个分片并记录序列号
func (h *segmentHeap) Push(seg segment) {
	if h.marks == nil {
		h.marks = make(map[uint32]struct{})
	}
	h.segments = append(h.segments, seg)
	h.marks[seg.sn] = struct{}{}

	index := len(h.segments) - 1
	for index > 0 {
		parent := (index - 1) / 2
		if !h.less(index, parent) {
			break
		}
		h.swap(index, parent)
		index = parent
	}
}

// Pop 移除并返回序列号最靠前的分片
func (h *segmentHeap) Pop() (segment, bool) {
	if len(h.segments) == 0 {
		return segment{}, false
	}

	result := h.segments[0]
	last := len(h.segments) - 1
	tail := h.segments[last]
	h.segments[last] = segment{}
	h.segments = h.segments[:last]
	delete(h.marks, result.sn)

	if last > 0 {
		h.segments[0] = tail
		index := 0
		for {
			left := index*2 + 1
			if left >= len(h.segments) {
				break
			}
			smallest := left
			right := left + 1
			if right < len(h.segments) && h.less(right, left) {
				smallest = right
			}
			if !h.less(smallest, index) {
				break
			}
			h.swap(index, smallest)
			index = smallest
		}
	}
	return result, true
}

// Has 判断指定序列号是否已在乱序缓冲区中
func (h *segmentHeap) Has(sn uint32) bool {
	_, exists := h.marks[sn]
	return exists
}

// KCP defines a single KCP connection
type KCP struct {
	conv                                   uint64 // KCP组合会话id
	mtu, mss, state                        uint32
	snd_una, snd_nxt, rcv_nxt              uint32
	ssthresh                               uint32
	rx_rttvar, rx_srtt                     int32
	rx_rto, rx_minrto                      uint32
	snd_wnd, rcv_wnd, rmt_wnd, cwnd, probe uint32
	interval, ts_flush                     uint32
	nodelay, updated                       uint32
	ts_probe, probe_wait                   uint32
	dead_link, incr                        uint32

	fastresend     int32
	nocwnd, stream int32

	snd_queue *ringBuffer[segment]
	rcv_queue *ringBuffer[segment]
	snd_buf   *ringBuffer[segment]
	rcv_buf   *segmentHeap

	acklist []ackItem

	buffer   []byte
	reserved int
	output   output_callback
}

type ackItem struct {
	sn uint32
	ts uint32
}

// NewKCP create a new kcp state machine
//
// 'conv' must be equal in the connection peers, or else data will be silently rejected.
//
// 'output' function will be called whenever these is data to be sent on wire.
func NewKCP(conv uint64, output output_callback) *KCP {
	// ByteCheck 会改变线上头部布局 首个实例创建后禁止再次切换
	byteCheckConfigMu.Lock()
	byteCheckConfigFrozen = true
	overhead := IKCP_OVERHEAD
	byteCheckConfigMu.Unlock()

	kcp := new(KCP)
	kcp.conv = conv
	kcp.snd_wnd = IKCP_WND_SND
	kcp.rcv_wnd = IKCP_WND_RCV
	kcp.rmt_wnd = IKCP_WND_RCV
	kcp.mtu = IKCP_MTU_DEF
	kcp.mss = kcp.mtu - uint32(overhead)
	kcp.buffer = make([]byte, kcp.mtu)
	kcp.rx_rto = IKCP_RTO_DEF
	kcp.rx_minrto = IKCP_RTO_MIN
	kcp.interval = IKCP_INTERVAL
	kcp.ts_flush = IKCP_INTERVAL
	kcp.ssthresh = IKCP_THRESH_INIT
	kcp.dead_link = IKCP_DEADLINK
	kcp.output = output
	kcp.snd_queue = newRingBuffer[segment](IKCP_WND_SND * 2)
	kcp.rcv_queue = newRingBuffer[segment](IKCP_WND_RCV * 2)
	kcp.snd_buf = newRingBuffer[segment](IKCP_WND_SND * 2)
	kcp.rcv_buf = newSegmentHeap()
	return kcp
}

// newSegment creates a KCP segment
func (kcp *KCP) newSegment(size int) (seg segment) {
	seg.data = xmitBuf.Get(size)
	return
}

// delSegment recycles a KCP segment
func (kcp *KCP) delSegment(seg *segment) {
	if seg.data != nil {
		xmitBuf.Put(seg.data)
		seg.data = nil
	}
}

// ReserveBytes keeps n bytes untouched from the beginning of the buffer,
// the output_callback function should be aware of this.
//
// Return false if n >= mss
func (kcp *KCP) ReserveBytes(n int) bool {
	available := int(kcp.mtu) - IKCP_OVERHEAD
	if n < 0 || n >= available {
		return false
	}
	kcp.reserved = n
	kcp.mss = kcp.mtu - uint32(IKCP_OVERHEAD) - uint32(n)
	return true
}

// PeekSize checks the size of next message in the recv queue
func (kcp *KCP) PeekSize() (length int) {
	seg, ok := kcp.rcv_queue.Peek()
	if !ok {
		return -1
	}

	if seg.frg == 0 {
		return len(seg.data)
	}

	if kcp.rcv_queue.Len() < int(seg.frg+1) {
		return -1
	}

	kcp.rcv_queue.ForEach(func(seg *segment) bool {
		length += len(seg.data)
		return seg.frg != 0
	})
	return
}

// Receive data from kcp state machine
//
// Return number of bytes read.
//
// Return -1 when there is no readable data.
//
// Return -2 if len(buffer) is smaller than kcp.PeekSize().
func (kcp *KCP) Recv(buffer []byte) (n int) {
	peeksize := kcp.PeekSize()
	if peeksize < 0 {
		return -1
	}

	if peeksize > len(buffer) {
		return -2
	}

	var fast_recover bool
	if kcp.rcv_queue.Len() >= int(kcp.rcv_wnd) {
		fast_recover = true
	}

	// merge fragment
	for {
		seg, ok := kcp.rcv_queue.Pop()
		if !ok {
			break
		}
		copy(buffer, seg.data)
		buffer = buffer[len(seg.data):]
		n += len(seg.data)
		kcp.delSegment(&seg)
		if seg.frg == 0 {
			break
		}
	}

	// move available data from rcv_buf -> rcv_queue
	for kcp.rcv_buf.Len() > 0 {
		seg, _ := kcp.rcv_buf.Pop()
		if seg.sn == kcp.rcv_nxt && kcp.rcv_queue.Len() < int(kcp.rcv_wnd) {
			kcp.rcv_queue.Push(seg)
			kcp.rcv_nxt++
		} else {
			kcp.rcv_buf.Push(seg)
			break
		}
	}

	// fast recover
	if kcp.rcv_queue.Len() < int(kcp.rcv_wnd) && fast_recover {
		// ready to send back IKCP_CMD_WINS in ikcp_flush
		// tell remote my window size
		kcp.probe |= IKCP_ASK_TELL
	}
	return
}

// Send is user/upper level send, returns below zero for error
func (kcp *KCP) Send(buffer []byte) int {
	var count int
	if len(buffer) == 0 {
		return -1
	}

	// append to previous segment in streaming mode (if possible)
	if kcp.stream != 0 {
		if seg, ok := kcp.snd_queue.Last(); ok {
			if len(seg.data) < int(kcp.mss) {
				capacity := int(kcp.mss) - len(seg.data)
				extend := capacity
				if len(buffer) < capacity {
					extend = len(buffer)
				}

				// grow slice, the underlying cap is guaranteed to
				// be larger than kcp.mss
				oldlen := len(seg.data)
				seg.data = seg.data[:oldlen+extend]
				copy(seg.data[oldlen:], buffer)
				buffer = buffer[extend:]
			}
		}

		if len(buffer) == 0 {
			return 0
		}
	}

	count = (len(buffer)-1)/int(kcp.mss) + 1
	if kcp.stream == 0 && count > 255 {
		return -2
	}

	for i := 0; i < count; i++ {
		var size int
		if len(buffer) > int(kcp.mss) {
			size = int(kcp.mss)
		} else {
			size = len(buffer)
		}
		seg := kcp.newSegment(size)
		copy(seg.data, buffer[:size])
		if kcp.stream == 0 { // message mode
			seg.frg = uint8(count - i - 1)
		} else { // stream mode
			seg.frg = 0
		}
		kcp.snd_queue.Push(seg)
		buffer = buffer[size:]
	}
	return 0
}

func (kcp *KCP) update_ack(rtt int32) {
	// https://tools.ietf.org/html/rfc6298
	var rto uint32
	if kcp.rx_srtt == 0 {
		kcp.rx_srtt = rtt
		kcp.rx_rttvar = rtt >> 1
	} else {
		delta := rtt - kcp.rx_srtt
		kcp.rx_srtt += delta >> 3
		if delta < 0 {
			delta = -delta
		}
		if rtt < kcp.rx_srtt-kcp.rx_rttvar {
			// if the new RTT sample is below the bottom of the range of
			// what an RTT measurement is expected to be.
			// give an 8x reduced weight versus its normal weighting
			kcp.rx_rttvar += (delta - kcp.rx_rttvar) >> 5
		} else {
			kcp.rx_rttvar += (delta - kcp.rx_rttvar) >> 2
		}
	}
	rto = uint32(kcp.rx_srtt) + _imax_(kcp.interval, uint32(kcp.rx_rttvar)<<2)
	kcp.rx_rto = _ibound_(kcp.rx_minrto, rto, IKCP_RTO_MAX)
}

func (kcp *KCP) shrink_buf() {
	if seg, ok := kcp.snd_buf.Peek(); ok {
		kcp.snd_una = seg.sn
	} else {
		kcp.snd_una = kcp.snd_nxt
	}
}

func (kcp *KCP) parse_ack(sn uint32) {
	if _itimediff(sn, kcp.snd_una) < 0 || _itimediff(sn, kcp.snd_nxt) >= 0 {
		return
	}

	kcp.snd_buf.ForEach(func(seg *segment) bool {
		if sn == seg.sn {
			// mark and free space, but leave the segment here,
			// and wait until `una` to delete this, then we don't
			// have to shift the segments behind forward,
			// which is an expensive operation for large window
			seg.acked = 1
			kcp.delSegment(seg)
			return false
		}
		if _itimediff(sn, seg.sn) < 0 {
			return false
		}
		return true
	})
}

func (kcp *KCP) parse_fastack(sn, ts uint32) int {
	shouldFastAck := 0
	if _itimediff(sn, kcp.snd_una) < 0 || _itimediff(sn, kcp.snd_nxt) >= 0 {
		return shouldFastAck
	}

	kcp.snd_buf.ForEach(func(seg *segment) bool {
		if _itimediff(sn, seg.sn) < 0 {
			return false
		} else if sn != seg.sn && _itimediff(seg.ts, ts) <= 0 {
			if seg.fastack != ^uint32(0) {
				seg.fastack++
				if seg.fastack >= uint32(kcp.fastresend) {
					shouldFastAck = 1
				}
			}
		}
		return true
	})
	return shouldFastAck
}

func (kcp *KCP) parse_una(una uint32) int {
	count := 0
	kcp.snd_buf.ForEach(func(seg *segment) bool {
		if _itimediff(una, seg.sn) > 0 {
			kcp.delSegment(seg)
			count++
		} else {
			return false
		}
		return true
	})
	kcp.snd_buf.Discard(count)
	return count
}

// ack append
func (kcp *KCP) ack_push(sn, ts uint32) {
	kcp.acklist = append(kcp.acklist, ackItem{sn, ts})
}

// returns true if data has repeated
func (kcp *KCP) parse_data(newseg segment) bool {
	sn := newseg.sn
	if _itimediff(sn, kcp.rcv_nxt+kcp.rcv_wnd) >= 0 ||
		_itimediff(sn, kcp.rcv_nxt) < 0 {
		return true
	}

	repeat := false
	if !kcp.rcv_buf.Has(sn) {
		// replicate the content if it's new
		dataCopy := xmitBuf.Get(len(newseg.data))
		copy(dataCopy, newseg.data)
		newseg.data = dataCopy
		kcp.rcv_buf.Push(newseg)
	} else {
		repeat = true
	}

	// move available data from rcv_buf -> rcv_queue
	for kcp.rcv_buf.Len() > 0 {
		seg, _ := kcp.rcv_buf.Pop()
		if seg.sn == kcp.rcv_nxt && kcp.rcv_queue.Len() < int(kcp.rcv_wnd) {
			kcp.rcv_queue.Push(seg)
			kcp.rcv_nxt++
		} else {
			kcp.rcv_buf.Push(seg)
			break
		}
	}

	return repeat
}

// Input a packet into kcp state machine.
//
// 'regular' indicates it's a real data packet from remote, and it means it's not generated from ReedSolomon
// codecs.
//
// 'ackNoDelay' will trigger immediate ACK, but surely it will not be efficient in bandwidth
func (kcp *KCP) Input(data []byte, regular, ackNoDelay bool) int {
	snd_una := kcp.snd_una
	if len(data) < IKCP_OVERHEAD {
		return -1
	}

	var latest uint32 // the latest ack packet
	var updateRTT int
	var inSegs uint64
	var flushSegments int

	for {
		if len(data) < int(IKCP_OVERHEAD) {
			break
		}

		_ = data[IKCP_OVERHEAD-1]
		conv := binary.LittleEndian.Uint64(data)
		cmd := data[8]
		frg := data[9]
		wnd := binary.LittleEndian.Uint16(data[10:])
		ts := binary.LittleEndian.Uint32(data[12:])
		sn := binary.LittleEndian.Uint32(data[16:])
		una := binary.LittleEndian.Uint32(data[20:])
		length := binary.LittleEndian.Uint32(data[24:])
		var hash uint32
		if byteCheckModeEnable {
			hash = binary.LittleEndian.Uint32(data[28:])
		}
		data = data[IKCP_OVERHEAD:]

		if conv != kcp.conv {
			return -1
		}

		if uint64(length) > uint64(len(data)) {
			return -2
		}

		if cmd != IKCP_CMD_PUSH && cmd != IKCP_CMD_ACK &&
			cmd != IKCP_CMD_WASK && cmd != IKCP_CMD_WINS {
			return -3
		}
		if byteCheckModeEnable && cmd == IKCP_CMD_PUSH {
			// 先验证声明长度再计算哈希 避免畸形报文触发越界切片
			if hash != byte_check_hash(data[:int(length)]) {
				return -4
			}
		}

		// only trust window updates from regular packets. i.e: latest update
		if regular {
			kcp.rmt_wnd = uint32(wnd)
		}
		if kcp.parse_una(una) > 0 {
			flushSegments |= 1
		}
		kcp.shrink_buf()

		if cmd == IKCP_CMD_ACK {
			kcp.parse_ack(sn)
			flushSegments |= kcp.parse_fastack(sn, ts)
			updateRTT |= 1
			latest = ts
		} else if cmd == IKCP_CMD_PUSH {
			repeat := true
			if _itimediff(sn, kcp.rcv_nxt+kcp.rcv_wnd) < 0 {
				kcp.ack_push(sn, ts)
				if _itimediff(sn, kcp.rcv_nxt) >= 0 {
					var seg segment
					seg.conv = conv
					seg.cmd = cmd
					seg.frg = frg
					seg.wnd = wnd
					seg.ts = ts
					seg.sn = sn
					seg.una = una
					seg.data = data[:length] // delayed data copying
					repeat = kcp.parse_data(seg)
				}
			}
			if regular && repeat {
				atomic.AddUint64(&DefaultSnmp.RepeatSegs, 1)
			}
		} else if cmd == IKCP_CMD_WASK {
			// ready to send back IKCP_CMD_WINS in Ikcp_flush
			// tell remote my window size
			kcp.probe |= IKCP_ASK_TELL
		} else if cmd == IKCP_CMD_WINS {
			// do nothing
		} else {
			return -3
		}

		inSegs++
		data = data[length:]
	}
	atomic.AddUint64(&DefaultSnmp.InSegs, inSegs)

	// update rtt with the latest ts
	// ignore the FEC packet
	if updateRTT != 0 && regular {
		current := currentMs()
		if _itimediff(current, latest) >= 0 {
			kcp.update_ack(_itimediff(current, latest))
		}
	}

	// cwnd update when packet arrived
	if kcp.nocwnd == 0 {
		if _itimediff(kcp.snd_una, snd_una) > 0 {
			if kcp.cwnd < kcp.rmt_wnd {
				mss := kcp.mss
				if kcp.cwnd < kcp.ssthresh {
					kcp.cwnd++
					kcp.incr += mss
				} else {
					if kcp.incr < mss {
						kcp.incr = mss
					}
					kcp.incr += (mss*mss)/kcp.incr + (mss / 16)
					if (kcp.cwnd+1)*mss <= kcp.incr {
						if mss > 0 {
							kcp.cwnd = (kcp.incr + mss - 1) / mss
						} else {
							kcp.cwnd = kcp.incr + mss - 1
						}
					}
				}
				if kcp.cwnd > kcp.rmt_wnd {
					kcp.cwnd = kcp.rmt_wnd
					kcp.incr = kcp.rmt_wnd * mss
				}
			}
		}
	}

	if flushSegments != 0 {
		// 发送窗口前移或达到快速重传条件时立即推进发送 避免等待下一个调度周期
		kcp.flush(false)
	} else if len(kcp.acklist) >= int(kcp.mtu/uint32(IKCP_OVERHEAD)) {
		// ACK 已能装满一个 MTU 时立即回传 避免低时延链路被周期调度限制
		kcp.flush(true)
	} else if ackNoDelay && len(kcp.acklist) > 0 { // ack immediately
		kcp.flush(true)
	}
	return 0
}

func (kcp *KCP) wnd_unused() uint16 {
	if kcp.rcv_queue.Len() < int(kcp.rcv_wnd) {
		return uint16(int(kcp.rcv_wnd) - kcp.rcv_queue.Len())
	}
	return 0
}

// flush pending data
func (kcp *KCP) flush(ackOnly bool) uint32 {
	var seg segment
	seg.conv = kcp.conv
	seg.cmd = IKCP_CMD_ACK
	seg.wnd = kcp.wnd_unused()
	seg.una = kcp.rcv_nxt

	buffer := kcp.buffer
	ptr := buffer[kcp.reserved:] // keep n bytes untouched

	// makeSpace makes room for writing
	makeSpace := func(space int) {
		size := len(buffer) - len(ptr)
		if size+space > int(kcp.mtu) {
			kcp.output(buffer, size)
			ptr = buffer[kcp.reserved:]
		}
	}

	// flush bytes in buffer if there is any
	flushBuffer := func() {
		size := len(buffer) - len(ptr)
		if size > kcp.reserved {
			kcp.output(buffer, size)
		}
	}

	// flush acknowledges
	for i, ack := range kcp.acklist {
		makeSpace(IKCP_OVERHEAD)
		// filter jitters caused by bufferbloat
		if _itimediff(ack.sn, kcp.rcv_nxt) >= 0 || len(kcp.acklist)-1 == i {
			seg.sn, seg.ts = ack.sn, ack.ts
			ptr = seg.encode(ptr)
		}
	}
	kcp.acklist = kcp.acklist[0:0]

	if ackOnly { // flash remain ack segments
		flushBuffer()
		return kcp.interval
	}

	// probe window size (if remote window size equals zero)
	if kcp.rmt_wnd == 0 {
		current := currentMs()
		if kcp.probe_wait == 0 {
			kcp.probe_wait = IKCP_PROBE_INIT
			kcp.ts_probe = current + kcp.probe_wait
		} else {
			if _itimediff(current, kcp.ts_probe) >= 0 {
				if kcp.probe_wait < IKCP_PROBE_INIT {
					kcp.probe_wait = IKCP_PROBE_INIT
				}
				kcp.probe_wait += kcp.probe_wait / 2
				if kcp.probe_wait > IKCP_PROBE_LIMIT {
					kcp.probe_wait = IKCP_PROBE_LIMIT
				}
				kcp.ts_probe = current + kcp.probe_wait
				kcp.probe |= IKCP_ASK_SEND
			}
		}
	} else {
		kcp.ts_probe = 0
		kcp.probe_wait = 0
	}

	// flush window probing commands
	if (kcp.probe & IKCP_ASK_SEND) != 0 {
		seg.cmd = IKCP_CMD_WASK
		makeSpace(IKCP_OVERHEAD)
		ptr = seg.encode(ptr)
	}

	// flush window probing commands
	if (kcp.probe & IKCP_ASK_TELL) != 0 {
		seg.cmd = IKCP_CMD_WINS
		makeSpace(IKCP_OVERHEAD)
		ptr = seg.encode(ptr)
	}

	kcp.probe = 0

	// calculate window size
	cwnd := _imin_(kcp.snd_wnd, kcp.rmt_wnd)
	if kcp.nocwnd == 0 {
		cwnd = _imin_(kcp.cwnd, cwnd)
	}

	// sliding window, controlled by snd_nxt && sna_una+cwnd
	newSegsCount := 0
	for {
		if _itimediff(kcp.snd_nxt, kcp.snd_una+cwnd) >= 0 {
			break
		}
		newseg, ok := kcp.snd_queue.Pop()
		if !ok {
			break
		}
		newseg.conv = kcp.conv
		newseg.cmd = IKCP_CMD_PUSH
		newseg.sn = kcp.snd_nxt
		kcp.snd_buf.Push(newseg)
		kcp.snd_nxt++
		newSegsCount++
	}

	// calculate resent
	resent := uint32(kcp.fastresend)
	if kcp.fastresend <= 0 {
		resent = 0xffffffff
	}

	// check for retransmissions
	current := currentMs()
	var change, lostSegs, fastRetransSegs, earlyRetransSegs uint64
	minrto := int32(kcp.interval)

	kcp.snd_buf.ForEach(func(segment *segment) bool {
		needsend := false
		if segment.acked == 1 {
			return true
		}
		if segment.xmit == 0 { // initial transmit
			needsend = true
			segment.rto = kcp.rx_rto
			segment.resendts = current + segment.rto
		} else if segment.fastack >= resent && segment.fastack != ^uint32(0) { // fast retransmit
			needsend = true
			segment.fastack = ^uint32(0)
			segment.rto = kcp.rx_rto
			segment.resendts = current + segment.rto
			change++
			fastRetransSegs++
		} else if segment.fastack > 0 && segment.fastack != ^uint32(0) && newSegsCount == 0 { // early retransmit
			needsend = true
			segment.fastack = ^uint32(0)
			segment.rto = kcp.rx_rto
			segment.resendts = current + segment.rto
			change++
			earlyRetransSegs++
		} else if _itimediff(current, segment.resendts) >= 0 { // RTO
			needsend = true
			if kcp.nodelay == 0 {
				segment.rto += kcp.rx_rto
			} else {
				segment.rto += kcp.rx_rto / 2
			}
			segment.fastack = 0
			segment.resendts = current + segment.rto
			lostSegs++
		}

		if needsend {
			current = currentMs()
			segment.xmit++
			segment.ts = current
			segment.wnd = seg.wnd
			segment.una = seg.una

			need := IKCP_OVERHEAD + len(segment.data)
			makeSpace(need)
			ptr = segment.encode(ptr)
			copy(ptr, segment.data)
			ptr = ptr[len(segment.data):]

			if segment.xmit >= kcp.dead_link {
				kcp.state = 0xFFFFFFFF
			}
		}

		// get the nearest rto
		if rto := _itimediff(segment.resendts, current); rto > 0 && rto < minrto {
			minrto = rto
		}
		return true
	})

	// flash remain segments
	flushBuffer()

	// counter updates
	sum := lostSegs
	if lostSegs > 0 {
		atomic.AddUint64(&DefaultSnmp.LostSegs, lostSegs)
	}
	if fastRetransSegs > 0 {
		atomic.AddUint64(&DefaultSnmp.FastRetransSegs, fastRetransSegs)
		sum += fastRetransSegs
	}
	if earlyRetransSegs > 0 {
		atomic.AddUint64(&DefaultSnmp.EarlyRetransSegs, earlyRetransSegs)
		sum += earlyRetransSegs
	}
	if sum > 0 {
		atomic.AddUint64(&DefaultSnmp.RetransSegs, sum)
	}

	// cwnd update
	if kcp.nocwnd == 0 {
		// update ssthresh
		// rate halving, https://tools.ietf.org/html/rfc6937
		if change > 0 {
			inflight := kcp.snd_nxt - kcp.snd_una
			kcp.ssthresh = inflight / 2
			if kcp.ssthresh < IKCP_THRESH_MIN {
				kcp.ssthresh = IKCP_THRESH_MIN
			}
			kcp.cwnd = kcp.ssthresh + resent
			kcp.incr = kcp.cwnd * kcp.mss
		}

		// congestion control, https://tools.ietf.org/html/rfc5681
		if lostSegs > 0 {
			kcp.ssthresh = cwnd / 2
			if kcp.ssthresh < IKCP_THRESH_MIN {
				kcp.ssthresh = IKCP_THRESH_MIN
			}
			kcp.cwnd = 1
			kcp.incr = kcp.mss
		}

		if kcp.cwnd < 1 {
			kcp.cwnd = 1
			kcp.incr = kcp.mss
		}
	}

	return uint32(minrto)
}

// (deprecated)
//
// Update updates state (call it repeatedly, every 10ms-100ms), or you can ask
// ikcp_check when to call it again (without ikcp_input/_send calling).
// 'current' - current timestamp in millisec.
func (kcp *KCP) Update() {
	var slap int32

	current := currentMs()
	if kcp.updated == 0 {
		kcp.updated = 1
		kcp.ts_flush = current
	}

	slap = _itimediff(current, kcp.ts_flush)

	if slap >= 10000 || slap < -10000 {
		kcp.ts_flush = current
		slap = 0
	}

	if slap >= 0 {
		kcp.ts_flush += kcp.interval
		if _itimediff(current, kcp.ts_flush) >= 0 {
			kcp.ts_flush = current + kcp.interval
		}
		kcp.flush(false)
	}
}

// (deprecated)
//
// Check determines when should you invoke ikcp_update:
// returns when you should invoke ikcp_update in millisec, if there
// is no ikcp_input/_send calling. you can call ikcp_update in that
// time, instead of call update repeatly.
// Important to reduce unnacessary ikcp_update invoking. use it to
// schedule ikcp_update (eg. implementing an epoll-like mechanism,
// or optimize ikcp_update when handling massive kcp connections)
func (kcp *KCP) Check() uint32 {
	current := currentMs()
	ts_flush := kcp.ts_flush
	tm_flush := int32(0x7fffffff)
	tm_packet := int32(0x7fffffff)
	minimal := uint32(0)
	if kcp.updated == 0 {
		return current
	}

	if _itimediff(current, ts_flush) >= 10000 ||
		_itimediff(current, ts_flush) < -10000 {
		ts_flush = current
	}

	if _itimediff(current, ts_flush) >= 0 {
		return current
	}

	tm_flush = _itimediff(ts_flush, current)

	ready := false
	kcp.snd_buf.ForEach(func(seg *segment) bool {
		diff := _itimediff(seg.resendts, current)
		if diff <= 0 {
			ready = true
			return false
		}
		if diff < tm_packet {
			tm_packet = diff
		}
		return true
	})
	if ready {
		return current
	}

	minimal = uint32(tm_packet)
	if tm_packet >= tm_flush {
		minimal = uint32(tm_flush)
	}
	if minimal >= kcp.interval {
		minimal = kcp.interval
	}

	return current + minimal
}

// SetMtu changes MTU size, default is 1400
func (kcp *KCP) SetMtu(mtu int) int {
	if mtu <= IKCP_OVERHEAD || kcp.reserved < 0 || kcp.reserved >= mtu-IKCP_OVERHEAD {
		return -1
	}
	buffer := make([]byte, mtu)
	kcp.mtu = uint32(mtu)
	kcp.mss = kcp.mtu - uint32(IKCP_OVERHEAD) - uint32(kcp.reserved)
	kcp.buffer = buffer
	return 0
}

// NoDelay options
// fastest: ikcp_nodelay(kcp, 1, 20, 2, 1)
// nodelay: 0:disable(default), 1:enable
// interval: internal update timer interval in millisec, default is 100ms
// resend: 0:disable fast resend(default), 1:enable fast resend
// nc: 0:normal congestion control(default), 1:disable congestion control
func (kcp *KCP) NoDelay(nodelay, interval, resend, nc int) int {
	if nodelay >= 0 {
		kcp.nodelay = uint32(nodelay)
		if nodelay != 0 {
			kcp.rx_minrto = IKCP_RTO_NDL
		} else {
			kcp.rx_minrto = IKCP_RTO_MIN
		}
	}
	if interval >= 0 {
		if interval > 5000 {
			interval = 5000
		} else if interval < 10 {
			interval = 10
		}
		kcp.interval = uint32(interval)
	}
	if resend >= 0 {
		kcp.fastresend = int32(resend)
	}
	if nc >= 0 {
		kcp.nocwnd = int32(nc)
	}
	return 0
}

// WndSize sets maximum window size: sndwnd=32, rcvwnd=32 by default
func (kcp *KCP) WndSize(sndwnd, rcvwnd int) int {
	if sndwnd > 0 {
		kcp.snd_wnd = uint32(sndwnd)
	}
	if rcvwnd > 0 {
		kcp.rcv_wnd = uint32(rcvwnd)
	}
	return 0
}

// WaitSnd gets how many packet is waiting to be sent
func (kcp *KCP) WaitSnd() int {
	return kcp.snd_buf.Len() + kcp.snd_queue.Len()
}

// Release all cached outgoing segments
func (kcp *KCP) ReleaseTX() {
	kcp.snd_queue.ForEach(func(seg *segment) bool {
		kcp.delSegment(seg)
		return true
	})
	kcp.snd_buf.ForEach(func(seg *segment) bool {
		kcp.delSegment(seg)
		return true
	})
	kcp.snd_queue.Clear()
	kcp.snd_buf.Clear()
}

// release 回收当前会话持有的全部收发分片
func (kcp *KCP) release() {
	kcp.ReleaseTX()
	kcp.rcv_queue.ForEach(func(seg *segment) bool {
		kcp.delSegment(seg)
		return true
	})
	kcp.rcv_queue.Clear()
	for kcp.rcv_buf.Len() > 0 {
		seg, _ := kcp.rcv_buf.Pop()
		kcp.delSegment(&seg)
	}
}
