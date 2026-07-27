package list

import (
	"encoding/json"
	"errors"

	"github.com/flswld/halo/mem"
)

const (
	initCap = 8
)

// ArrayList 提供使用自定义分配器的泛型动态数组
type ArrayList[T any] struct {
	data      *T            // 元素存储区首地址
	len       int           // 元素数量
	cap       int           // 存储容量
	allocator mem.Allocator // 内存分配器
}

// NewArrayList 使用默认容量创建动态数组
func NewArrayList[T any](allocator mem.Allocator) *ArrayList[T] {
	return NewArrayListWithCap[T](allocator, initCap)
}

// NewArrayListWithCap 使用指定初始容量创建动态数组
func NewArrayListWithCap[T any](allocator mem.Allocator, cap int) *ArrayList[T] {
	if cap < initCap {
		cap = initCap
	}
	a := mem.MallocType[ArrayList[T]](allocator, 1)
	if a == nil {
		return nil
	}
	a.data = mem.MallocType[T](allocator, uint64(cap))
	if a.data == nil {
		mem.FreeType[ArrayList[T]](allocator, a)
		return nil
	}
	a.len = 0
	a.cap = cap
	a.allocator = allocator
	return a
}

// Len 返回元素数量
func (a *ArrayList[T]) Len() int {
	return a.len
}

// Add 向数组尾部追加元素
func (a *ArrayList[T]) Add(value T) bool {
	if a.len >= a.cap {
		// 容量不足时先分配两倍空间 完整复制后再替换原存储区
		data := mem.MallocType[T](a.allocator, uint64(a.cap*2))
		if data == nil {
			return false
		}
		mem.MemCpyType[T](data, a.data, uint64(a.cap))
		mem.FreeType[T](a.allocator, a.data)
		a.data = data
		a.cap *= 2
	}
	p := mem.OffsetType[T](a.data, int64(a.len))
	*p = value
	a.len++
	return true
}

// Set 更新指定索引的元素
func (a *ArrayList[T]) Set(index int, value T) {
	if index >= a.len {
		return
	}
	p := mem.OffsetType[T](a.data, int64(index))
	*p = value
}

// Get 返回指定索引的元素
func (a *ArrayList[T]) Get(index int) T {
	if index >= a.len {
		var t T
		return t
	}
	p := mem.OffsetType[T](a.data, int64(index))
	return *p
}

// Del 删除指定索引的元素
func (a *ArrayList[T]) Del(index int) {
	if index >= a.len {
		return
	}
	// memmove 支持区域重叠 将后续元素整体左移一位
	mem.MemCpyType[T](mem.OffsetType[T](a.data, int64(index)), mem.OffsetType[T](a.data, int64(index+1)), uint64(a.len-index-1))
	a.len--
}

// For 按索引顺序遍历元素
func (a *ArrayList[T]) For(fn func(index int, value T) (next bool)) {
	for index := 0; index < a.len; index++ {
		value := a.Get(index)
		next := fn(index, value)
		if !next {
			return
		}
	}
}

// Free 释放动态数组占用的全部内存
func (a *ArrayList[T]) Free() {
	mem.FreeType[T](a.allocator, a.data)
	mem.FreeType[ArrayList[T]](a.allocator, a)
}

// MarshalJSON 将动态数组编码为 JSON
func (a *ArrayList[T]) MarshalJSON() ([]byte, error) {
	aa := make([]T, a.Len())
	a.For(func(index int, value T) (next bool) {
		aa[index] = value
		return true
	})
	data, err := json.Marshal(aa)
	return data, err
}

// UnmarshalJSON 从 JSON 解码并追加元素
func (a *ArrayList[T]) UnmarshalJSON(data []byte) error {
	aa := make([]T, 0, initCap)
	err := json.Unmarshal(data, &aa)
	if err != nil {
		return err
	}
	// 复用 Add 的扩容和分配失败处理 保留当前数组已有内容
	for _, v := range aa {
		ok := a.Add(v)
		if !ok {
			return errors.New("overflow")
		}
	}
	return nil
}
