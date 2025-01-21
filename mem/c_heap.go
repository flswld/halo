package mem

import (
	"unsafe"
)

/*
#include <stdlib.h>

void* c_malloc(size_t size) {
	return malloc(size);
}

void c_free(void* p) {
	free(p);
}
*/
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
