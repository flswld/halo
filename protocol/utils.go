package protocol

import (
	"errors"
	"strconv"
	"strings"
)

var CheckSumEnable = false

// MacAddr 表示按网络字节顺序保存的六字节 MAC 地址
type MacAddr [6]byte

// Ipv4Addr 表示按网络字节顺序保存的四字节 IPv4 地址
type Ipv4Addr [4]byte

// GetCheckSum 计算网络字节序的互联网校验和
func GetCheckSum(data []byte) uint16 {
	sum := uint32(0)
	length := len(data)
	index := 0
	// 以网络字节序累加所有完整的 16 位字
	for length > 1 {
		sum += uint32(data[index])<<8 + uint32(data[index+1])
		index += 2
		length -= 2
	}
	if length > 0 {
		// 奇数字节载荷将末字节放在高八位参与计算
		sum += uint32(data[index]) << 8
	}
	// 反复折叠高 16 位直到不再产生进位
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	sum16 := uint16(^sum)
	return sum16
}

// IpAddrToU 将四字节 IPv4 地址转换为整数
func IpAddrToU(ipAddr Ipv4Addr) uint32 {
	ipAddrU := uint32(0)
	ipAddrU |= uint32(ipAddr[0]) << 24
	ipAddrU |= uint32(ipAddr[1]) << 16
	ipAddrU |= uint32(ipAddr[2]) << 8
	ipAddrU |= uint32(ipAddr[3]) << 0
	return ipAddrU
}

// UToIpAddr 将整数转换为四字节 IPv4 地址
func UToIpAddr(ipAddrU uint32) Ipv4Addr {
	var ipAddr Ipv4Addr
	ipAddr[0] = uint8(ipAddrU >> 24)
	ipAddr[1] = uint8(ipAddrU >> 16)
	ipAddr[2] = uint8(ipAddrU >> 8)
	ipAddr[3] = uint8(ipAddrU >> 0)
	return ipAddr
}

// ParseMacAddr 解析冒号分隔的 MAC 地址
func ParseMacAddr(macAddrStr string) (MacAddr, error) {
	macAddrSplit := strings.Split(macAddrStr, ":")
	if len(macAddrSplit) != 6 {
		return MacAddr{}, errors.New("mac address must have 6 components")
	}
	var macAddr MacAddr
	for i := 0; i < 6; i++ {
		split, err := strconv.ParseUint(macAddrSplit[i], 16, 8)
		if err != nil {
			return MacAddr{}, err
		}
		macAddr[i] = uint8(split)
	}
	return macAddr, nil
}

// ParseIpAddr 解析点分十进制 IPv4 地址
func ParseIpAddr(ipAddrStr string) (Ipv4Addr, error) {
	ipAddrSplit := strings.Split(ipAddrStr, ".")
	if len(ipAddrSplit) != 4 {
		return Ipv4Addr{}, errors.New("ipv4 address must have 4 components")
	}
	var ipAddr Ipv4Addr
	for i := 0; i < 4; i++ {
		split, err := strconv.ParseUint(ipAddrSplit[i], 10, 8)
		if err != nil {
			return Ipv4Addr{}, err
		}
		ipAddr[i] = uint8(split)
	}
	return ipAddr, nil
}
