package mem

import (
	"testing"
	"time"
	"unsafe"
)

func TestStaticAllocator(t *testing.T) {
	var heapAllocator Allocator = GetHeapAllocator()
	p := heapAllocator.Malloc(8 * GB)
	var staticAllocator Allocator = NewStaticAllocator(p, 8*GB)
	ptrList := make([]unsafe.Pointer, 4*1024)
	for i := 0; i < len(ptrList); i++ {
		ptr := staticAllocator.Malloc(1 * MB)
		if ptr == nil {
			panic("???")
		}
		for i := 0; i < 1*MB; i++ {
			v := (*uint8)(Offset(ptr, int64(i)))
			*v = 0xFF
		}
		ptrList[i] = ptr
	}
	time.Sleep(time.Second * 5)
	for _, ptr := range ptrList {
		for i := 0; i < 1*MB; i++ {
			v := (*uint8)(Offset(ptr, int64(i)))
			if *v != 0xFF {
				panic("???")
			}
		}
		ok := staticAllocator.Free(ptr)
		if !ok {
			panic("???")
		}
	}
	heapAllocator.Free(p)
}
