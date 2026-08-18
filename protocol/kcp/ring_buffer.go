package kcp

const (
	// ringBufferMinCapacity 是动态环形队列的最小容量
	ringBufferMinCapacity = 8
	// ringBufferGrowthThreshold 是切换到平缓扩容策略的容量阈值
	ringBufferGrowthThreshold = 1024
)

// ringBuffer 是供 KCP 单会话内部使用的动态环形队列
//
// KCP 已通过 Session 锁串行访问该结构 因此这里不提供额外并发同步
type ringBuffer[T any] struct {
	elements []T
	head     int
	length   int
}

// newRingBuffer 创建至少具有指定容量的环形队列
func newRingBuffer[T any](capacity int) *ringBuffer[T] {
	if capacity < ringBufferMinCapacity {
		capacity = ringBufferMinCapacity
	}
	return &ringBuffer[T]{elements: make([]T, capacity)}
}

// Len 返回当前元素数量
func (r *ringBuffer[T]) Len() int {
	return r.length
}

// Push 在队尾追加元素并在容量不足时自动扩容
func (r *ringBuffer[T]) Push(value T) {
	if r.length == len(r.elements) {
		r.grow()
	}

	index := r.head + r.length
	if index >= len(r.elements) {
		index -= len(r.elements)
	}
	r.elements[index] = value
	r.length++
}

// Pop 移除并返回队首元素
func (r *ringBuffer[T]) Pop() (T, bool) {
	var zero T
	if r.length == 0 {
		return zero, false
	}

	value := r.elements[r.head]
	r.elements[r.head] = zero
	r.head++
	if r.head == len(r.elements) {
		r.head = 0
	}
	r.length--
	if r.length == 0 {
		r.head = 0
	}
	return value, true
}

// Peek 返回队首元素指针但不移除元素
func (r *ringBuffer[T]) Peek() (*T, bool) {
	if r.length == 0 {
		return nil, false
	}
	return &r.elements[r.head], true
}

// Last 返回队尾元素指针但不移除元素
func (r *ringBuffer[T]) Last() (*T, bool) {
	if r.length == 0 {
		return nil, false
	}

	index := r.head + r.length - 1
	if index >= len(r.elements) {
		index -= len(r.elements)
	}
	return &r.elements[index], true
}

// Discard 清理并移除队首最多 count 个元素
func (r *ringBuffer[T]) Discard(count int) int {
	if count <= 0 || r.length == 0 {
		return 0
	}
	if count > r.length {
		count = r.length
	}

	firstPart := min(count, len(r.elements)-r.head)
	clear(r.elements[r.head : r.head+firstPart])
	clear(r.elements[:count-firstPart])
	r.head += count
	if r.head >= len(r.elements) {
		r.head -= len(r.elements)
	}
	r.length -= count
	if r.length == 0 {
		r.head = 0
	}
	return count
}

// ForEach 按队列顺序访问元素并允许提前停止
func (r *ringBuffer[T]) ForEach(visit func(*T) bool) {
	firstPart := min(r.length, len(r.elements)-r.head)
	for i := 0; i < firstPart; i++ {
		if !visit(&r.elements[r.head+i]) {
			return
		}
	}
	for i := 0; i < r.length-firstPart; i++ {
		if !visit(&r.elements[i]) {
			return
		}
	}
}

// Clear 清理全部元素并重置读写位置
func (r *ringBuffer[T]) Clear() {
	r.Discard(r.length)
}

// grow 扩大底层数组并保持现有元素的逻辑顺序
func (r *ringBuffer[T]) grow() {
	currentCapacity := len(r.elements)
	newCapacity := currentCapacity * 2
	if currentCapacity >= ringBufferGrowthThreshold {
		newCapacity = currentCapacity + currentCapacity/4
	}

	newElements := make([]T, newCapacity)
	firstPart := min(r.length, currentCapacity-r.head)
	copy(newElements, r.elements[r.head:r.head+firstPart])
	copy(newElements[firstPart:], r.elements[:r.length-firstPart])
	r.elements = newElements
	r.head = 0
}
