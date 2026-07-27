//go:build !cgo
// +build !cgo

package kcp

// byte_check_hash 在无 CGO 模式下为 Halo 数据校验扩展提供零值回退
func byte_check_hash(data []byte) uint32 {
	switch byteCheckMode {
	case 0:
		return 0
	case 1:
		return 0
	case 2:
		return 0
	default:
		return 0
	}
}
