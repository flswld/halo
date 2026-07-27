//go:build !cgo && darwin
// +build !cgo,darwin

package cpu

// BindCpuCore 在不启用 CGO 的 Darwin 平台报告不支持 CPU 核心绑定
func BindCpuCore(core int) bool {
	return false
}

// UnbindCpuCore 在不启用 CGO 的 Darwin 平台报告不支持解除 CPU 核心绑定
func UnbindCpuCore() bool {
	return false
}
