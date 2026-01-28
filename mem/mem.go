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

type Allocator interface {
	Malloc(size uint64) unsafe.Pointer
	Free(p unsafe.Pointer) bool
	AlignedMalloc(size uint64, align uint64) unsafe.Pointer
	AlignedFree(p unsafe.Pointer) bool
	GetAllocSize() uint64
}

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

func SizeOf[T any]() uint64 {
	var t T
	return uint64(unsafe.Sizeof(t))
}

func Offset(p unsafe.Pointer, offset int64) unsafe.Pointer {
	if offset > 0 {
		return unsafe.Pointer(uintptr(p) + uintptr(offset))
	} else if offset < 0 {
		return unsafe.Pointer(uintptr(p) - uintptr(-offset))
	} else {
		return p
	}
}

func OffsetType[T any](t *T, offset int64) *T {
	return (*T)(Offset(unsafe.Pointer(t), offset*int64(SizeOf[T]())))
}

//go:linkname memmove runtime.memmove
func memmove(to, from unsafe.Pointer, n uintptr)

func MemCpy(dst unsafe.Pointer, src unsafe.Pointer, size uint64) {
	memmove(dst, src, uintptr(size))
}

func MemCpyType[T any](dst *T, src *T, size uint64) {
	MemCpy(unsafe.Pointer(dst), unsafe.Pointer(src), size*SizeOf[T]())
}
