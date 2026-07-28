package mem

import (
	"sync/atomic"
	"unsafe"
)

const (
	ringBufferLayoutVersion    = uint8(1)                                           // Go/C 共享内存布局版本
	ringBufferRecordHeaderSize = uint64(4)                                          // 单条记录的 uint32 长度头字节数
	ringBufferRecordAlign      = uint64(4)                                          // 单条记录的字节对齐粒度
	ringBufferMinDataSize      = ringBufferRecordHeaderSize + ringBufferRecordAlign // 环形缓冲区数据区最小字节数
	ringBufferMaxPacketSize    = uint64(1<<32 - 1)                                  // uint32 能表示的最大单包字节数
	ringBufferMaxDataSize      = uint64(1 << 62)                                    // 环形缓冲区数据区最大字节数
)

// RingBuffer 表示单生产者单消费者环形报文缓冲区
type RingBuffer struct {
	head   uint64         // 写入位置
	_      [56]byte       // 头尾位置之间的缓存行填充
	tail   uint64         // 读取位置
	size   uint64         // 数据区字节大小
	mask   uint64         // 环形索引掩码
	buffer unsafe.Pointer // 数据区地址
	_      [32]byte       // 结构体尾部的缓存行填充
}

// RingBufferProducer 保存单个生产者独占的本地游标
type RingBufferProducer struct {
	rb         *RingBuffer    // 关联的环形缓冲区
	buffer     unsafe.Pointer // 当前映射中的数据区地址
	head       uint64         // 生产者本地写入位置
	cachedTail uint64         // 缓存的消费者读取位置
	_          [32]byte       // 独占缓存行填充
}

// RingBufferConsumer 保存单个消费者独占的本地游标
type RingBufferConsumer struct {
	rb         *RingBuffer    // 关联的环形缓冲区
	buffer     unsafe.Pointer // 当前映射中的数据区地址
	tail       uint64         // 消费者本地读取位置
	cachedHead uint64         // 缓存的生产者写入位置
	_          [32]byte       // 独占缓存行填充
}

// ringBufferRecordSize 返回包含长度头和对齐填充的记录大小
func ringBufferRecordSize(dataLen uint32) uint64 {
	totalSize := ringBufferRecordHeaderSize + uint64(dataLen)
	return (totalSize + ringBufferRecordAlign - 1) & ^(ringBufferRecordAlign - 1)
}

// ringBufferStructureValid 校验环形缓冲区固定元数据
func ringBufferStructureValid(rb *RingBuffer) bool {
	if rb == nil || rb.buffer == nil {
		return false
	}
	if rb.size < ringBufferMinDataSize || rb.size > ringBufferMaxDataSize ||
		rb.size&(rb.size-1) != 0 || rb.mask != rb.size-1 {
		return false
	}
	return true
}

// ringBufferLocalData 解析当前映射中的数据区地址并校验映射偏移
func ringBufferLocalData(rb *RingBuffer, offset int64) (unsafe.Pointer, bool) {
	if rb == nil || rb.buffer == nil {
		return nil, false
	}
	localBuffer := unsafe.Add(unsafe.Pointer(rb), SizeOf[RingBuffer]())
	localAddress := uintptr(localBuffer)
	storedAddress := uintptr(rb.buffer)
	var expectedOffset int64
	if localAddress >= storedAddress {
		delta := uint64(localAddress - storedAddress)
		if delta > ^uint64(0)>>1 {
			return nil, false
		}
		expectedOffset = int64(delta)
	} else {
		delta := uint64(storedAddress - localAddress)
		if delta > ^uint64(0)>>1 {
			return nil, false
		}
		expectedOffset = -int64(delta)
	}
	if offset != expectedOffset {
		return nil, false
	}
	return localBuffer, true
}

// RingBufferCreate 在指定内存上初始化环形缓冲区
func RingBufferCreate(memory unsafe.Pointer, size uint64) *RingBuffer {
	if memory == nil {
		return nil
	}
	headerSize := SizeOf[RingBuffer]()
	if size < headerSize+ringBufferMinDataSize || size > headerSize+ringBufferMaxDataSize {
		return nil
	}
	// 调用方传入总大小 头部之后的剩余区域必须为不小于 8 的 2 次幂
	size -= headerSize
	if size < ringBufferMinDataSize || size&(size-1) != 0 {
		return nil
	}

	rb := (*RingBuffer)(memory)
	atomic.StoreUint64(&rb.head, 0)
	atomic.StoreUint64(&rb.tail, 0)
	rb.size = size
	rb.mask = size - 1
	rb.buffer = unsafe.Add(memory, headerSize)

	// 布局版本和固定填充值用于跨进程映射时校验记录格式及 Go/C 结构布局
	*(*uint8)(Offset(unsafe.Pointer(rb), 8)) = ringBufferLayoutVersion
	for i := 9; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xAA
	}
	for i := 96; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0xFF
	}

	return rb
}

