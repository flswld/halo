package mem

import (
	"testing"
	"time"
	"unsafe"
)

func TestStaticHeap(t *testing.T) {
	var goHeap Heap = NewGoHeap()
	p := goHeap.Malloc(8 * GB)
	var staticHeap Heap = NewStaticHeap(p, 8*GB)
	ptrList := make([]unsafe.Pointer, 4*1024)
	for i := 0; i < len(ptrList); i++ {
		ptr := staticHeap.Malloc(1 * MB)
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
		ok := staticHeap.Free(ptr)
		if !ok {
			panic("???")
		}
	}
	goHeap.Free(p)
}
