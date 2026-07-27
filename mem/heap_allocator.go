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

// HeapAllocator 使用 Go 运行时堆分配内存
type HeapAllocator struct{}

// GetHeapAllocator 返回堆内存分配器
func GetHeapAllocator() HeapAllocator {
	return struct{}{}
}

// _type 对应 Go 运行时的类型元数据占位结构
type _type struct{}

// mallocgc 调用 Go 运行时分配堆内存
//
//go:linkname mallocgc runtime.mallocgc
func mallocgc(size uintptr, typ *_type, needzero bool) unsafe.Pointer

// Malloc 从 Go 运行时堆分配指定字节数的内存
func (h HeapAllocator) Malloc(size uint64) unsafe.Pointer {
	p := mallocgc(uintptr(size), nil, true)
	return p
}

// Free 保持指针存活并将内存回收交给 Go 运行时
func (h HeapAllocator) Free(p unsafe.Pointer) bool {
	runtime.KeepAlive(p)
	return true
}

// AlignedMalloc 从 Go 运行时堆分配按指定边界对齐的内存
func (h HeapAllocator) AlignedMalloc(size uint64, align uint64) unsafe.Pointer {
	if align == 0 {
		align = CACHE_LINE_SIZE
	}
	// 额外预留对齐余量和一个原始地址槽位
	total := size + align - 1 + SizeOf[uintptr]()
	raw := h.Malloc(total)
	if raw == nil {
		return nil
	}
	rawAddr := uintptr(raw)
	// 先越过地址槽位 再用位掩码向上对齐
	alignedAddr := rawAddr + uintptr(SizeOf[uintptr]())
	alignedAddr = (alignedAddr + uintptr(align) - 1) & ^(uintptr(align) - 1)
	// 对齐地址前保存原始分配地址供释放时恢复
	origPtrAddr := alignedAddr - uintptr(SizeOf[uintptr]())
	*(*uintptr)(unsafe.Pointer(origPtrAddr)) = rawAddr
	return unsafe.Pointer(alignedAddr)
}

// AlignedFree 释放对齐分配的 Go 堆内存引用
func (h HeapAllocator) AlignedFree(p unsafe.Pointer) bool {
	if p == nil {
		return false
	}
	// 从对齐地址前的槽位取回 Go 运行时分配的原始地址
	alignedAddr := uintptr(p)
	origPtrAddr := alignedAddr - unsafe.Sizeof(uintptr(0))
	rawAddr := *(*uintptr)(unsafe.Pointer(origPtrAddr))
	h.Free(unsafe.Pointer(rawAddr))
	return true
}

// GetAllocSize 返回 0 当前堆分配器不统计已分配字节数
func (h HeapAllocator) GetAllocSize() uint64 {
	return 0
}

// GetShareMem 在不启用 CGO 时报告不支持共享内存
func GetShareMem(name string, size uint64) unsafe.Pointer {
	return nil
}
