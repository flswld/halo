package mem

import (
	"testing"
	"time"
)

func TestMalloc(t *testing.T) {
	var goHeap Heap = NewGoHeap()
	p := MallocType[uint8](goHeap, 8*GB)
	for i := 0; i < 8*GB; i++ {
		v := OffsetType[uint8](p, uint64(i))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := OffsetType[uint8](p, uint64(i))
		if *v != 0xFF {
			panic("???")
		}
	}
	FreeType[uint8](goHeap, p)
}

func TestMemCpy(t *testing.T) {
	var goHeap Heap = NewGoHeap()
	ptr1 := goHeap.Malloc(8 * GB)
	ptr2 := goHeap.Malloc(8 * GB)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr1, uint64(i)))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	MemCpy(ptr2, ptr1, 8*GB)
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr2, uint64(i)))
		if *v != 0xFF {
			panic("???")
		}
	}
	goHeap.Free(ptr1)
	goHeap.Free(ptr2)
}
