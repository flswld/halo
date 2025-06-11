package mem

import (
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

func RingBufferCreate(memory unsafe.Pointer, size uint32) *RingBuffer {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[RingBuffer]()
	if uint64(size) < headerSize {
		return nil
	}
	size -= uint32(headerSize)
	if (size & (size - 1)) != 0 {
		return nil
	}

	rb := (*RingBuffer)(memory)
	rb.head = 0
	rb.tail = 0
	rb.size = size
	rb.mask = size - 1
	rb.buffer = unsafe.Pointer(uintptr(memory) + uintptr(headerSize))

	for i := 8; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xAA
	}
	for i := 88; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xFF
	}

	return rb
}

func RingBufferDestroy(rb *RingBuffer) {
	if rb != nil {
		rb.head = 0
		rb.tail = 0
		rb.size = 0
		rb.mask = 0
		rb.buffer = nil

		for i := 8; i <= 63; i++ {
			v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
			*v = 0x00
		}
		for i := 88; i <= 127; i++ {
			v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
			*v = 0x00
		}
	}
}

func RingBufferMapping(memory unsafe.Pointer, offset *int64) *RingBuffer {
	if memory == nil {
		return nil
	}
	rb := (*RingBuffer)(memory)

	for i := 8; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xAA {
			return nil
		}
	}
	for i := 88; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xFF {
			return nil
		}
	}

	headerSize := SizeOf[RingBuffer]()
	*offset = int64(uintptr(memory) + uintptr(headerSize) - uintptr(rb.buffer))
	return rb
}

func WritePacketOffset(rb *RingBuffer, offset int64, data []uint8, len uint16) bool {
	if len == 0 || uint32(len) > rb.size/2 {
		return false
	}
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	freeSpace := rb.size - uint32(head-tail)
	totalSize := uint32(2 + len)
	totalSize = (totalSize + 3) & ^uint32(3)
	if freeSpace < totalSize {
		return false
	}
	pos := uint32(head & uint64(rb.mask))
	*(*uint16)(Offset(rb.buffer, offset+int64(pos))) = len
	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos
	if spaceAfter >= uint32(len) {
		MemCpy(Offset(rb.buffer, offset+int64(dataPos)), unsafe.Pointer(&data[0]), uint64(len))
	} else {
		MemCpy(Offset(rb.buffer, offset+int64(dataPos)), unsafe.Pointer(&data[0]), uint64(spaceAfter))
		MemCpy(Offset(rb.buffer, offset), Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), uint64(uint32(len)-spaceAfter))
	}
	atomic.StoreUint64(&rb.head, head+uint64(totalSize))
	return true
}

func WritePacket(rb *RingBuffer, data []uint8, len uint16) bool {
	return WritePacketOffset(rb, 0, data, len)
}

func ReadPacketOffset(rb *RingBuffer, offset int64, data []uint8, len *uint16) bool {
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	usedSpace := uint32(head - tail)
	if usedSpace < 2 {
		return false
	}
	pos := uint32(tail & uint64(rb.mask))
	packetLen := *(*uint16)(Offset(rb.buffer, offset+int64(pos)))
	if packetLen == 0 || uint32(packetLen) > rb.size/2 {
		return false
	}
	totalSize := uint32(2 + packetLen)
	totalSize = (totalSize + 3) & ^uint32(3)
	if usedSpace < totalSize {
		return false
	}
	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos
	if spaceAfter >= uint32(packetLen) {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, offset+int64(dataPos)), uint64(packetLen))
	} else {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, offset+int64(dataPos)), uint64(spaceAfter))
		MemCpy(Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), Offset(rb.buffer, offset), uint64(uint32(packetLen)-spaceAfter))
	}
	*len = packetLen
	atomic.StoreUint64(&rb.tail, tail+uint64(totalSize))
	return true
}

func ReadPacket(rb *RingBuffer, data []uint8, len *uint16) bool {
	return ReadPacketOffset(rb, 0, data, len)
}

type SliceHeader struct {
	Data uintptr
	Len  int
	Cap  int
}
