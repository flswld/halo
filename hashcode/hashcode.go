// Package hashcode 提供整数和字节序列使用的哈希码函数
package hashcode

// integerHashPrime 是整数哈希码使用的 64 位黄金比例奇数乘数
const integerHashPrime = uint64(0x9e3779b97f4a7c15)

// GetHashCodeInt 折叠整数高位并保留连续小整数在二次幂桶中的均匀分布
func GetHashCodeInt(value uint64) uint64 {
	value ^= value >> 32
	value ^= value >> 16
	return value * integerHashPrime
}

// GetHashCodeXXH3 使用 XXH3 算法计算字节序列的 64 位哈希码
func GetHashCodeXXH3(data []byte) uint64 {
	return xxh3HashCode(data)
}

// GetHashCodeBKDR 使用 seed 131 的 BKDR 算法计算字节序列的 64 位哈希码
func GetHashCodeBKDR(data []byte) uint64 {
	var hashCode uint64
	for _, value := range data {
		hashCode = uint64(value) + 131*hashCode
	}
	return hashCode
}
