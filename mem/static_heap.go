package mem

import (
	"unsafe"
)

type blockHeader uint64

func (h *blockHeader) getFree() bool {
	return (uint64(*h) >> 63) == 1
}

func (h *blockHeader) setFree(free bool) {
	x := uint64(0)
	if free {
		x = 1 << 63
	} else {
		x = 0
	}
	*h = blockHeader(x | (uint64(*h) & ((1<<64 - 1) >> 1)))
}

func (h *blockHeader) getSize() uint64 {
	return uint64(*h) & ((1<<64 - 1) >> 1)
}

func (h *blockHeader) setSize(size uint64) {
	*h = blockHeader((uint64(*h) & (1 << 63)) | (size & ((1<<64 - 1) >> 1)))
}

type block struct {
	header   blockHeader
	next     *block
	prev     *block
	freeNext *block
	freePrev *block
}

var blockSize uint64 = 0

func init() {
	blockSize = SizeOf[block]()
}

type StaticHeap struct {
	allocSize     uint64
	freeBlockList *block
}

func NewStaticHeap(memory unsafe.Pointer, size uint64) *StaticHeap {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[StaticHeap]()
	if size < headerSize+blockSize {
		return nil
	}
	h := (*StaticHeap)(memory)
	b := (*block)(unsafe.Pointer(uintptr(memory) + uintptr(headerSize)))
	b.header.setSize(size - headerSize - blockSize)
	b.header.setFree(true)
	b.next = nil
	b.prev = nil
	b.freeNext = nil
	b.freePrev = nil
	h.allocSize = headerSize
	h.freeBlockList = b
	return h
}

func (h *StaticHeap) Malloc(size uint64) unsafe.Pointer {
	if size == 0 {
		return nil
	}
	size = (size + 7) & ^uint64(7)
	for b := h.freeBlockList; b != nil; b = b.freeNext {
		if b.header.getSize() < size {
			continue
		}
		if b.header.getSize()-size > blockSize {
			nb := (*block)(unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize) + uintptr(size)))
			nb.header.setSize(b.header.getSize() - size - blockSize)
			nb.header.setFree(true)
			nb.next = b.next
			nb.prev = b
			if b.next != nil {
				b.next.prev = nb
			}
			b.next = nb
			h.insertFreeBlock(nb)
			b.header.setSize(size)
		}
		h.removeFreeBlock(b)
		b.header.setFree(false)
		h.allocSize += blockSize + b.header.getSize()
		return unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize))
	}
	return nil
}

func (h *StaticHeap) Free(p unsafe.Pointer) bool {
	if p == nil {
		return false
	}
	b := (*block)(unsafe.Pointer(uintptr(p) - uintptr(blockSize)))
	if b.header.getFree() {
		return false
	}
	b.header.setFree(true)
	h.allocSize -= blockSize + b.header.getSize()
	for b.next != nil && b.next.header.getFree() {
		next := b.next
		h.removeFreeBlock(next)
		b.header.setSize(b.header.getSize() + next.header.getSize() + blockSize)
		b.next = next.next
		if next.next != nil {
			next.next.prev = b
		}
	}
	if b.prev != nil && b.prev.header.getFree() {
		prev := b.prev
		h.removeFreeBlock(prev)
		prev.header.setSize(prev.header.getSize() + b.header.getSize() + blockSize)
		prev.next = b.next
		if b.next != nil {
			b.next.prev = prev
		}
		b = prev
	}
	h.insertFreeBlock(b)
	return true
}

func (h *StaticHeap) insertFreeBlock(b *block) {
	b.freeNext = h.freeBlockList
	if h.freeBlockList != nil {
		h.freeBlockList.freePrev = b
	}
	b.freePrev = nil
	h.freeBlockList = b
}

func (h *StaticHeap) removeFreeBlock(b *block) {
	if b.freePrev != nil {
		b.freePrev.freeNext = b.freeNext
	} else {
		h.freeBlockList = b.freeNext
	}
	if b.freeNext != nil {
		b.freeNext.freePrev = b.freePrev
	}
	b.freeNext = nil
	b.freePrev = nil
}

func (h *StaticHeap) GetAllocSize() uint64 {
	return h.allocSize
}

type StaticString64 [64]uint8

func (s *StaticString64) Get() string {
	var l uint8 = 0
	l = (*s)[64-1]
	return unsafe.String(&(*s)[0], l)
}

func (s *StaticString64) Set(v string) {
	l := uint8(len(v))
	if l > 64-1 {
		l = 64 - 1
	}
	(*s)[64-1] = l
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}

type StaticString1K [1 * KB]uint8

func (s *StaticString1K) Get() string {
	var l uint16 = 0
	l |= uint16((*s)[1*KB-1]) << 8
	l |= uint16((*s)[1*KB-2])
	return unsafe.String(&(*s)[0], l)
}

func (s *StaticString1K) Set(v string) {
	l := uint16(len(v))
	if l > 1*KB-2 {
		l = 1*KB - 2
	}
	(*s)[1*KB-1] = uint8(l >> 8)
	(*s)[1*KB-2] = uint8(l)
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}

type StaticString1M [1 * MB]uint8

func (s *StaticString1M) Get() string {
	var l uint32 = 0
	l |= uint32((*s)[1*MB-1]) << 24
	l |= uint32((*s)[1*MB-2]) << 16
	l |= uint32((*s)[1*MB-3]) << 8
	l |= uint32((*s)[1*MB-4])
	return unsafe.String(&(*s)[0], l)
}

func (s *StaticString1M) Set(v string) {
	l := uint32(len(v))
	if l > 1*MB-4 {
		l = 1*MB - 4
	}
	(*s)[1*MB-1] = uint8(l >> 24)
	(*s)[1*MB-2] = uint8(l >> 16)
	(*s)[1*MB-3] = uint8(l >> 8)
	(*s)[1*MB-4] = uint8(l)
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}
