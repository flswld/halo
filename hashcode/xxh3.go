// 当前实现移植自 github.com/zeebo/xxh3 v1.1.0 并裁剪了流式、seed、128 位和 SIMD 路径
// 算法与原始代码按 BSD-2-Clause License 使用 许可证文本见同目录 LICENSE
package hashcode

import (
	"encoding/binary"
	"math/bits"
	"unsafe"
)

const (
	stripeSize = 64
	blockSize  = 1024

	prime32One   = uint64(2654435761)
	prime32Two   = uint64(2246822519)
	prime32Three = uint64(3266489917)

	prime64One   = uint64(11400714785074694791)
	prime64Two   = uint64(14029467366897019727)
	prime64Three = uint64(1609587929392839161)
	prime64Four  = uint64(9650029242287828579)
	prime64Five  = uint64(2870177450012600261)
)

// defaultSecret 是 XXH3 规范定义的 192 字节默认密钥
var defaultSecret = [...]byte{
	0xb8, 0xfe, 0x6c, 0x39, 0x23, 0xa4, 0x4b, 0xbe, 0x7c, 0x01, 0x81, 0x2c, 0xf7, 0x21, 0xad, 0x1c,
	0xde, 0xd4, 0x6d, 0xe9, 0x83, 0x90, 0x97, 0xdb, 0x72, 0x40, 0xa4, 0xa4, 0xb7, 0xb3, 0x67, 0x1f,
	0xcb, 0x79, 0xe6, 0x4e, 0xcc, 0xc0, 0xe5, 0x78, 0x82, 0x5a, 0xd0, 0x7d, 0xcc, 0xff, 0x72, 0x21,
	0xb8, 0x08, 0x46, 0x74, 0xf7, 0x43, 0x24, 0x8e, 0xe0, 0x35, 0x90, 0xe6, 0x81, 0x3a, 0x26, 0x4c,
	0x3c, 0x28, 0x52, 0xbb, 0x91, 0xc3, 0x00, 0xcb, 0x88, 0xd0, 0x65, 0x8b, 0x1b, 0x53, 0x2e, 0xa3,
	0x71, 0x64, 0x48, 0x97, 0xa2, 0x0d, 0xf9, 0x4e, 0x38, 0x19, 0xef, 0x46, 0xa9, 0xde, 0xac, 0xd8,
	0xa8, 0xfa, 0x76, 0x3f, 0xe3, 0x9c, 0x34, 0x3f, 0xf9, 0xdc, 0xbb, 0xc7, 0xc7, 0x0b, 0x4f, 0x1d,
	0x8a, 0x51, 0xe0, 0x4b, 0xcd, 0xb4, 0x59, 0x31, 0xc8, 0x9f, 0x7e, 0xc9, 0xd9, 0x78, 0x73, 0x64,
	0xea, 0xc5, 0xac, 0x83, 0x34, 0xd3, 0xeb, 0xc3, 0xc5, 0x81, 0xa0, 0xff, 0xfa, 0x13, 0x63, 0xeb,
	0x17, 0x0d, 0xdd, 0x51, 0xb7, 0xf0, 0xda, 0x49, 0xd3, 0x16, 0x55, 0x26, 0x29, 0xd4, 0x68, 0x9e,
	0x2b, 0x16, 0xbe, 0x58, 0x7d, 0x47, 0xa1, 0xfc, 0x8f, 0xf8, 0xb8, 0xd1, 0x7a, 0xd0, 0x31, 0xce,
	0x45, 0xcb, 0x3a, 0x8f, 0x95, 0x16, 0x04, 0x28, 0xaf, 0xd7, 0xfb, 0xca, 0xbb, 0x4b, 0x40, 0x7e,
}

// xxh3HashCode 计算字节切片的 XXH3 64 位哈希码
func xxh3HashCode(data []byte) uint64 {
	length := len(data)
	switch {
	case length <= 16:
		return hashSmall(data)
	case length <= 128:
		return hashMedium(data)
	case length <= 240:
		return hashLarge(data)
	default:
		return hashLong(data)
	}
}

