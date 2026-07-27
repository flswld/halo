package hashmap

import (
	"encoding/json"
	"errors"

	"github.com/flswld/halo/list"
	"github.com/flswld/halo/mem"
)

const (
	initBucketSize = 8
	growBucketLoad = 0.75
)

// MapKey 约束可比较且可计算哈希值的键类型
type MapKey interface {
	comparable
	// GetHashCode 返回键的哈希值
	GetHashCode() uint64
}

// HashMap 提供使用自定义分配器的泛型哈希表
type HashMap[K MapKey, V any] struct {
	bucket    *list.ArrayList[*entry[K, V]] // 哈希桶数组
	load      int                           // 非空哈希桶数量
	len       int                           // 键值对数量
	allocator mem.Allocator                 // 内存分配器
}

// entry 保存哈希桶链表中的一个键值对
type entry[K MapKey, V any] struct {
	key   K            // 键
	value V            // 值
	front *entry[K, V] // 前一节点
	next  *entry[K, V] // 后一节点
}

// NewHashMap 使用默认容量创建哈希表
func NewHashMap[K MapKey, V any](allocator mem.Allocator) *HashMap[K, V] {
	return NewHashMapWithCap[K, V](allocator, initBucketSize)
}

// NewHashMapWithCap 使用指定初始容量创建哈希表
func NewHashMapWithCap[K MapKey, V any](allocator mem.Allocator, cap int) *HashMap[K, V] {
	if cap < initBucketSize {
		cap = initBucketSize
	}
	m := mem.MallocType[HashMap[K, V]](allocator, 1)
	if m == nil {
		return nil
	}
	m.bucket = list.NewArrayListWithCap[*entry[K, V]](allocator, cap)
	if m.bucket == nil {
		mem.FreeType[HashMap[K, V]](allocator, m)
		return nil
	}
	for i := 0; i < cap; i++ {
		m.bucket.Add(nil)
	}
	m.load = 0
	m.len = 0
	m.allocator = allocator
	return m
}

// Get 查询指定键对应的值
func (m *HashMap[K, V]) Get(key K) (V, bool) {
	i := key.GetHashCode() % uint64(m.bucket.Len())
	e := m.bucket.Get(int(i))
	if e == nil {
		var v V
		return v, false
	}
	for {
		if e.key == key {
			return e.value, true
		}
		if e.next == nil {
			var v V
			return v, false
		}
		e = e.next
	}
}

// Set 新增或更新键值对
func (m *HashMap[K, V]) Set(key K, value V) bool {
	i := key.GetHashCode() % uint64(m.bucket.Len())
	e := m.bucket.Get(int(i))
	if e == nil {
		// 首个元素直接成为桶头并增加非空桶计数
		ne := mem.MallocType[entry[K, V]](m.allocator, 1)
		if ne == nil {
			return false
		}
		ne.key = key
		ne.value = value
		ne.front = nil
		ne.next = nil
		m.bucket.Set(int(i), ne)
		m.load++
		m.len++
		return true
	}
	for {
		if e.key == key {
			e.key = key
			e.value = value
			return true
		}
		if e.next == nil {
			// 哈希冲突通过桶内双向链表串接
			ne := mem.MallocType[entry[K, V]](m.allocator, 1)
			if ne == nil {
				return false
			}
			ne.key = key
			ne.value = value
			ne.front = e
			ne.next = nil
			e.next = ne
			m.len++
			if float32(m.load)/float32(m.bucket.Len()) > growBucketLoad {
				m.Grow()
			}
			return true
		}
		e = e.next
	}
}

