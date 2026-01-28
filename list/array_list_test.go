package list

import (
	"log"
	"testing"

	"github.com/flswld/halo/mem"
)

func TestArrayList(t *testing.T) {
	heapAllocator := mem.GetHeapAllocator()
	ptr := heapAllocator.Malloc(1 * mem.GB)
	staticAllocator := mem.NewStaticAllocator(ptr, 1*mem.GB)
	arrayList := NewArrayList[uint64](staticAllocator)
	for i := 0; i < 100; i++ {
		arrayList.Add(uint64(i))
	}
	arrayList.Set(10, 666)
	arrayList.For(func(index int, value uint64) (next bool) {
		log.Printf("index: %d, value: %d\n", index, value)
		return true
	})
	arrayList.Free()
	heapAllocator.Free(ptr)
}
