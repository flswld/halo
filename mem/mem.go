package mem

import (
	"fmt"
	"io"
	"unsafe"
)

const (
	B  = 1
	KB = 1024 * B
	MB = 1024 * KB
	GB = 1024 * MB
)

var (
	DefaultLogWriter    io.Writer = nil
	MallocFreeFailPanic           = false
)

// Allocator 定义内存分配器接口
type Allocator interface {
	// Malloc 分配指定字节数的内存
	Malloc(size uint64) unsafe.Pointer
	// Free 释放指定内存
	Free(p unsafe.Pointer) bool
	// AlignedMalloc 按指定对齐方式分配内存
	AlignedMalloc(size uint64, align uint64) unsafe.Pointer
	// AlignedFree 释放对齐分配的内存
	AlignedFree(p unsafe.Pointer) bool
	// GetAllocSize 返回当前已分配的字节数
	GetAllocSize() uint64
}

// MallocType 为指定类型分配连续内存
func MallocType[T any](allocator Allocator, size uint64) *T {
	p := (*T)(allocator.Malloc(size * SizeOf[T]()))
	if MallocFreeFailPanic && p == nil {
		panic("malloc fail")
	}
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(fmt.Sprintf("[Malloc] allocator:%T size:%d ptr:%p\n", allocator, size*SizeOf[T](), p)))
	}
	return p
}

// FreeType 释放指定类型的内存
func FreeType[T any](allocator Allocator, t *T) bool {
	ok := allocator.Free(unsafe.Pointer(t))
	if MallocFreeFailPanic && !ok {
		panic("free fail")
	}
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(fmt.Sprintf("[Free] allocator:%T ptr:%p\n", allocator, unsafe.Pointer(t))))
	}
	return ok
}

// SizeOf 返回指定类型的字节大小
func SizeOf[T any]() uint64 {
	var t T
	return uint64(unsafe.Sizeof(t))
}

// Offset 返回相对指定指针偏移若干字节的地址
func Offset(p unsafe.Pointer, offset int64) unsafe.Pointer {
	if offset > 0 {
		return unsafe.Pointer(uintptr(p) + uintptr(offset))
	} else if offset < 0 {
		return unsafe.Pointer(uintptr(p) - uintptr(-offset))
	} else {
		return p
	}
}

// OffsetType 返回相对指定指针偏移若干元素的地址
func OffsetType[T any](t *T, offset int64) *T {
	return (*T)(Offset(unsafe.Pointer(t), offset*int64(SizeOf[T]())))
}

// memmove 调用 Go 运行时复制可能重叠的内存区域
//
//go:linkname memmove runtime.memmove
func memmove(to, from unsafe.Pointer, n uintptr)

// MemCpy 复制指定字节数的内存
func MemCpy(dst unsafe.Pointer, src unsafe.Pointer, size uint64) {
	memmove(dst, src, uintptr(size))
}

// MemCpyType 复制指定数量的类型元素
func MemCpyType[T any](dst *T, src *T, size uint64) {
	MemCpy(unsafe.Pointer(dst), unsafe.Pointer(src), size*SizeOf[T]())
}
