package mem

import (
	"unsafe"
)

// #include "../cgo/mem.h"
import "C"

type CHeap struct{}

func NewCHeap() CHeap {
	return struct{}{}
}

func (h CHeap) Malloc(size uint64) unsafe.Pointer {
	p := C.c_malloc(C.size_t(size))
	return p
}

func (h CHeap) Free(p unsafe.Pointer) bool {
	C.c_free(p)
	return true
}

func (h CHeap) AlignedMalloc(size uint64) unsafe.Pointer {
	p := C.aligned_malloc(C.size_t(size))
	return p
}

func (h CHeap) AlignedFree(p unsafe.Pointer) bool {
	C.aligned_free(p)
	return true
}

func (h CHeap) GetAllocSize() uint64 {
	return 0
}
