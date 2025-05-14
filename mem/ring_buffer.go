package mem

import (
	"runtime"
	"sync/atomic"
	"unsafe"
)

type RingBuffer struct {
	head   uint64
	_      [56]byte
	tail   uint64
	size   uint32
	mask   uint32
	buffer unsafe.Pointer
	_      [40]byte
}

func RingBufferCreate(mem unsafe.Pointer, size uint32) *RingBuffer {
	if mem == nil {
		return nil
	}
	if (size & (size - 1)) != 0 {
		return nil
	}
	rb := new(RingBuffer)

	rb.head = 0
	rb.tail = 0
	rb.size = size
	rb.mask = size - 1
	rb.buffer = mem

	return rb
}

func RingBufferDestroy(rb *RingBuffer) {
	if rb != nil {
		rb.head = 0
		rb.tail = 0
		rb.size = 0
		rb.mask = 0
		rb.buffer = nil
		runtime.KeepAlive(rb)
	}
}

func ringBufferFreeSpace(rb *RingBuffer) uint32 {
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	return rb.size - uint32(head-tail)
}

func ringBufferUsedSpace(rb *RingBuffer) uint32 {
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	return uint32(head - tail)
}

func WritePacket(rb *RingBuffer, data []uint8, len uint16) bool {
	if len == 0 || uint32(len) > rb.size/2 {
		return false
	}

	totalSize := uint32(2 + len)
	totalSize = (totalSize + 3) & ^uint32(3)

	if ringBufferFreeSpace(rb) < totalSize {
		return false
	}

	head := atomic.LoadUint64(&rb.head)
	pos := uint32(head & uint64(rb.mask))

	*(*uint16)(Offset(rb.buffer, uint64(pos))) = len

	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos

	if spaceAfter >= uint32(len) {
		MemCpy(Offset(rb.buffer, uint64(dataPos)), unsafe.Pointer(&data[0]), uint64(len))
	} else {
		MemCpy(Offset(rb.buffer, uint64(dataPos)), unsafe.Pointer(&data[0]), uint64(spaceAfter))
		MemCpy(rb.buffer, Offset(unsafe.Pointer(&data[0]), uint64(spaceAfter)), uint64(uint32(len)-spaceAfter))
	}

	atomic.StoreUint64(&rb.head, head+uint64(totalSize))

	return true
}

func ReadPacket(rb *RingBuffer, data []uint8, len *uint16) bool {
	if ringBufferUsedSpace(rb) < 2 {
		return false
	}

	tail := atomic.LoadUint64(&rb.tail)
	pos := uint32(tail & uint64(rb.mask))

	packetLen := *(*uint16)(Offset(rb.buffer, uint64(pos)))

	if packetLen == 0 || uint32(packetLen) > rb.size/2 {
		return false
	}

	totalSize := uint32(2 + packetLen)
	totalSize = (totalSize + 3) & ^uint32(3)

	if ringBufferUsedSpace(rb) < totalSize {
		return false
	}

	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos

	if spaceAfter >= uint32(packetLen) {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, uint64(dataPos)), uint64(packetLen))
	} else {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, uint64(dataPos)), uint64(spaceAfter))
		MemCpy(Offset(unsafe.Pointer(&data[0]), uint64(spaceAfter)), rb.buffer, uint64(uint32(packetLen)-spaceAfter))
	}

	*len = packetLen

	atomic.StoreUint64(&rb.tail, tail+uint64(totalSize))

	return true
}
