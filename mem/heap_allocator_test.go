package mem

import (
	"testing"
	"time"
)

// TestHeapAllocator 验证堆分配器的大块内存读写
func TestHeapAllocator(t *testing.T) {
	var heapAllocator Allocator = GetHeapAllocator()
	ptr := heapAllocator.Malloc(1 * GB)
	for i := 0; i < 1*GB; i++ {
		v := (*uint8)(Offset(ptr, int64(i)))
		*v = 0xFF
	}
	time.Sleep(time.Second * 1)
	for i := 0; i < 1*GB; i++ {
		v := (*uint8)(Offset(ptr, int64(i)))
		if *v != 0xFF {
			panic("???")
		}
	}
	heapAllocator.Free(ptr)
}
