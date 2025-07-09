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

type MapKey interface {
	comparable
	GetHashCode() uint64
}

type HashMap[K MapKey, V any] struct {
	bucket *list.ArrayList[*entry[K, V]]
	load   int
	len    int
	heap   mem.Heap
}

type entry[K MapKey, V any] struct {
	key   K
	value V
	front *entry[K, V]
	next  *entry[K, V]
}

func NewHashMap[K MapKey, V any](heap mem.Heap) *HashMap[K, V] {
	return NewHashMapWithCap[K, V](heap, initBucketSize)
}

func NewHashMapWithCap[K MapKey, V any](heap mem.Heap, cap int) *HashMap[K, V] {
	if cap < initBucketSize {
		cap = initBucketSize
	}
	m := mem.MallocType[HashMap[K, V]](heap, 1)
	if m == nil {
		return nil
	}
	m.bucket = list.NewArrayListWithCap[*entry[K, V]](heap, cap)
	if m.bucket == nil {
		mem.FreeType[HashMap[K, V]](heap, m)
		return nil
	}
	for i := 0; i < cap; i++ {
		m.bucket.Add(nil)
	}
	m.load = 0
	m.len = 0
	m.heap = heap
	return m
}

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

func (m *HashMap[K, V]) Set(key K, value V) bool {
	i := key.GetHashCode() % uint64(m.bucket.Len())
	e := m.bucket.Get(int(i))
	if e == nil {
		ne := mem.MallocType[entry[K, V]](m.heap, 1)
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
			ne := mem.MallocType[entry[K, V]](m.heap, 1)
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

func (m *HashMap[K, V]) Grow() {
	b := list.NewArrayListWithCap[*entry[K, V]](m.heap, m.bucket.Len()*2)
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
			ne := mem.MallocType[entry[K, V]](m.heap, 1)
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
				ne := mem.MallocType[entry[K, V]](m.heap, 1)
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
				mem.FreeType[entry[K, V]](m.heap, ee)
			}
			return true
		})
		b.Free()
		return
	}
	m.Clear()
	m.bucket.Free()
	m.bucket = b
	m.load = bl
	m.len = l
}

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
		mem.FreeType[entry[K, V]](m.heap, e)
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
			mem.FreeType[entry[K, V]](m.heap, e)
			m.len--
			return
		}
		if e.next == nil {
			return
		}
		e = e.next
	}
}

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

func (m *HashMap[K, V]) Len() int {
	return m.len
}

func (m *HashMap[K, V]) Clear() {
	m.bucket.For(func(index int, e *entry[K, V]) (next bool) {
		for {
			if e == nil {
				break
			}
			ee := e
			e = e.next
			mem.FreeType[entry[K, V]](m.heap, ee)
		}
		return true
	})
	for i := 0; i < m.bucket.Len(); i++ {
		m.bucket.Set(i, nil)
	}
	m.load = 0
}

func (m *HashMap[K, V]) Free() {
	m.Clear()
	m.bucket.Free()
	mem.FreeType[HashMap[K, V]](m.heap, m)
}

func (m *HashMap[K, V]) MarshalJSON() ([]byte, error) {
	mm := make(map[K]V)
	m.For(func(key K, value V) (next bool) {
		mm[key] = value
		return true
	})
	data, err := json.Marshal(mm)
	return data, err
}

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

func GetHashCode(data []byte) uint64 {
	hashCode := uint64(0)
	for _, v := range data {
		hashCode = uint64(v) + 131*hashCode
	}
	return hashCode
}
