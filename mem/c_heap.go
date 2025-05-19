package mem

import (
	"unsafe"
)

// #cgo linux LDFLAGS: -lrt
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

func (h CHeap) GetAllocSize() uint64 {
	return 0
}

func (h CHeap) AlignedMalloc(size uint64) unsafe.Pointer {
	p := C.aligned_malloc(C.size_t(size))
	return p
}

func (h CHeap) AlignedFree(p unsafe.Pointer) bool {
	C.aligned_free(p)
	return true
}

func GetShareMem(name string, size uint64) unsafe.Pointer {
	_name := C.CString(name)
	p := C.get_share_mem(_name, C.size_t(size))
	C.free(unsafe.Pointer(_name))
	return p
}
