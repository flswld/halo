package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/protocol"
)

func (i *NetIf) ConvMacAddrToUint64(macAddr []byte) (macAddrUint64 uint64) {
	macAddrUint64 = uint64(0)
	macAddrUint64 += uint64(macAddr[0]) << 40
	macAddrUint64 += uint64(macAddr[1]) << 32
	macAddrUint64 += uint64(macAddr[2]) << 24
	macAddrUint64 += uint64(macAddr[3]) << 16
	macAddrUint64 += uint64(macAddr[4]) << 8
	macAddrUint64 += uint64(macAddr[5]) << 0
	return macAddrUint64
}

func (i *NetIf) ConvUint64ToMacAddr(macAddrUint64 uint64) (macAddr []byte) {
	macAddr = make([]byte, 6)
	macAddr[0] = uint8(macAddrUint64 >> 40)
	macAddr[1] = uint8(macAddrUint64 >> 32)
	macAddr[2] = uint8(macAddrUint64 >> 24)
	macAddr[3] = uint8(macAddrUint64 >> 16)
	macAddr[4] = uint8(macAddrUint64 >> 8)
	macAddr[5] = uint8(macAddrUint64 >> 0)
	return macAddr
}

func (i *NetIf) GetArpCache(ipAddr []byte) (macAddr []byte) {
	ipAddrUint32 := protocol.ConvIpAddrToUint32(ipAddr)
	i.ArpCacheTableLock.RLock()
	macAddrUint64, exist := i.ArpCacheTable[ipAddrUint32]
	i.ArpCacheTableLock.RUnlock()
	if !exist {
		// 不存在则发起ARP询问并返回空
		arpPkt, err := protocol.BuildArpPkt(protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, BROADCAST_MAC_ADDR, ipAddr)
		if err != nil {
			fmt.Printf("build arp packet error: %v\n", err)
			return
		}
		ethFrm, err := protocol.BuildEthFrm(arpPkt, BROADCAST_MAC_ADDR, i.MacAddr, protocol.ETH_PROTO_ARP)
		if err != nil {
			fmt.Printf("build ethernet frame error: %v\n", err)
			return
		}
		if i.Engine.DebugLog {
			fmt.Printf("tx arp pkt, eth frm len: %v, eth frm data: %v\n", len(ethFrm), ethFrm)
		}
		i.EthTxChan <- ethFrm
		return nil
	}
	macAddr = i.ConvUint64ToMacAddr(macAddrUint64)
	return macAddr
}

func (i *NetIf) SetArpCache(ipAddr []byte, macAddr []byte) {
	ipAddrUint32 := protocol.ConvIpAddrToUint32(ipAddr)
	macAddrUint64 := i.ConvMacAddrToUint64(macAddr)
	i.ArpCacheTableLock.Lock()
	i.ArpCacheTable[ipAddrUint32] = macAddrUint64
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
	i.SetArpCache(arpSrcAddr, arpSrcMac)
	// 对目的IP为本机的ARP询问请求进行回应
	if arpOption == protocol.ARP_REQUEST && bytes.Equal(arpDstAddr, i.IpAddr) {
		arpPkt, err := protocol.BuildArpPkt(protocol.ARP_REPLY, i.MacAddr, i.IpAddr, arpSrcMac, arpSrcAddr)
		if err != nil {
			fmt.Printf("build arp packet error: %v\n", err)
			return
		}
		ethFrm, err := protocol.BuildEthFrm(arpPkt, arpSrcMac, i.MacAddr, protocol.ETH_PROTO_ARP)
		if err != nil {
			fmt.Printf("build ethernet frame error: %v\n", err)
			return
		}
		if i.Engine.DebugLog {
			fmt.Printf("tx arp pkt, eth frm len: %v, eth frm data: %v\n", len(ethFrm), ethFrm)
		}
		i.EthTxChan <- ethFrm
	}
}
