package kcp

import (
	"hash/crc32"

	"github.com/flswld/halo/hashcode"
)

// byteCheckModeSupported 判断 Go 实现是否支持指定校验模式
func byteCheckModeSupported(mode int) bool {
	return mode == ByteCheckModeZero || mode == ByteCheckModeCRC32 || mode == ByteCheckModeXXH3
}

// byte_check_hash 根据当前模式计算 KCP 载荷校验值
func byte_check_hash(data []byte) uint32 {
	switch byteCheckMode {
	case ByteCheckModeZero:
		return 0
	case ByteCheckModeCRC32:
		return crc32.ChecksumIEEE(data)
	case ByteCheckModeXXH3:
		return uint32(hashcode.GetHashCodeXXH3(data))
	default:
		return 0
	}
}