// hashSmall 处理不超过 16 字节的输入
func hashSmall(data []byte) uint64 {
	length := len(data)
	var accumulator uint64
	switch {
	case length > 8:
		inputLow := read64(data, 0) ^ (secret64(24) ^ secret64(32))
		inputHigh := read64(data, length-8) ^ (secret64(40) ^ secret64(48))
		folded := multiplyFold64(inputLow, inputHigh)
		return avalanche(uint64(length) + bits.ReverseBytes64(inputLow) + inputHigh + folded)
	case length > 3:
		inputOne := read32(data, 0)
		inputTwo := read32(data, length-4)
		input := uint64(inputTwo) + uint64(inputOne)<<32
		return rrmxmx(input^(secret64(8)^secret64(16)), uint64(length))
	case length == 3:
		firstTwo := uint64(read16(data, 0))
		third := uint64(data[2])
		accumulator = firstTwo<<16 + third + 3<<8
	case length == 2:
		firstTwo := uint64(read16(data, 0))
		accumulator = firstTwo*(1<<24+1)>>8 + 2<<8
	case length == 1:
		first := uint64(data[0])
		accumulator = first*(1<<24+1<<16+1) + 1<<8
	default:
		return 0x2d06800538d394c2
	}

	accumulator ^= uint64(secret32(0) ^ secret32(4))
	return avalancheSmall(accumulator)
}

// hashMedium 处理 17 到 128 字节的输入
func hashMedium(data []byte) uint64 {
	length := len(data)
	accumulator := uint64(length) * prime64One
	if length > 32 {
		if length > 64 {
			if length > 96 {
				accumulator += mix16(data, 48, 96)
				accumulator += mix16(data, length-64, 112)
			}
			accumulator += mix16(data, 32, 64)
			accumulator += mix16(data, length-48, 80)
		}
		accumulator += mix16(data, 16, 32)
		accumulator += mix16(data, length-32, 48)
	}
	accumulator += mix16(data, 0, 0)
	accumulator += mix16(data, length-16, 16)
	return avalanche(accumulator)
}

// hashLarge 处理 129 到 240 字节的输入
func hashLarge(data []byte) uint64 {
	length := len(data)
	accumulator := uint64(length) * prime64One
	for offset := 0; offset < 128; offset += 16 {
		accumulator += mix16(data, offset, offset)
	}
	accumulator = avalanche(accumulator)

	for offset, top := 128, length&^15; offset < top; offset += 16 {
		accumulator += mix16(data, offset, offset-125)
	}
	accumulator += mix16(data, length-16, 119)
	return avalanche(accumulator)
}

// hashLong 处理超过 240 字节的输入
func hashLong(data []byte) uint64 {
	accumulators := [8]uint64{
		prime32Three, prime64One, prime64Two, prime64Three,
		prime64Four, prime32Two, prime64Five, prime32One,
	}
	accumulateLong(&accumulators, data)

	result := uint64(len(data)) * prime64One
	result += multiplyFold64(accumulators[0]^secret64(11), accumulators[1]^secret64(19))
	result += multiplyFold64(accumulators[2]^secret64(27), accumulators[3]^secret64(35))
	result += multiplyFold64(accumulators[4]^secret64(43), accumulators[5]^secret64(51))
	result += multiplyFold64(accumulators[6]^secret64(59), accumulators[7]^secret64(67))
	return avalanche(result)
}

// accumulateLong 按 1024 字节块和 64 字节条带累加长输入
func accumulateLong(accumulators *[8]uint64, data []byte) {
	dataPointer := unsafe.Pointer(unsafe.SliceData(data))
	secretPointer := unsafe.Pointer(&defaultSecret[0])
	remaining := len(data)
	for remaining > blockSize {
		blockSecret := secretPointer
		for stripe := 0; stripe < blockSize/stripeSize; stripe++ {
			accumulateStripe(accumulators, dataPointer, blockSecret)
			dataPointer = unsafe.Add(dataPointer, stripeSize)
			blockSecret = unsafe.Add(blockSecret, 8)
			remaining -= stripeSize
		}
		scramble(accumulators)
	}

	if remaining == 0 {
		return
	}
	stripes := (remaining - 1) / stripeSize
	blockSecret := secretPointer
	for stripe := 0; stripe < stripes; stripe++ {
		accumulateStripe(accumulators, dataPointer, blockSecret)
		dataPointer = unsafe.Add(dataPointer, stripeSize)
		blockSecret = unsafe.Add(blockSecret, 8)
		remaining -= stripeSize
	}
	if remaining > 0 {
		lastStripe := unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), len(data)-stripeSize)
		lastSecret := unsafe.Add(secretPointer, 121)
		accumulateStripe(accumulators, lastStripe, lastSecret)
	}
}

