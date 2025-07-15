package engine

import (
	"bytes"
	"fmt"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

// ArpCache ARP缓存
type ArpCache struct {
	IpAddr     uint32  // ip地址
	MacAddr    [6]byte // mac地址
	CreateTime uint32  // 创建时间
}

func (i *NetIf) SendFreeArp() {
	arpPkt := make([]byte, 0, 28)
	arpPkt, err := protocol.BuildArpPkt(arpPkt, protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, protocol.BROADCAST_MAC_ADDR, i.IpAddr)
	if err != nil {
		Log(fmt.Sprintf("build arp packet error: %v\n", err))
		return
	}
	i.TxEthernet(arpPkt, protocol.BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
}

func (i *NetIf) GetArpCache(ipAddr []byte) *ArpCache {
	if bytes.Equal(ipAddr, i.IpAddr) {
		return nil
	}
	ipAddrU := protocol.IpAddrToU(ipAddr)
	i.ArpLock.RLock()
	arpCache, exist := i.ArpCacheTable.Get(IpAddrHash(ipAddrU))
	i.ArpLock.RUnlock()
	if !exist {
		// 不存在则发起ARP询问并返回空
		arpPkt := make([]byte, 0, 28)
		arpPkt, err := protocol.BuildArpPkt(arpPkt, protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, protocol.BROADCAST_MAC_ADDR, ipAddr)
		if err != nil {
			Log(fmt.Sprintf("build arp packet error: %v\n", err))
			return nil
		}
		i.TxEthernet(arpPkt, protocol.BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
		return nil
	}
	return arpCache
}

func (i *NetIf) SetArpCache(ipAddr []byte, macAddr []byte) {
	i.ArpLock.Lock()
	defer i.ArpLock.Unlock()
	ipAddrU := protocol.IpAddrToU(ipAddr)
	arpCache, exist := i.ArpCacheTable.Get(IpAddrHash(ipAddrU))
	if !exist {
		arpCache = mem.MallocType[ArpCache](i.StaticHeap, 1)
		if arpCache == nil {
			return
		}
	}
	arpCache.IpAddr = ipAddrU
	copy(arpCache.MacAddr[:], macAddr)
	arpCache.CreateTime = i.Engine.TimeNow
	i.ArpCacheTable.Set(IpAddrHash(ipAddrU), arpCache)
}

func (i *NetIf) HandleArp(ethPayload []byte, ethSrcMac []byte) {
	arpOption, arpSrcMac, arpSrcAddr, _, arpDstAddr, err := protocol.ParseArpPkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse arp packet error: %v\n", err))
		return
	}
	if !bytes.Equal(arpSrcMac, ethSrcMac) {
		Log(fmt.Sprintf("arp packet src mac addr not match\n"))
		return
	}
	if bytes.Equal(arpSrcAddr, i.IpAddr) {
		Log(fmt.Sprintf("arp find ip addr conflect\n"))
		return
	}
	i.SetArpCache(arpSrcAddr, arpSrcMac)
	// 对目的IP为本机的ARP询问请求进行回应
	if arpOption == protocol.ARP_REQUEST && bytes.Equal(arpDstAddr, i.IpAddr) {
		arpPkt := make([]byte, 0, 28)
		arpPkt, err := protocol.BuildArpPkt(arpPkt, protocol.ARP_REPLY, i.MacAddr, i.IpAddr, arpSrcMac, arpSrcAddr)
		if err != nil {
			Log(fmt.Sprintf("build arp packet error: %v\n", err))
			return
		}
		i.TxEthernet(arpPkt, arpSrcMac, protocol.ETH_PROTO_ARP)
	}
}

func (i *NetIf) ListArp() []*ArpCache {
	i.ArpLock.Lock()
	defer i.ArpLock.Unlock()
	ret := make([]*ArpCache, 0)
	i.ArpCacheTable.For(func(key IpAddrHash, value *ArpCache) (next bool) {
		v := *value
		ret = append(ret, &v)
		return true
	})
	return ret
}
