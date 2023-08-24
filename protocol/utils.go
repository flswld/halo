package protocol

func GetCheckSum(data []byte) []byte {
	sum := uint32(0)
	length := len(data)
	index := 0
	for length > 1 {
		sum += uint32(data[index])<<8 + uint32(data[index+1])
		index += 2
		length -= 2
	}
	if length > 0 {
		sum += uint32(data[index]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	sum16 := uint16(^sum)
	return []byte{
		byte(sum16 >> 8),
		byte(sum16),
	}
}

func IpAddrToU(ipAddr []byte) (ipAddrU uint32) {
	ipAddrU = uint32(0)
	ipAddrU += uint32(ipAddr[0]) << 24
	ipAddrU += uint32(ipAddr[1]) << 16
	ipAddrU += uint32(ipAddr[2]) << 8
	ipAddrU += uint32(ipAddr[3]) << 0
	return ipAddrU
}

func UToIpAddr(ipAddrU uint32) (ipAddr []byte) {
	ipAddr = make([]byte, 4)
	ipAddr[0] = uint8(ipAddrU >> 24)
	ipAddr[1] = uint8(ipAddrU >> 16)
	ipAddr[2] = uint8(ipAddrU >> 8)
	ipAddr[3] = uint8(ipAddrU >> 0)
	return ipAddr
}
