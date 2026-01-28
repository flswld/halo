package cpu

import (
	"sync/atomic"
	_ "unsafe"
)

type SpinLock uint32

//go:linkname procyield runtime.procyield
func procyield(cycles uint32)

func ProcYield(cycles uint32) {
	procyield(cycles)
}

func (l *SpinLock) Lock() {
	for {
		ok := atomic.CompareAndSwapUint32((*uint32)(l), 0, 1)
		if ok {
			break
		}
		procyield(10)
	}
}

func (l *SpinLock) Unlock() {
	atomic.StoreUint32((*uint32)(l), 0)
}
