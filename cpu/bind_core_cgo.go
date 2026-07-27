//go:build cgo
// +build cgo

package cpu

import (
	"runtime"
)

// #include "../cgo/cpu.h"
import "C"

// BindCpuCore 将当前操作系统线程绑定到指定 CPU 核心
func BindCpuCore(core int) bool {
	runtime.LockOSThread()
	var core_list [64]C.int
	core_list[0] = C.int(core)
	ret := C.bind_cpu_core((*C.int)(&core_list[0]), 1)
	if ret != 0 {
		return false
	}
	return true
}

// UnbindCpuCore 解除当前操作系统线程的 CPU 核心绑定
func UnbindCpuCore() bool {
	var core_list [64]C.int
	num_cpu := runtime.NumCPU()
	for core := 0; core < num_cpu; core++ {
		core_list[core] = C.int(core)
	}
	ret := C.bind_cpu_core((*C.int)(&core_list[0]), C.int(num_cpu))
	if ret != 0 {
		return false
	}
	runtime.UnlockOSThread()
	return true
}
