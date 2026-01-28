package mem

import (
	"testing"
	"time"
)

func TestHeapAllocator(t *testing.T) {
	var heapAllocator Allocator = GetHeapAllocator()
	ptr := heapAllocator.Malloc(8 * GB)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr, int64(i)))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr, int64(i)))
		if *v != 0xFF {
			panic("???")
		}
	}
	heapAllocator.Free(ptr)
}
