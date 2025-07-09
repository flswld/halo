package list

import (
	"encoding/json"
	"errors"

	"github.com/flswld/halo/mem"
)

const (
	initCap = 8
)

type ArrayList[T any] struct {
	data *T
	len  int
	cap  int
	heap mem.Heap
}

func NewArrayList[T any](heap mem.Heap) *ArrayList[T] {
	return NewArrayListWithCap[T](heap, initCap)
}

func NewArrayListWithCap[T any](heap mem.Heap, cap int) *ArrayList[T] {
	if cap < initCap {
		cap = initCap
	}
	a := mem.MallocType[ArrayList[T]](heap, 1)
	if a == nil {
		return nil
	}
	a.data = mem.MallocType[T](heap, uint64(cap))
	if a.data == nil {
		mem.FreeType[ArrayList[T]](heap, a)
		return nil
	}
	a.len = 0
	a.cap = cap
	a.heap = heap
	return a
}

func (a *ArrayList[T]) Len() int {
	return a.len
}

func (a *ArrayList[T]) Add(value T) bool {
	if a.len >= a.cap {
		data := mem.MallocType[T](a.heap, uint64(a.cap*2))
		if data == nil {
			return false
		}
		mem.MemCpyType[T](data, a.data, uint64(a.cap))
		mem.FreeType[T](a.heap, a.data)
		a.data = data
		a.cap *= 2
	}
	p := mem.OffsetType[T](a.data, int64(a.len))
	*p = value
	a.len++
	return true
}

func (a *ArrayList[T]) Set(index int, value T) {
	if index >= a.len {
		return
	}
	p := mem.OffsetType[T](a.data, int64(index))
	*p = value
}

func (a *ArrayList[T]) Get(index int) T {
	if index >= a.len {
		var t T
		return t
	}
	p := mem.OffsetType[T](a.data, int64(index))
	return *p
}

func (a *ArrayList[T]) Pop() T {
	if a.len == 0 {
		var t T
		return t
	}
	p := mem.OffsetType[T](a.data, int64(a.len-1))
	a.len--
	return *p
}

func (a *ArrayList[T]) For(fn func(index int, value T) (next bool)) {
	for index := 0; index < a.len; index++ {
		value := a.Get(index)
		next := fn(index, value)
		if !next {
			return
		}
	}
}

func (a *ArrayList[T]) Free() {
	mem.FreeType[T](a.heap, a.data)
	mem.FreeType[ArrayList[T]](a.heap, a)
}

func (a *ArrayList[T]) MarshalJSON() ([]byte, error) {
	aa := make([]T, a.Len())
	a.For(func(index int, value T) (next bool) {
		aa[index] = value
		return true
	})
	data, err := json.Marshal(aa)
	return data, err
}

func (a *ArrayList[T]) UnmarshalJSON(data []byte) error {
	aa := make([]T, 0, initCap)
	err := json.Unmarshal(data, &aa)
	if err != nil {
		return err
	}
	for _, v := range aa {
		ok := a.Add(v)
		if !ok {
			return errors.New("overflow")
		}
	}
	return nil
}
