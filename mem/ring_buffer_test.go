package mem

import (
	"encoding/binary"
	"log"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/flswld/halo/cpu"
)

func TestRingBufferData(t *testing.T) {
	memory := new(CHeap).AlignedMalloc(SizeOf[RingBuffer]() + 1*MB)
	rb := RingBufferCreate(memory, uint32(SizeOf[RingBuffer]()+1*MB))

	var stop atomic.Bool

	go func() {
		cpu.BindCpuCore(0)
		data := make([]byte, 16)
		for i := 8; i <= 15; i++ {
			data[i] = 0xff
		}
		seq := uint64(1)
		for {
			if stop.Load() {
				break
			}
			binary.BigEndian.PutUint64(data[0:8], seq)
			ok := WritePacket(rb, data, 16)
			if !ok {
				continue
			}
			seq++
		}
	}()

	go func() {
		cpu.BindCpuCore(1)
		_data := make([]byte, 16)
		_len := uint16(0)
		_seq := uint64(1)
		_tt := time.Now()
		for {
			if stop.Load() {
				break
			}
			ReadPacket(rb, _data, &_len)
			if _len == 0 {
				continue
			}
			data := _data[0:_len]
			if _len != 16 || data[15] != 0xff {
				panic("???")
			}
			seq := binary.BigEndian.Uint64(data[0:8])
			if seq%(1024*1024*8) == 0 {
				tt := time.Now()
				ops := float64(seq-_seq) / (tt.Sub(_tt).Seconds())
				log.Printf("speed: %.0f op/s\n", ops)
				_tt = tt
				_seq = seq
			}
		}
	}()

	time.Sleep(time.Second * 30)
	stop.Store(true)
	time.Sleep(time.Second)

	RingBufferDestroy(rb)
	new(CHeap).AlignedFree(memory)
}

type TestMsg struct {
	Seq uint64
}

func TestRingBufferStruct(t *testing.T) {
	memory := new(CHeap).AlignedMalloc(SizeOf[RingBuffer]() + 1*MB)
	rb := RingBufferCreate(memory, uint32(SizeOf[RingBuffer]()+1*MB))

	var stop atomic.Bool

	go func() {
		cpu.BindCpuCore(0)
		msg := new(TestMsg)
		msg.Seq = 1
		msgLen := SizeOf[TestMsg]()
		msgData := new(SliceHeader)
		for {
			if stop.Load() {
				break
			}
			msgData.Data = uintptr(unsafe.Pointer(msg))
			msgData.Len = int(msgLen)
			msgData.Cap = int(msgLen)
			ok := WritePacket(rb, *(*[]uint8)(unsafe.Pointer(msgData)), uint16(msgLen))
			if !ok {
				continue
			}
			msg.Seq++
		}
	}()

	go func() {
		cpu.BindCpuCore(1)
		msgData := make([]byte, 64)
		_len := uint16(0)
		seq := uint64(1)
		_tt := time.Now()
		for {
			if stop.Load() {
				break
			}
			ReadPacket(rb, msgData, &_len)
			if _len == 0 {
				continue
			}
			msg := (*TestMsg)(unsafe.Pointer(&msgData[0]))
			if msg.Seq%(1024*1024*8) == 0 {
				tt := time.Now()
				ops := float64(msg.Seq-seq) / (tt.Sub(_tt).Seconds())
				log.Printf("speed: %.0f op/s\n", ops)
				_tt = tt
				seq = msg.Seq
			}
		}
	}()

	time.Sleep(time.Second * 30)
	stop.Store(true)
	time.Sleep(time.Second)

	RingBufferDestroy(rb)
	new(CHeap).AlignedFree(memory)
}

func TestRingBufferShmWrite(t *testing.T) {
	memory := GetShareMem("RingBuffer", SizeOf[RingBuffer]()+1*MB)
	offset := int64(0)
	rb := RingBufferMapping(memory, &offset)
	if rb == nil {
		rb = RingBufferCreate(memory, uint32(SizeOf[RingBuffer]()+1*MB))
	}
	data := make([]byte, 16)
	for i := 8; i <= 15; i++ {
		data[i] = 0xff
	}
	seq := uint64(1)
	cpu.BindCpuCore(0)
	for {
		binary.BigEndian.PutUint64(data[0:8], seq)
		ok := WritePacketOffset(rb, offset, data, 16)
		if !ok {
			continue
		}
		seq++
	}
}

func TestRingBufferShmRead(t *testing.T) {
	memory := GetShareMem("RingBuffer", SizeOf[RingBuffer]()+1*MB)
	offset := int64(0)
	rb := RingBufferMapping(memory, &offset)
	if rb == nil {
		rb = RingBufferCreate(memory, uint32(SizeOf[RingBuffer]()+1*MB))
	}
	_data := make([]byte, 16)
	_len := uint16(0)
	_seq := uint64(1)
	_tt := time.Now()
	cpu.BindCpuCore(1)
	for {
		ReadPacketOffset(rb, offset, _data, &_len)
		if _len == 0 {
			continue
		}
		data := _data[0:_len]
		if _len != 16 || data[15] != 0xff {
			panic("???")
		}
		seq := binary.BigEndian.Uint64(data[0:8])
		if seq%(1024*1024*8) == 0 {
			tt := time.Now()
			ops := float64(seq-_seq) / (tt.Sub(_tt).Seconds())
			log.Printf("speed: %.0f op/s\n", ops)
			_tt = tt
			_seq = seq
		}
	}
}