// accumulateStripe 混合一个 64 字节输入条带
func accumulateStripe(accumulators *[8]uint64, dataPointer, secretPointer unsafe.Pointer) {
	inputZero := readPointer64(dataPointer, 0)
	keyedZero := inputZero ^ readPointer64(secretPointer, 0)
	inputOne := readPointer64(dataPointer, 8)
	keyedOne := inputOne ^ readPointer64(secretPointer, 8)
	accumulators[0] += uint64(uint32(keyedZero))*(keyedZero>>32) + inputOne
	accumulators[1] += inputZero + uint64(uint32(keyedOne))*(keyedOne>>32)

	inputTwo := readPointer64(dataPointer, 16)
	keyedTwo := inputTwo ^ readPointer64(secretPointer, 16)
	inputThree := readPointer64(dataPointer, 24)
	keyedThree := inputThree ^ readPointer64(secretPointer, 24)
	accumulators[2] += uint64(uint32(keyedTwo))*(keyedTwo>>32) + inputThree
	accumulators[3] += inputTwo + uint64(uint32(keyedThree))*(keyedThree>>32)

	inputFour := readPointer64(dataPointer, 32)
	keyedFour := inputFour ^ readPointer64(secretPointer, 32)
	inputFive := readPointer64(dataPointer, 40)
	keyedFive := inputFive ^ readPointer64(secretPointer, 40)
	accumulators[4] += uint64(uint32(keyedFour))*(keyedFour>>32) + inputFive
	accumulators[5] += inputFour + uint64(uint32(keyedFive))*(keyedFive>>32)

	inputSix := readPointer64(dataPointer, 48)
	keyedSix := inputSix ^ readPointer64(secretPointer, 48)
	inputSeven := readPointer64(dataPointer, 56)
	keyedSeven := inputSeven ^ readPointer64(secretPointer, 56)
	accumulators[6] += uint64(uint32(keyedSix))*(keyedSix>>32) + inputSeven
	accumulators[7] += inputSix + uint64(uint32(keyedSeven))*(keyedSeven>>32)
}

// scramble 打散一个完整块累积后的状态
func scramble(accumulators *[8]uint64) {
	for lane := range accumulators {
		accumulators[lane] ^= accumulators[lane] >> 47
		accumulators[lane] ^= secret64(128 + lane*8)
		accumulators[lane] *= prime32One
	}
}

// mix16 使用默认密钥混合 16 字节输入
func mix16(data []byte, dataOffset, secretOffset int) uint64 {
	inputLow := read64(data, dataOffset) ^ secret64(secretOffset)
	inputHigh := read64(data, dataOffset+8) ^ secret64(secretOffset+8)
	return multiplyFold64(inputLow, inputHigh)
}

// avalancheSmall 完成短输入的雪崩混合
func avalancheSmall(value uint64) uint64 {
	value ^= value >> 33
	value *= prime64Two
	value ^= value >> 29
	value *= prime64Three
	value ^= value >> 32
	return value
}

// avalanche 完成 XXH3 的雪崩混合
func avalanche(value uint64) uint64 {
	value ^= value >> 37
	value *= 0x165667919e3779f9
	value ^= value >> 32
	return value
}

// rrmxmx 完成 4 到 8 字节输入的旋转乘法混合
func rrmxmx(value, length uint64) uint64 {
	value ^= bits.RotateLeft64(value, 49) ^ bits.RotateLeft64(value, 24)
	value *= 0x9fb21c651e98df25
	value ^= value>>35 + length
	value *= 0x9fb21c651e98df25
	value ^= value >> 28
	return value
}

// multiplyFold64 折叠 64 位乘法的高低部分
func multiplyFold64(left, right uint64) uint64 {
	high, low := bits.Mul64(left, right)
	return high ^ low
}

// read16 按小端序读取 16 位整数
func read16(data []byte, offset int) uint16 {
	return binary.LittleEndian.Uint16(data[offset:])
}

// read32 按小端序读取 32 位整数
func read32(data []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(data[offset:])
}

// read64 按小端序读取 64 位整数
func read64(data []byte, offset int) uint64 {
	return binary.LittleEndian.Uint64(data[offset:])
}

// secret32 按小端序读取默认密钥的 32 位整数
func secret32(offset int) uint32 {
	return binary.LittleEndian.Uint32(defaultSecret[offset:])
}

// secret64 按小端序读取默认密钥的 64 位整数
func secret64(offset int) uint64 {
	return binary.LittleEndian.Uint64(defaultSecret[offset:])
}

// readPointer64 在长输入内部已验证的条带范围内读取 64 位整数
func readPointer64(pointer unsafe.Pointer, offset uintptr) uint64 {
	value := (*[8]byte)(unsafe.Add(pointer, offset))
	return binary.LittleEndian.Uint64(value[:])
}
