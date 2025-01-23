package list

import (
	"log"
	"testing"

	"github.com/flswld/halo/mem"
)

func TestArrayList(t *testing.T) {
	goHeap := mem.NewGoHeap()
	ptr := goHeap.Malloc(1 * mem.GB)
	staticHeap := mem.NewStaticHeap(ptr, 1*mem.GB)
	arrayList := NewArrayList[uint64](staticHeap)
	for i := 0; i < 100; i++ {
		arrayList.Add(uint64(i))
	}
	arrayList.Set(10, 666)
	arrayList.For(func(index int, value uint64) (next bool) {
		log.Printf("index: %d, value: %d\n", index, value)
		return true
	})
	arrayList.Free()
	goHeap.Free(ptr)
}
