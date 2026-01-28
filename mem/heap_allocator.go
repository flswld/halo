//go:build !cgo
// +build !cgo

package mem

import (
	"runtime"
	"unsafe"
)

const (
	CACHE_LINE_SIZE = 64
)

type HeapAllocator struct{}

func GetHeapAllocator() HeapAllocator {
	return struct{}{}
}

type _type struct{}

//go:linkname mallocgc runtime.mallocgc
func mallocgc(size uintptr, typ *_type, needzero bool) unsafe.Pointer

func (h HeapAllocator) Malloc(size uint64) unsafe.Pointer {
	p := mallocgc(uintptr(size), nil, true)
	return p
}

func (h HeapAllocator) Free(p unsafe.Pointer) bool {
	runtime.KeepAlive(p)
	return true
}

func (h HeapAllocator) AlignedMalloc(size uint64, align uint64) unsafe.Pointer {
	if align == 0 {
		align = CACHE_LINE_SIZE
	}
	total := size + align - 1 + SizeOf[uintptr]()
	raw := h.Malloc(total)
	if raw == nil {
		return nil
	}
	rawAddr := uintptr(raw)
	alignedAddr := rawAddr + uintptr(SizeOf[uintptr]())
	alignedAddr = (alignedAddr + uintptr(align) - 1) & ^(uintptr(align) - 1)
	origPtrAddr := alignedAddr - uintptr(SizeOf[uintptr]())
	*(*uintptr)(unsafe.Pointer(origPtrAddr)) = rawAddr
	return unsafe.Pointer(alignedAddr)
}

func (h HeapAllocator) AlignedFree(p unsafe.Pointer) bool {
	if p == nil {
		return false
	}
	alignedAddr := uintptr(p)
	origPtrAddr := alignedAddr - unsafe.Sizeof(uintptr(0))
	rawAddr := *(*uintptr)(unsafe.Pointer(origPtrAddr))
	h.Free(unsafe.Pointer(rawAddr))
	return true
}

func (h HeapAllocator) GetAllocSize() uint64 {
	return 0
}

func GetShareMem(name string, size uint64) unsafe.Pointer {
	return nil
}
