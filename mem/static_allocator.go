package mem

import (
	"unsafe"

	"github.com/flswld/halo/cpu"
)

// blockHeader 编码内存块的空闲状态和大小
type blockHeader uint64

// getFree 返回内存块是否空闲
func (h *blockHeader) getFree() bool {
	return (uint64(*h) >> 63) == 1
}

// setFree 设置内存块的空闲状态
func (h *blockHeader) setFree(free bool) {
	x := uint64(0)
	if free {
		x = 1 << 63
	} else {
		x = 0
	}
	*h = blockHeader(x | (uint64(*h) & ((1<<64 - 1) >> 1)))
}

// getSize 返回内存块的数据区大小
func (h *blockHeader) getSize() uint64 {
	return uint64(*h) & ((1<<64 - 1) >> 1)
}

// setSize 设置内存块的数据区大小
func (h *blockHeader) setSize(size uint64) {
	*h = blockHeader((uint64(*h) & (1 << 63)) | (size & ((1<<64 - 1) >> 1)))
}

// block 描述静态内存池中的一个内存块
type block struct {
	header   blockHeader // 内存块头部
	next     *block      // 相邻后一内存块
	prev     *block      // 相邻前一内存块
	freeNext *block      // 空闲链表后一内存块
	freePrev *block      // 空闲链表前一内存块
}

var blockSize uint64 = 0

// init 缓存内存块头部大小
func init() {
	blockSize = SizeOf[block]()
}

// StaticAllocator 在一段固定内存上提供并发安全的动态分配
type StaticAllocator struct {
	allocSize     uint64       // 已分配字节数
	freeBlockList *block       // 空闲内存块链表
	lock          cpu.SpinLock // 分配器元数据锁
}

// NewStaticAllocator 在指定内存区域上创建静态分配器
func NewStaticAllocator(memory unsafe.Pointer, size uint64) *StaticAllocator {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[StaticAllocator]()
	if size < headerSize+blockSize {
		return nil
	}
	h := (*StaticAllocator)(memory)
	// 初始状态只有一个覆盖全部可用区域的空闲块
	b := (*block)(unsafe.Pointer(uintptr(memory) + uintptr(headerSize)))
	b.header.setSize(size - headerSize - blockSize)
	b.header.setFree(true)
	b.next = nil
	b.prev = nil
	b.freeNext = nil
	b.freePrev = nil
	h.allocSize = headerSize
	h.freeBlockList = b
	h.lock = 0
	return h
}

// Malloc 从静态内存池分配指定字节数的内存
func (h *StaticAllocator) Malloc(size uint64) unsafe.Pointer {
	h.lock.Lock()
	defer h.lock.Unlock()
	if size == 0 {
		return nil
	}
	// 所有块按 8 字节对齐 保证后续块头部满足基础类型对齐要求
	size = (size + 7) & ^uint64(7)
	// 空闲链表采用首次适配策略
	for b := h.freeBlockList; b != nil; b = b.freeNext {
		if b.header.getSize() < size {
			continue
		}
		if b.header.getSize()-size > blockSize {
			// 剩余空间足以容纳块头部时拆分出新的物理相邻空闲块
			nb := (*block)(unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize) + uintptr(size)))
			nb.header.setSize(b.header.getSize() - size - blockSize)
			nb.header.setFree(true)
			nb.next = b.next
			nb.prev = b
			if b.next != nil {
				b.next.prev = nb
			}
			b.next = nb
			h.insertFreeBlock(nb)
			b.header.setSize(size)
		}
		h.removeFreeBlock(b)
		// 分配块从空闲链表移除但仍保留在物理块双向链表中
		b.header.setFree(false)
		h.allocSize += blockSize + b.header.getSize()
		return unsafe.Pointer(uintptr(unsafe.Pointer(b)) + uintptr(blockSize))
	}
	return nil
}

// Free 释放静态内存池中的指定内存并合并相邻空闲块
func (h *StaticAllocator) Free(p unsafe.Pointer) bool {
	h.lock.Lock()
	defer h.lock.Unlock()
	if p == nil {
		return false
	}
	b := (*block)(unsafe.Pointer(uintptr(p) - uintptr(blockSize)))
	if b.header.getFree() {
		return false
	}
	b.header.setFree(true)
	h.allocSize -= blockSize + b.header.getSize()
	// 先向后合并连续空闲块
	for b.next != nil && b.next.header.getFree() {
		next := b.next
		h.removeFreeBlock(next)
		b.header.setSize(b.header.getSize() + next.header.getSize() + blockSize)
		b.next = next.next
		if next.next != nil {
			next.next.prev = b
		}
	}
	// 再向前合并并把最终大块重新插入空闲链表
	if b.prev != nil && b.prev.header.getFree() {
		prev := b.prev
		h.removeFreeBlock(prev)
		prev.header.setSize(prev.header.getSize() + b.header.getSize() + blockSize)
		prev.next = b.next
		if b.next != nil {
			b.next.prev = prev
		}
		b = prev
	}
	h.insertFreeBlock(b)
	return true
}

