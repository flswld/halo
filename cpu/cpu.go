package cpu

import (
	"sync/atomic"
	_ "unsafe"
)

// SpinLock 提供基于原子操作的自旋锁
type SpinLock uint32

// procyield 让当前处理器执行指定次数的让步指令
//
//go:linkname procyield runtime.procyield
func procyield(cycles uint32)

// ProcYield 让当前处理器执行指定次数的让步指令
func ProcYield(cycles uint32) {
	procyield(cycles)
}

// Lock 获取自旋锁
func (l *SpinLock) Lock() {
	for {
		ok := atomic.CompareAndSwapUint32((*uint32)(l), 0, 1)
		if ok {
			break
		}
		procyield(10)
	}
}

// Unlock 释放自旋锁
func (l *SpinLock) Unlock() {
	atomic.StoreUint32((*uint32)(l), 0)
}
