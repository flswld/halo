//go:build !cgo && linux
// +build !cgo,linux

package cpu

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	CPU_SETSIZE = 1024
	__NCPUBITS  = 64
)

func BindCpuCore(core int) bool {
	runtime.LockOSThread()
	var mask [CPU_SETSIZE / __NCPUBITS]uint64
	mask[core/__NCPUBITS] |= 1 << (uint(core % __NCPUBITS))
	_, _, err := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0])))
	if err != 0 {
		return false
	}
	return true
}

func UnbindCpuCore() bool {
	var mask [CPU_SETSIZE / __NCPUBITS]uint64
	num_cpu := runtime.NumCPU()
	for core := 0; core < num_cpu; core++ {
		mask[core/__NCPUBITS] |= 1 << (uint(core % __NCPUBITS))
	}
	_, _, err := syscall.RawSyscall(syscall.SYS_SCHED_SETAFFINITY, 0, uintptr(len(mask)*8), uintptr(unsafe.Pointer(&mask[0])))
	if err != 0 {
		return false
	}
	runtime.UnlockOSThread()
	return true
}
