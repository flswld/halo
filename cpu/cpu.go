package cpu

import (
	"runtime"
	"sync/atomic"
	_ "unsafe"
)

// #include "../cgo/cpu.h"
import "C"

func BindCpuCore(core int) bool {
	var core_list [64]C.int
	core_list[0] = C.int(core)
	ret := C.bind_cpu_core((*C.int)(&core_list[0]), 1)
	if ret == 0 {
		runtime.LockOSThread()
		return true
	}
	return false
}

func UnbindCpuCore() bool {
	var core_list [64]C.int
	num_cpu := runtime.NumCPU()
	for i := 0; i < num_cpu; i++ {
		core_list[i] = C.int(i)
	}
	ret := C.bind_cpu_core((*C.int)(&core_list[0]), C.int(num_cpu))
	if ret == 0 {
		runtime.UnlockOSThread()
		return true
	}
	return false
}

type SpinLock uint32

//go:linkname procyield runtime.procyield
func procyield(cycles uint32)

func (l *SpinLock) Lock() {
	for {
		ok := atomic.CompareAndSwapUint32((*uint32)(l), 0, 1)
		if ok {
			break
		}
		procyield(10)
	}
}

func (l *SpinLock) UnLock() {
	atomic.StoreUint32((*uint32)(l), 0)
}
