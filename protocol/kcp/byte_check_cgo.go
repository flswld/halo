//go:build cgo
// +build cgo

package kcp

import (
	"unsafe"
)

// #define XXH_STATIC_LINKING_ONLY
// #define XXH_IMPLEMENTATION
// #include "../../cgo/xxhash.h"
import "C"

// byte_check_hash 按 Halo 配置计算 KCP 载荷校验值
func byte_check_hash(data []byte) uint32 {
	switch byteCheckMode {
	case 0:
		return 0
	case 1:
		return 0
	case 2:
		// 模式 2 使用 xxHash3 的低 32 位并写入 Halo 扩展头部
		h := C.XXH3_64bits(unsafe.Pointer(&data[0]), C.size_t(len(data)))
		return uint32(h)
	default:
		return 0
	}
}
