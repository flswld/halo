package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) MacAddrToU(macAddr []byte) (macAddrU uint64) {
	macAddrU = uint64(0)
	macAddrU += uint64(macAddr[0]) << 40
	macAddrU += uint64(macAddr[1]) << 32
	macAddrU += uint64(macAddr[2]) << 24
	macAddrU += uint64(macAddr[3]) << 16
	macAddrU += uint64(macAddr[4]) << 8
	macAddrU += uint64(macAddr[5]) << 0
	return macAddrU
}

func (i *NetIf) UToMacAddr(macAddrU uint64) (macAddr []byte) {
	macAddr = make([]byte, 6)
	macAddr[0] = uint8(macAddrU >> 40)
	macAddr[1] = uint8(macAddrU >> 32)
	macAddr[2] = uint8(macAddrU >> 24)
	macAddr[3] = uint8(macAddrU >> 16)
	macAddr[4] = uint8(macAddrU >> 8)
	macAddr[5] = uint8(macAddrU >> 0)
	return macAddr
}

func (i *NetIf) SendFreeArp() {
	arpPkt, err := protocol.BuildArpPkt(protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, BROADCAST_MAC_ADDR, i.IpAddr)
	if err != nil {
		fmt.Printf("build arp packet error: %v\n", err)
		return
	}
	i.TxEthernet(arpPkt, BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
}

func (i *NetIf) GetArpCache(ipAddr []byte) (macAddr []byte) {
	if bytes.Equal(ipAddr, i.IpAddr) {
		return nil
	}
	ipAddrU := protocol.IpAddrToU(ipAddr)
	i.ArpCacheTableLock.RLock()
	macAddrU, exist := i.ArpCacheTable[ipAddrU]
	i.ArpCacheTableLock.RUnlock()
	if !exist {
		// 不存在则发起ARP询问并返回空
		arpPkt, err := protocol.BuildArpPkt(protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, BROADCAST_MAC_ADDR, ipAddr)
		if err != nil {
			fmt.Printf("build arp packet error: %v\n", err)
			return nil
		}
		i.TxEthernet(arpPkt, BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
		return nil
	}
	macAddr = i.UToMacAddr(macAddrU)
	return macAddr
}

func (i *NetIf) SetArpCache(ipAddr []byte, macAddr []byte) {
	ipAddrU := protocol.IpAddrToU(ipAddr)
	macAddrU := i.MacAddrToU(macAddr)
	i.ArpCacheTableLock.Lock()
	i.ArpCacheTable[ipAddrU] = macAddrU
	i.ArpCacheTableLock.Unlock()
}

func (i *NetIf) HandleArp(ethPayload []byte, ethSrcMac []byte) {
	arpOption, arpSrcMac, arpSrcAddr, _, arpDstAddr, err := protocol.ParseArpPkt(ethPayload)
	if err != nil {
		fmt.Printf("parse arp packet error: %v\n", err)
		return
	}
	if !bytes.Equal(arpSrcMac, ethSrcMac) {
		fmt.Printf("arp packet src mac addr not match\n")
		return
	}
	if bytes.Equal(arpSrcAddr, i.IpAddr) {
		fmt.Printf("arp find ip addr conflect\n")
		return
	}
	i.SetArpCache(arpSrcAddr, arpSrcMac)
	// 对目的IP为本机的ARP询问请求进行回应
	if arpOption == protocol.ARP_REQUEST && bytes.Equal(arpDstAddr, i.IpAddr) {
		arpPkt, err := protocol.BuildArpPkt(protocol.ARP_REPLY, i.MacAddr, i.IpAddr, arpSrcMac, arpSrcAddr)
		if err != nil {
			fmt.Printf("build arp packet error: %v\n", err)
			return
		}
		i.TxEthernet(arpPkt, arpSrcMac, protocol.ETH_PROTO_ARP)
	}
}
