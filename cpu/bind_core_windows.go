//go:build !cgo && windows
// +build !cgo,windows

package cpu

import (
	"errors"
	"runtime"

	"golang.org/x/sys/windows"
)

func BindCpuCore(core int) bool {
	runtime.LockOSThread()
	var mask uintptr
	mask |= 1 << core
	thread := windows.CurrentThread()
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadAffinityMask := modkernel32.NewProc("SetThreadAffinityMask")
	_, _, err := procSetThreadAffinityMask.Call(uintptr(thread), mask)
	if !errors.Is(err, windows.NOERROR) {
		return false
	}
	return true
}

func UnbindCpuCore() bool {
	var mask uintptr
	num_cpu := runtime.NumCPU()
	for core := 0; core < num_cpu; core++ {
		mask |= 1 << core
	}
	thread := windows.CurrentThread()
	modkernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadAffinityMask := modkernel32.NewProc("SetThreadAffinityMask")
	_, _, err := procSetThreadAffinityMask.Call(uintptr(thread), mask)
	if !errors.Is(err, windows.NOERROR) {
		return false
	}
	runtime.UnlockOSThread()
	return true
}
