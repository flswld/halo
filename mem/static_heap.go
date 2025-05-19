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
	header blockHeader
	next   *block
}

var blockSize uint64 = 0

func init() {
	blockSize = SizeOf[block]()
}

type StaticHeap struct {
	allocSize uint64
	blockList *block
}

func NewStaticHeap(memory unsafe.Pointer, size uint64) *StaticHeap {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[StaticHeap]()
	if size < headerSize {
		return nil
	}
	h := (*StaticHeap)(memory)
	b := (*block)(unsafe.Pointer(uintptr(memory) + uintptr(headerSize)))
	b.header.setSize(size - headerSize)
	b.header.setFree(true)
	b.next = nil
	h.allocSize = headerSize
	h.blockList = b
	return h
}

func (h *StaticHeap) Malloc(size uint64) unsafe.Pointer {
	if size == 0 {
		return nil
	}
	align := size % 8
	if align != 0 {
		align = 8 - align
	}
	size += align
	for b := h.blockList; b != nil; b = b.next {
		if !b.header.getFree() {
			continue
		}
		if size > b.header.getSize() {
			for {
				nb := b.next
				if nb == nil {
					break
				}
				if !nb.header.getFree() {
					break
				}
				b.header.setSize(b.header.getSize() + nb.header.getSize() + blockSize)
				b.next = nb.next
				if b.header.getSize() >= size {
					break
				}
			}
			if size > b.header.getSize() {
				continue
			}
		}
		if size < b.header.getSize() && b.header.getSize()-size > blockSize {
			nb := (*block)(unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize) + uintptr(size)))
			nb.header.setSize(b.header.getSize() - size - blockSize)
			nb.header.setFree(true)
			nb.next = b.next
			b.header.setSize(size)
			b.next = nb
		}
		b.header.setFree(false)
		h.allocSize += blockSize + b.header.getSize()
		return unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize))
	}
	return nil
}

func (h *StaticHeap) Free(p unsafe.Pointer) bool {
	b := (*block)(unsafe.Pointer(uintptr(p) - uintptr(blockSize)))
	if b.header.getFree() {
		return false
	}
	b.header.setFree(true)
	h.allocSize -= blockSize + b.header.getSize()
	return true
}

func (h *StaticHeap) GetAllocSize() uint64 {
	return h.allocSize
}
