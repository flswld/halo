package protocol

import (
	"strconv"
	"strings"
)

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

func IpAddrToU(ipAddr []byte) uint32 {
	ipAddrU := uint32(0)
	ipAddrU += uint32(ipAddr[0]) << 24
	ipAddrU += uint32(ipAddr[1]) << 16
	ipAddrU += uint32(ipAddr[2]) << 8
	ipAddrU += uint32(ipAddr[3]) << 0
	return ipAddrU
}

func UToIpAddr(ipAddrU uint32) []byte {
	ipAddr := make([]byte, 4)
	ipAddr[0] = uint8(ipAddrU >> 24)
	ipAddr[1] = uint8(ipAddrU >> 16)
	ipAddr[2] = uint8(ipAddrU >> 8)
	ipAddr[3] = uint8(ipAddrU >> 0)
	return ipAddr
}

func MacAddrToU(macAddr []byte) uint64 {
	macAddrU := uint64(0)
	macAddrU += uint64(macAddr[0]) << 40
	macAddrU += uint64(macAddr[1]) << 32
	macAddrU += uint64(macAddr[2]) << 24
	macAddrU += uint64(macAddr[3]) << 16
	macAddrU += uint64(macAddr[4]) << 8
	macAddrU += uint64(macAddr[5]) << 0
	return macAddrU
}

func UToMacAddr(macAddrU uint64) []byte {
	macAddr := make([]byte, 6)
	macAddr[0] = uint8(macAddrU >> 40)
	macAddr[1] = uint8(macAddrU >> 32)
	macAddr[2] = uint8(macAddrU >> 24)
	macAddr[3] = uint8(macAddrU >> 16)
	macAddr[4] = uint8(macAddrU >> 8)
	macAddr[5] = uint8(macAddrU >> 0)
	return macAddr
}

func ParseMacAddr(macAddrStr string) ([]byte, error) {
	macAddrSplit := strings.Split(macAddrStr, ":")
	macAddr := make([]byte, 6)
	for i := 0; i < 6; i++ {
		split, err := strconv.ParseUint(macAddrSplit[i], 16, 8)
		if err != nil {
			return nil, err
		}
		macAddr[i] = uint8(split)
	}
	return macAddr, nil
}

func ParseIpAddr(ipAddrStr string) ([]byte, error) {
	ipAddrSplit := strings.Split(ipAddrStr, ".")
	ipAddr := make([]byte, 4)
	for i := 0; i < 4; i++ {
		split, err := strconv.Atoi(ipAddrSplit[i])
		if err != nil {
			return nil, err
		}
		ipAddr[i] = uint8(split)
	}
	return ipAddr, nil
}
