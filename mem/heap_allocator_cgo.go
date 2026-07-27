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

// HeapAllocator 使用 C 运行时堆分配内存
type HeapAllocator struct{}

// GetHeapAllocator 返回堆内存分配器
func GetHeapAllocator() HeapAllocator {
	return struct{}{}
}

// Malloc 从 C 运行时堆分配指定字节数的内存
func (h HeapAllocator) Malloc(size uint64) unsafe.Pointer {
	p := C.c_malloc(C.size_t(size))
	return p
}

// Free 释放 C 运行时堆内存
func (h HeapAllocator) Free(p unsafe.Pointer) bool {
	C.c_free(p)
	return true
}

// AlignedMalloc 从 C 运行时堆分配按指定边界对齐的内存
func (h HeapAllocator) AlignedMalloc(size uint64, align uint64) unsafe.Pointer {
	if align == 0 {
		align = CACHE_LINE_SIZE
	}
	p := C.aligned_malloc(C.size_t(size), C.size_t(align))
	return p
}

// AlignedFree 释放对齐分配的 C 堆内存
func (h HeapAllocator) AlignedFree(p unsafe.Pointer) bool {
	C.aligned_free(p)
	return true
}

// GetAllocSize 返回 0 当前堆分配器不统计已分配字节数
func (h HeapAllocator) GetAllocSize() uint64 {
	return 0
}

// GetShareMem 获取或创建指定名称和大小的共享内存
func GetShareMem(name string, size uint64) unsafe.Pointer {
	_name := C.CString(name)
	p := C.get_share_mem(_name, C.size_t(size))
	C.free(unsafe.Pointer(_name))
	return p
}
