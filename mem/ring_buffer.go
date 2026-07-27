package mem

import (
	"sync/atomic"
	"unsafe"
)

// RingBuffer 表示单生产者单消费者环形报文缓冲区
type RingBuffer struct {
	head   uint64         // 写入位置
	_      [56]byte       // 头尾位置之间的缓存行填充
	tail   uint64         // 读取位置
	size   uint32         // 数据区字节大小
	mask   uint32         // 环形索引掩码
	buffer unsafe.Pointer // 数据区地址
	_      [40]byte       // 结构体尾部的缓存行填充
}

// RingBufferCreate 在指定内存上初始化环形缓冲区
func RingBufferCreate(memory unsafe.Pointer, size uint32) *RingBuffer {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[RingBuffer]()
	if uint64(size) < headerSize {
		return nil
	}
	// 调用方传入总大小 头部之后的剩余区域必须为 2 的幂
	size -= uint32(headerSize)
	if (size & (size - 1)) != 0 {
		return nil
	}

	rb := (*RingBuffer)(memory)
	rb.head = 0
	rb.tail = 0
	rb.size = size
	rb.mask = size - 1
	rb.buffer = unsafe.Pointer(uintptr(memory) + uintptr(headerSize))

	// 固定填充值用于跨进程映射时校验 Go 和 C 结构布局是否一致
	for i := 8; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xAA
	}
	for i := 88; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xFF
	}

	return rb
}

// RingBufferDestroy 清空环形缓冲区元数据
func RingBufferDestroy(rb *RingBuffer) {
	if rb != nil {
		rb.head = 0
		rb.tail = 0
		rb.size = 0
		rb.mask = 0
		rb.buffer = nil

		for i := 8; i <= 63; i++ {
			v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
			*v = 0x00
		}
		for i := 88; i <= 127; i++ {
			v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
			*v = 0x00
		}
	}
}

// RingBufferMapping 映射已有环形缓冲区并计算数据区地址偏移
func RingBufferMapping(memory unsafe.Pointer, offset *int64) *RingBuffer {
	if memory == nil {
		return nil
	}
	rb := (*RingBuffer)(memory)

	// 布局标记不匹配时拒绝映射 防止使用错误版本或损坏的共享内存
	for i := 8; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xAA {
			return nil
		}
	}
	for i := 88; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xFF {
			return nil
		}
	}

	headerSize := SizeOf[RingBuffer]()
	// 共享内存在不同进程的虚拟地址可能不同 访问数据区时统一叠加该偏移
	*offset = int64(uintptr(memory) + uintptr(headerSize) - uintptr(rb.buffer))
	return rb
}

// WritePacketOffset 使用指定数据区地址偏移写入报文
func WritePacketOffset(rb *RingBuffer, offset int64, data []uint8, len uint16) bool {
	if len == 0 || uint32(len) > rb.size/2 {
		return false
	}
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	// 单生产者通过单调递增位置计算剩余空间 掩码仅用于访问数据区
	freeSpace := rb.size - uint32(head-tail)
	totalSize := uint32(2 + len)
	totalSize = (totalSize + 3) & ^uint32(3)
	if freeSpace < totalSize {
		return false
	}
	pos := uint32(head & uint64(rb.mask))
	*(*uint16)(Offset(rb.buffer, offset+int64(pos))) = len
	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos
	// 报文跨越数据区末尾时拆成两段复制
	if spaceAfter >= uint32(len) {
		MemCpy(Offset(rb.buffer, offset+int64(dataPos)), unsafe.Pointer(&data[0]), uint64(len))
	} else {
		MemCpy(Offset(rb.buffer, offset+int64(dataPos)), unsafe.Pointer(&data[0]), uint64(spaceAfter))
		MemCpy(Offset(rb.buffer, offset), Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), uint64(uint32(len)-spaceAfter))
	}
	// 最后发布新 head 保证消费者不会看到尚未复制完成的报文
	atomic.StoreUint64(&rb.head, head+uint64(totalSize))
	return true
}

// WritePacket 向环形缓冲区写入报文
func WritePacket(rb *RingBuffer, data []uint8, len uint16) bool {
	return WritePacketOffset(rb, 0, data, len)
}

// ReadPacketOffset 使用指定数据区地址偏移读取报文
func ReadPacketOffset(rb *RingBuffer, offset int64, data []uint8, len *uint16) bool {
	*len = 0
	head := atomic.LoadUint64(&rb.head)
	tail := atomic.LoadUint64(&rb.tail)
	usedSpace := uint32(head - tail)
	if usedSpace < 2 {
		return false
	}
	pos := uint32(tail & uint64(rb.mask))
	packetLen := *(*uint16)(Offset(rb.buffer, offset+int64(pos)))
	if packetLen == 0 || uint32(packetLen) > rb.size/2 {
		return false
	}
	totalSize := uint32(2 + packetLen)
	totalSize = (totalSize + 3) & ^uint32(3)
	// 已用空间不足一条完整记录时保持 tail 不变并等待生产者
	if usedSpace < totalSize {
		return false
	}
	dataPos := (pos + 2) & rb.mask
	spaceAfter := rb.size - dataPos
	if spaceAfter >= uint32(packetLen) {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, offset+int64(dataPos)), uint64(packetLen))
	} else {
		MemCpy(unsafe.Pointer(&data[0]), Offset(rb.buffer, offset+int64(dataPos)), uint64(spaceAfter))
		MemCpy(Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), Offset(rb.buffer, offset), uint64(uint32(packetLen)-spaceAfter))
	}
	*len = packetLen
	// 数据复制完成后再发布新 tail 允许生产者复用对应空间
	atomic.StoreUint64(&rb.tail, tail+uint64(totalSize))
	return true
}

// ReadPacket 从环形缓冲区读取报文
func ReadPacket(rb *RingBuffer, data []uint8, len *uint16) bool {
	return ReadPacketOffset(rb, 0, data, len)
}

// SliceHeader 描述切片的底层地址 长度和容量
type SliceHeader struct {
	Data uintptr // 底层数组地址
	Len  int     // 切片长度
	Cap  int     // 切片容量
}