// RingBufferDestroy 清空环形缓冲区元数据
func RingBufferDestroy(rb *RingBuffer) {
	if rb == nil {
		return
	}
	atomic.StoreUint64(&rb.head, 0)
	atomic.StoreUint64(&rb.tail, 0)
	rb.size = 0
	rb.mask = 0
	rb.buffer = nil

	for i := 8; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0x00
	}
	for i := 96; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		*v = 0x00
	}
}

// RingBufferMapping 映射已有环形缓冲区并计算数据区地址偏移
func RingBufferMapping(memory unsafe.Pointer, offset *int64) *RingBuffer {
	if memory == nil || offset == nil {
		return nil
	}
	rb := (*RingBuffer)(memory)

	// 布局版本不匹配时拒绝映射 防止把 uint16 长度记录误读成 uint32
	if *(*uint8)(Offset(unsafe.Pointer(rb), 8)) != ringBufferLayoutVersion {
		return nil
	}
	for i := 9; i <= 63; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xAA {
			return nil
		}
	}
	for i := 96; i <= 127; i++ {
		v := (*uint8)(Offset(unsafe.Pointer(rb), int64(i)))
		if *v != 0xFF {
			return nil
		}
	}
	if !ringBufferStructureValid(rb) {
		return nil
	}
	tail := atomic.LoadUint64(&rb.tail)
	head := atomic.LoadUint64(&rb.head)
	if head-tail > rb.size {
		return nil
	}

	headerSize := SizeOf[RingBuffer]()
	// 共享内存在不同进程的虚拟地址可能不同 访问数据区时统一叠加该偏移
	localBuffer := unsafe.Add(memory, headerSize)
	localAddress := uintptr(localBuffer)
	storedAddress := uintptr(rb.buffer)
	if localAddress >= storedAddress {
		delta := uint64(localAddress - storedAddress)
		if delta > ^uint64(0)>>1 {
			return nil
		}
		*offset = int64(delta)
	} else {
		delta := uint64(storedAddress - localAddress)
		if delta > ^uint64(0)>>1 {
			return nil
		}
		*offset = -int64(delta)
	}
	return rb
}

// NewRingBufferProducer 创建独占指定写入端的生产者上下文
func NewRingBufferProducer(rb *RingBuffer, offset int64) *RingBufferProducer {
	if !ringBufferStructureValid(rb) {
		return nil
	}
	buffer, ok := ringBufferLocalData(rb, offset)
	if !ok {
		return nil
	}
	// 先读取对端游标再读取本端游标 保证挂接运行中的环时得到有效占用量
	tail := atomic.LoadUint64(&rb.tail)
	head := atomic.LoadUint64(&rb.head)
	if head-tail > rb.size {
		return nil
	}
	return &RingBufferProducer{
		rb:         rb,
		buffer:     buffer,
		head:       head,
		cachedTail: tail,
	}
}

// NewRingBufferConsumer 创建独占指定读取端的消费者上下文
func NewRingBufferConsumer(rb *RingBuffer, offset int64) *RingBufferConsumer {
	if !ringBufferStructureValid(rb) {
		return nil
	}
	buffer, ok := ringBufferLocalData(rb, offset)
	if !ok {
		return nil
	}
	// 先读取本端游标再读取对端游标 保证挂接运行中的环时不会把旧缓存当成数据
	tail := atomic.LoadUint64(&rb.tail)
	head := atomic.LoadUint64(&rb.head)
	if head-tail > rb.size {
		return nil
	}
	return &RingBufferConsumer{
		rb:         rb,
		buffer:     buffer,
		tail:       tail,
		cachedHead: head,
	}
}

