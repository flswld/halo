package hashmap

import (
	"encoding/binary"
	"log"
	"testing"

	"github.com/flswld/halo/mem"
)

type Key uint32

func (k Key) GetHashCode() uint64 {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, uint32(k))
	return GetHashCode(data)
}

func TestHashMap(t *testing.T) {
	goHeap := mem.NewGoHeap()
	ptr := goHeap.Malloc(1 * mem.GB)
	staticHeap := mem.NewStaticHeap(ptr, 1*mem.GB)
	hashMap := NewHashMap[Key, uint64](staticHeap)
	for i := 0; i < 100; i++ {
		hashMap.Set(Key(i), uint64(i+10000))
	}
	for i := 90; i < 100; i++ {
		hashMap.Del(Key(i))
	}
	for i := 0; i < 10; i++ {
		hashMap.Set(Key(i), 666)
	}
	hashMap.For(func(key Key, value uint64) (next bool) {
		log.Printf("key: %d, value: %d\n", key, value)
		return true
	})
	hashMap.Free()
	goHeap.Free(ptr)
}
