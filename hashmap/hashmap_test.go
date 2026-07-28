package hashmap

import (
	"encoding/binary"
	"log"
	"testing"

	"github.com/flswld/halo/mem"
)

// Key 表示哈希表测试使用的键
type Key uint32

// GetHashCode 计算测试键的哈希值
func (k Key) GetHashCode() uint64 {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, uint32(k))
	return GetHashCode(data)
}

// TestHashMap 验证哈希表的增删改查和遍历
func TestHashMap(t *testing.T) {
	heapAllocator := mem.GetHeapAllocator()
	ptr := heapAllocator.Malloc(1 * mem.MB)
	staticAllocator := mem.NewStaticAllocator(ptr, 1*mem.MB)
	hashMap := NewHashMap[Key, uint64](staticAllocator)
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
	heapAllocator.Free(ptr)
}
