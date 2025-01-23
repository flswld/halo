package hashmap

import (
	"log"
	"testing"

	"github.com/flswld/halo/mem"
)

func TestHashMap(t *testing.T) {
	goHeap := mem.NewGoHeap()
	ptr := goHeap.Malloc(1 * mem.GB)
	staticHeap := mem.NewStaticHeap(ptr, 1*mem.GB)
	hashMap := NewHashMap[int, uint64](staticHeap)
	for i := 0; i < 100; i++ {
		hashMap.Set(i, uint64(i+10000))
	}
	for i := 90; i < 100; i++ {
		hashMap.Del(i)
	}
	for i := 0; i < 10; i++ {
		hashMap.Set(i, 666)
	}
	hashMap.For(func(key int, value uint64) (next bool) {
		log.Printf("key: %d, value: %d\n", key, value)
		return true
	})
	hashMap.Free()
	goHeap.Free(ptr)
}
