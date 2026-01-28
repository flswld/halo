package mem

import (
	"testing"
	"time"
)

func TestMalloc(t *testing.T) {
	var heapAllocator Allocator = GetHeapAllocator()
	p := MallocType[uint8](heapAllocator, 8*GB)
	for i := 0; i < 8*GB; i++ {
		v := OffsetType[uint8](p, int64(i))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := OffsetType[uint8](p, int64(i))
		if *v != 0xFF {
			panic("???")
		}
	}
	FreeType[uint8](heapAllocator, p)
}

func TestMemCpy(t *testing.T) {
	var heapAllocator Allocator = GetHeapAllocator()
	ptr1 := heapAllocator.Malloc(8 * GB)
	ptr2 := heapAllocator.Malloc(8 * GB)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr1, int64(i)))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	MemCpy(ptr2, ptr1, 8*GB)
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr2, int64(i)))
		if *v != 0xFF {
			panic("???")
		}
	}
	heapAllocator.Free(ptr1)
	heapAllocator.Free(ptr2)
}
