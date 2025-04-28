//go:build !xxhash
// +build !xxhash

package kcp

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
