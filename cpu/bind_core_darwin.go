//go:build !cgo && darwin
// +build !cgo,darwin

package cpu

func BindCpuCore(core int) bool {
	return false
}

func UnbindCpuCore() bool {
	return false
}
