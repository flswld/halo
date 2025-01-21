package mem

import (
	"runtime"
	"unsafe"
)

type _type uint8

//go:linkname mallocgc runtime.mallocgc
func mallocgc(size uintptr, typ *_type, needzero bool) unsafe.Pointer

type GoHeap struct{}

func NewGoHeap() GoHeap {
	return struct{}{}
}

func (h GoHeap) Malloc(size uint64) unsafe.Pointer {
	p := mallocgc(uintptr(size), nil, true)
	return p
}

func (h GoHeap) Free(p unsafe.Pointer) bool {
	runtime.KeepAlive(p)
	return true
}

func (h GoHeap) GetAllocSize() uint64 {
	return 0
}
