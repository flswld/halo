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

// TestRingBufferData 验证环形缓冲区的并发报文读写
func TestRingBufferData(t *testing.T) {
	memory := GetHeapAllocator().AlignedMalloc(SizeOf[RingBuffer]()+1*MB, 0)
	rb := RingBufferCreate(memory, SizeOf[RingBuffer]()+1*MB)
	producer := NewRingBufferProducer(rb, 0)
	consumer := NewRingBufferConsumer(rb, 0)

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
			ok := producer.WritePacket(data)
			if !ok {
				continue
			}
			seq++
		}
	}()

	go func() {
		cpu.BindCpuCore(1)
		_data := make([]byte, 16)
		_len := uint32(0)
		_seq := uint64(1)
		_tt := time.Now()
		for {
			if stop.Load() {
				break
			}
			consumer.ReadPacket(_data, &_len)
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

	time.Sleep(10 * time.Second)
	stop.Store(true)
	time.Sleep(time.Second)

	RingBufferDestroy(rb)
	GetHeapAllocator().AlignedFree(memory)
}

// TestMsg 表示环形缓冲区结构体读写测试消息
type TestMsg struct {
	Seq uint64 // 消息序号
}

// TestRingBufferStruct 验证结构体数据通过环形缓冲区并发传输
func TestRingBufferStruct(t *testing.T) {
	memory := GetHeapAllocator().AlignedMalloc(SizeOf[RingBuffer]()+1*MB, 0)
	rb := RingBufferCreate(memory, SizeOf[RingBuffer]()+1*MB)
	producer := NewRingBufferProducer(rb, 0)
	consumer := NewRingBufferConsumer(rb, 0)

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
			ok := producer.WritePacket(*(*[]uint8)(unsafe.Pointer(msgData)))
			if !ok {
				continue
			}
			msg.Seq++
		}
	}()

	go func() {
		cpu.BindCpuCore(1)
		msgData := make([]byte, 64)
		_len := uint32(0)
		seq := uint64(1)
		_tt := time.Now()
		for {
			if stop.Load() {
				break
			}
			consumer.ReadPacket(msgData, &_len)
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

	time.Sleep(10 * time.Second)
	stop.Store(true)
	time.Sleep(time.Second)

	RingBufferDestroy(rb)
	GetHeapAllocator().AlignedFree(memory)
}

// TestRingBufferShmWrite 持续向共享内存环形缓冲区写入测试报文
func TestRingBufferShmWrite(t *testing.T) {
	memory := GetShareMem("RingBuffer", SizeOf[RingBuffer]()+1*MB)
	offset := int64(0)
	rb := RingBufferMapping(memory, &offset)
	if rb == nil {
		rb = RingBufferCreate(memory, SizeOf[RingBuffer]()+1*MB)
	}
	producer := NewRingBufferProducer(rb, offset)
	data := make([]byte, 16)
	for i := 8; i <= 15; i++ {
		data[i] = 0xff
	}
	seq := uint64(1)
	var stop atomic.Bool
	timer := time.AfterFunc(10*time.Second, func() {
		stop.Store(true)
	})
	defer timer.Stop()
	cpu.BindCpuCore(0)
	for !stop.Load() {
		binary.BigEndian.PutUint64(data[0:8], seq)
		ok := producer.WritePacket(data)
		if !ok {
			continue
		}
		seq++
	}
}

// TestRingBufferShmRead 持续从共享内存环形缓冲区读取测试报文
func TestRingBufferShmRead(t *testing.T) {
	memory := GetShareMem("RingBuffer", SizeOf[RingBuffer]()+1*MB)
	offset := int64(0)
	rb := RingBufferMapping(memory, &offset)
	if rb == nil {
		rb = RingBufferCreate(memory, SizeOf[RingBuffer]()+1*MB)
	}
	consumer := NewRingBufferConsumer(rb, offset)
	_data := make([]byte, 16)
	_len := uint32(0)
	_seq := uint64(1)
	_tt := time.Now()
	var stop atomic.Bool
	timer := time.AfterFunc(10*time.Second, func() {
		stop.Store(true)
	})
	defer timer.Stop()
	cpu.BindCpuCore(1)
	for !stop.Load() {
		consumer.ReadPacket(_data, &_len)
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
