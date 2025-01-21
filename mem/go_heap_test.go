package mem

import (
	"testing"
	"time"
)

func TestGoHeap(t *testing.T) {
	var goHeap Heap = NewGoHeap()
	ptr := goHeap.Malloc(8 * GB)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr, uint64(i)))
		*v = 0xFF
	}
	time.Sleep(time.Second * 5)
	for i := 0; i < 8*GB; i++ {
		v := (*uint8)(Offset(ptr, uint64(i)))
		if *v != 0xFF {
			panic("???")
		}
	}
	goHeap.Free(ptr)
}
