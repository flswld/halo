//go:build xxhash
// +build xxhash

package kcp

// #include "./cgo/xxhash.c"
import "C"

func byte_check_hash(data []byte) uint32 {
	switch byteCheckMode {
	case 0:
		return 0
	case 1:
		return 0
	case 2:
		d := C.CBytes(data)
		h := C.XXH3_64bits(d, C.size_t(len(data)))
		C.free(d)
		return uint32(h)
	default:
		return 0
	}
}
