//go:build cgo
// +build cgo

package mem

import (
	"unsafe"
)

// #cgo linux LDFLAGS: -lrt
// #include "../cgo/mem.h"
import "C"

const (
	CACHE_LINE_SIZE = 64
)

type HeapAllocator struct{}

func GetHeapAllocator() HeapAllocator {
	return struct{}{}
}

func (h HeapAllocator) Malloc(size uint64) unsafe.Pointer {
	p := C.c_malloc(C.size_t(size))
	return p
}

func (h HeapAllocator) Free(p unsafe.Pointer) bool {
	C.c_free(p)
	return true
}

func (h HeapAllocator) AlignedMalloc(size uint64, align uint64) unsafe.Pointer {
	if align == 0 {
		align = CACHE_LINE_SIZE
	}
	p := C.aligned_malloc(C.size_t(size), C.size_t(align))
	return p
}

func (h HeapAllocator) AlignedFree(p unsafe.Pointer) bool {
	C.aligned_free(p)
	return true
}

func (h HeapAllocator) GetAllocSize() uint64 {
	return 0
}

func GetShareMem(name string, size uint64) unsafe.Pointer {
	_name := C.CString(name)
	p := C.get_share_mem(_name, C.size_t(size))
	C.free(unsafe.Pointer(_name))
	return p
}