// Grow 将哈希桶数量扩展为当前的两倍
func (m *HashMap[K, V]) Grow() {
	// 先构建完整新桶表 失败时释放临时结构并保留原表
	b := list.NewArrayListWithCap[*entry[K, V]](m.allocator, m.bucket.Len()*2)
	if b == nil {
		return
	}
	for i := 0; i < m.bucket.Len()*2; i++ {
		b.Add(nil)
	}
	bl := 0
	l := 0
	fail := false
	m.For(func(key K, value V) (next bool) {
		i := key.GetHashCode() % uint64(b.Len())
		e := b.Get(int(i))
		if e == nil {
			ne := mem.MallocType[entry[K, V]](m.allocator, 1)
			if ne == nil {
				fail = true
				return false
			}
			ne.key = key
			ne.value = value
			ne.front = nil
			ne.next = nil
			b.Set(int(i), ne)
			bl++
			l++
			return true
		}
		for {
			if e.key == key {
				e.key = key
				e.value = value
				return true
			}
			if e.next == nil {
				ne := mem.MallocType[entry[K, V]](m.allocator, 1)
				if ne == nil {
					fail = true
					return false
				}
				ne.key = key
				ne.value = value
				ne.front = e
				ne.next = nil
				e.next = ne
				l++
				return true
			}
			e = e.next
		}
	})
	if fail {
		b.For(func(index int, e *entry[K, V]) (next bool) {
			for {
				if e == nil {
					break
				}
				ee := e
				e = e.next
				mem.FreeType[entry[K, V]](m.allocator, ee)
			}
			return true
		})
		b.Free()
		return
	}
	// 仅在全部元素重散列成功后切换桶表
	m.Clear()
	m.bucket.Free()
	m.bucket = b
	m.load = bl
	m.len = l
}

// Del 删除指定键及其值
func (m *HashMap[K, V]) Del(key K) {
	i := key.GetHashCode() % uint64(m.bucket.Len())
	e := m.bucket.Get(int(i))
	if e == nil {
		return
	}
	if e.key == key {
		m.bucket.Set(int(i), e.next)
		if e.next == nil {
			m.load--
		}
		mem.FreeType[entry[K, V]](m.allocator, e)
		m.len--
		return
	}
	for {
		if e.key == key {
			if e.front != nil {
				e.front.next = e.next
			}
			if e.next != nil {
				e.next.front = e.front
			}
			mem.FreeType[entry[K, V]](m.allocator, e)
			m.len--
			return
		}
		if e.next == nil {
			return
		}
		e = e.next
	}
}

// For 按桶顺序遍历键值对
func (m *HashMap[K, V]) For(fn func(key K, value V) (next bool)) {
	m.bucket.For(func(index int, e *entry[K, V]) (next bool) {
		for {
			if e == nil {
				break
			}
			ne := e.next
			n := fn(e.key, e.value)
			if !n {
				return false
			}
			e = ne
		}
		return true
	})
}

// Len 返回键值对数量
func (m *HashMap[K, V]) Len() int {
	return m.len
}

// Clear 清空全部键值对并保留哈希桶
func (m *HashMap[K, V]) Clear() {
	m.bucket.For(func(index int, e *entry[K, V]) (next bool) {
		for {
			if e == nil {
				break
			}
			ee := e
			e = e.next
			mem.FreeType[entry[K, V]](m.allocator, ee)
		}
		return true
	})
	for i := 0; i < m.bucket.Len(); i++ {
		m.bucket.Set(i, nil)
	}
	m.load = 0
}

// Free 释放哈希表占用的全部内存
func (m *HashMap[K, V]) Free() {
	m.Clear()
	m.bucket.Free()
	mem.FreeType[HashMap[K, V]](m.allocator, m)
}

// MarshalJSON 将哈希表编码为 JSON
func (m *HashMap[K, V]) MarshalJSON() ([]byte, error) {
	mm := make(map[K]V)
	m.For(func(key K, value V) (next bool) {
		mm[key] = value
		return true
	})
	data, err := json.Marshal(mm)
	return data, err
}

// UnmarshalJSON 从 JSON 解码并追加键值对
func (m *HashMap[K, V]) UnmarshalJSON(data []byte) error {
	mm := make(map[K]V)
	err := json.Unmarshal(data, &mm)
	if err != nil {
		return err
	}
	for k, v := range mm {
		ok := m.Set(k, v)
		if !ok {
			return errors.New("overflow")
		}
	}
	return nil
}

// GetHashCode 使用乘法散列算法计算字节序列的哈希值
func GetHashCode(data []byte) uint64 {
	hashCode := uint64(0)
	for _, v := range data {
		hashCode = uint64(v) + 131*hashCode
	}
	return hashCode
}