// insertFreeBlock 将内存块插入空闲链表头部
func (h *StaticAllocator) insertFreeBlock(b *block) {
	b.freeNext = h.freeBlockList
	if h.freeBlockList != nil {
		h.freeBlockList.freePrev = b
	}
	b.freePrev = nil
	h.freeBlockList = b
}

// removeFreeBlock 从空闲链表移除内存块
func (h *StaticAllocator) removeFreeBlock(b *block) {
	if b.freePrev != nil {
		b.freePrev.freeNext = b.freeNext
	} else {
		h.freeBlockList = b.freeNext
	}
	if b.freeNext != nil {
		b.freeNext.freePrev = b.freePrev
	}
	b.freeNext = nil
	b.freePrev = nil
}

// AlignedMalloc 报告静态分配器暂不支持对齐分配
func (h *StaticAllocator) AlignedMalloc(size uint64, align uint64) unsafe.Pointer {
	return nil
}

// AlignedFree 报告静态分配器暂不支持释放对齐内存
func (h *StaticAllocator) AlignedFree(p unsafe.Pointer) bool {
	return false
}

// GetAllocSize 返回静态内存池的已分配字节数
func (h *StaticAllocator) GetAllocSize() uint64 {
	h.lock.Lock()
	defer h.lock.Unlock()
	return h.allocSize
}

// StaticString64 提供最大 63 字节的定长字符串存储
type StaticString64 [64]uint8

// Get 返回定长存储中的字符串
func (s *StaticString64) Get() string {
	var l uint8 = 0
	// 最后一个字节保存有效字符串长度
	l = (*s)[64-1]
	return unsafe.String(&(*s)[0], l)
}

// Set 将字符串截断后写入定长存储
func (s *StaticString64) Set(v string) {
	l := uint8(len(v))
	if l > 64-1 {
		l = 64 - 1
	}
	(*s)[64-1] = l
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}

// String 返回定长存储中的字符串
func (s StaticString64) String() string {
	return s.Get()
}

// StaticString1K 提供最大 1022 字节的定长字符串存储
type StaticString1K [1 * KB]uint8

// Get 返回定长存储中的字符串
func (s *StaticString1K) Get() string {
	var l uint16 = 0
	// 最后两个字节按大端顺序保存有效字符串长度
	l |= uint16((*s)[1*KB-1]) << 8
	l |= uint16((*s)[1*KB-2])
	return unsafe.String(&(*s)[0], l)
}

// Set 将字符串截断后写入定长存储
func (s *StaticString1K) Set(v string) {
	l := uint16(len(v))
	if l > 1*KB-2 {
		l = 1*KB - 2
	}
	(*s)[1*KB-1] = uint8(l >> 8)
	(*s)[1*KB-2] = uint8(l)
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}

// String 返回定长存储中的字符串
func (s StaticString1K) String() string {
	return s.Get()
}

// StaticString1M 提供最大 1 MiB 减 4 字节的定长字符串存储
type StaticString1M [1 * MB]uint8

// Get 返回定长存储中的字符串
func (s *StaticString1M) Get() string {
	var l uint32 = 0
	// 最后四个字节按大端顺序保存有效字符串长度
	l |= uint32((*s)[1*MB-1]) << 24
	l |= uint32((*s)[1*MB-2]) << 16
	l |= uint32((*s)[1*MB-3]) << 8
	l |= uint32((*s)[1*MB-4])
	return unsafe.String(&(*s)[0], l)
}

// Set 将字符串截断后写入定长存储
func (s *StaticString1M) Set(v string) {
	l := uint32(len(v))
	if l > 1*MB-4 {
		l = 1*MB - 4
	}
	(*s)[1*MB-1] = uint8(l >> 24)
	(*s)[1*MB-2] = uint8(l >> 16)
	(*s)[1*MB-3] = uint8(l >> 8)
	(*s)[1*MB-4] = uint8(l)
	MemCpy(unsafe.Pointer(s), unsafe.Pointer(&unsafe.Slice(unsafe.StringData(v), len(v))[0]), uint64(l))
}

// String 返回定长存储中的字符串
func (s StaticString1M) String() string {
	return s.Get()
}
