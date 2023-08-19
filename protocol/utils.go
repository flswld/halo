package protocol

func GetCheckSum(data []byte) []byte {
	sum := uint32(0)
	length := len(data)
	index := 0
	// 以每16位为单位进行求和 直到所有的字节全部求完或者只剩下一个8位字节 如果剩余一个8位字节说明字节数为奇数个
	for length > 1 {
		sum += uint32(data[index])<<8 + uint32(data[index+1])
		index += 2
		length -= 2
	}
	// 如果字节数为奇数个 要加上最后剩下的那个8位字节
	if length > 0 {
		sum += uint32(data[index])
	}
	// 加上高16位进位的部分
	sum += sum >> 16
	// 求反
	sum16 := uint16(^sum)
	return []byte{
		byte(sum16 >> 8),
		byte(sum16),
	}
}

func ConvIpAddrToUint32(ipAddr []byte) (ipAddrUint32 uint32) {
	ipAddrUint32 = uint32(0)
	ipAddrUint32 += uint32(ipAddr[0]) << 24
	ipAddrUint32 += uint32(ipAddr[1]) << 16
	ipAddrUint32 += uint32(ipAddr[2]) << 8
	ipAddrUint32 += uint32(ipAddr[3]) << 0
	return ipAddrUint32
}
