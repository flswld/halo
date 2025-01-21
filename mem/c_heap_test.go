package mem

import (
	"testing"
	"time"
)

func TestCHeap(t *testing.T) {
	var cHeap Heap = NewCHeap()
	ptr := cHeap.Malloc(8 * GB)
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
	cHeap.Free(ptr)
}
