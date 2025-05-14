//go:build xxhash
// +build xxhash

package kcp

import (
	"unsafe"
)

// #define XXH_STATIC_LINKING_ONLY
// #define XXH_IMPLEMENTATION
// #include "../../cgo/xxhash.h"
import "C"

func byte_check_hash(data []byte) uint32 {
	switch byteCheckMode {
	case 0:
		return 0
	case 1:
		return 0
	case 2:
		h := C.XXH3_64bits(unsafe.Pointer(&data[0]), C.size_t(len(data)))
		return uint32(h)
	default:
		return 0
	}
}