// WritePacket 向生产者独占的环形缓冲区写入报文
func (p *RingBufferProducer) WritePacket(data []byte) bool {
	if p == nil || p.rb == nil || len(data) == 0 || uint64(len(data)) > ringBufferMaxPacketSize {
		return false
	}
	dataLen := uint32(len(data))
	if uint64(dataLen) > p.rb.size/2 {
		return false
	}

	head := p.head
	usedSpace := head - p.cachedTail
	if usedSpace > p.rb.size {
		p.cachedTail = atomic.LoadUint64(&p.rb.tail)
		usedSpace = head - p.cachedTail
		if usedSpace > p.rb.size {
			return false
		}
	}

	totalSize := ringBufferRecordSize(dataLen)
	freeSpace := p.rb.size - usedSpace
	if freeSpace < totalSize {
		// 缓存空间不足时才读取对端缓存线
		p.cachedTail = atomic.LoadUint64(&p.rb.tail)
		usedSpace = head - p.cachedTail
		if usedSpace > p.rb.size || p.rb.size-usedSpace < totalSize {
			return false
		}
	}

	pos := head & p.rb.mask
	*(*uint32)(Offset(p.buffer, int64(pos))) = dataLen
	dataPos := (pos + ringBufferRecordHeaderSize) & p.rb.mask
	spaceAfter := p.rb.size - dataPos
	if spaceAfter >= uint64(dataLen) {
		MemCpy(Offset(p.buffer, int64(dataPos)), unsafe.Pointer(&data[0]), uint64(dataLen))
	} else {
		MemCpy(Offset(p.buffer, int64(dataPos)), unsafe.Pointer(&data[0]), spaceAfter)
		MemCpy(p.buffer, Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), uint64(dataLen)-spaceAfter)
	}

	// 先完成载荷复制再发布 head 消费者不会观察到半条记录
	nextHead := head + totalSize
	p.head = nextHead
	atomic.StoreUint64(&p.rb.head, nextHead)
	return true
}

// ReadPacket 从消费者独占的环形缓冲区读取报文
func (c *RingBufferConsumer) ReadPacket(data []byte, dataLen *uint32) bool {
	if dataLen == nil {
		return false
	}
	*dataLen = 0
	if c == nil || c.rb == nil {
		return false
	}

	tail := c.tail
	usedSpace := c.cachedHead - tail
	if usedSpace > c.rb.size || usedSpace < ringBufferRecordHeaderSize {
		// 缓存无数据或上下文刚挂接运行中的环时读取最新 head
		c.cachedHead = atomic.LoadUint64(&c.rb.head)
		usedSpace = c.cachedHead - tail
		if usedSpace > c.rb.size || usedSpace < ringBufferRecordHeaderSize {
			return false
		}
	}

	pos := tail & c.rb.mask
	packetLen := *(*uint32)(Offset(c.buffer, int64(pos)))
	if packetLen == 0 || uint64(packetLen) > c.rb.size/2 {
		return false
	}
	totalSize := ringBufferRecordSize(packetLen)
	if usedSpace < totalSize {
		c.cachedHead = atomic.LoadUint64(&c.rb.head)
		usedSpace = c.cachedHead - tail
		if usedSpace > c.rb.size || usedSpace < totalSize {
			return false
		}
	}
	if uint64(len(data)) < uint64(packetLen) {
		// 返回所需容量并保持 tail 不变 调用方扩容后可以重试
		*dataLen = packetLen
		return false
	}

	dataPos := (pos + ringBufferRecordHeaderSize) & c.rb.mask
	spaceAfter := c.rb.size - dataPos
	if spaceAfter >= uint64(packetLen) {
		MemCpy(unsafe.Pointer(&data[0]), Offset(c.buffer, int64(dataPos)), uint64(packetLen))
	} else {
		MemCpy(unsafe.Pointer(&data[0]), Offset(c.buffer, int64(dataPos)), spaceAfter)
		MemCpy(Offset(unsafe.Pointer(&data[0]), int64(spaceAfter)), c.buffer, uint64(packetLen)-spaceAfter)
	}
	*dataLen = packetLen

	// 数据复制完成后再发布 tail 生产者才能复用对应空间
	nextTail := tail + totalSize
	c.tail = nextTail
	atomic.StoreUint64(&c.rb.tail, nextTail)
	return true
}

// SliceHeader 描述切片的底层地址 长度和容量
type SliceHeader struct {
	Data uintptr // 底层数组地址
	Len  int     // 切片长度
	Cap  int     // 切片容量
}
