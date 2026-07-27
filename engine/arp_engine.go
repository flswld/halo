package engine

import (
	"bytes"
	"fmt"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

// ArpCache 保存一条 ARP 缓存记录
type ArpCache struct {
	IpAddr  uint32  // IP 地址
	MacAddr [6]byte // MAC 地址
	ExpTime uint32  // 过期时间
}

// SendFreeArp 发送免费 ARP 请求
func (i *NetIf) SendFreeArp() {
	arpPkt := make([]byte, 0, 28)
	arpPkt, err := protocol.BuildArpPkt(arpPkt, protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, protocol.BROADCAST_MAC_ADDR, i.IpAddr)
	if err != nil {
		Log(fmt.Sprintf("build arp packet error: %v\n", err))
		return
	}
	i.TxEthernet(arpPkt, protocol.BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
}

// GetArpCache 查询指定 IP 地址的 ARP 缓存
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
		// 当前报文不会排队等待解析 上层需在后续报文中重新尝试发送
		i.SendArpReq(ipAddr)
		return nil
	}
	return arpCache
}

// SendArpReq 发送指定 IP 地址的 ARP 查询请求
func (i *NetIf) SendArpReq(ipAddr []byte) {
	arpPkt := make([]byte, 0, 28)
	arpPkt, err := protocol.BuildArpPkt(arpPkt, protocol.ARP_REQUEST, i.MacAddr, i.IpAddr, protocol.BROADCAST_MAC_ADDR, ipAddr)
	if err != nil {
		Log(fmt.Sprintf("build arp packet error: %v\n", err))
		return
	}
	i.TxEthernet(arpPkt, protocol.BROADCAST_MAC_ADDR, protocol.ETH_PROTO_ARP)
}

// SetArpCache 新增或刷新一条 ARP 缓存
func (i *NetIf) SetArpCache(ipAddr []byte, macAddr []byte) {
	i.ArpLock.Lock()
	defer i.ArpLock.Unlock()
	ipAddrU := protocol.IpAddrToU(ipAddr)
	arpCache, exist := i.ArpCacheTable.Get(IpAddrHash(ipAddrU))
	if !exist {
		// ARP 项与其他接口状态共用路由器静态内存池
		arpCache = mem.MallocType[ArpCache](i.Router.StaticAllocator, 1)
		if arpCache == nil {
			return
		}
	}
	arpCache.IpAddr = ipAddrU
	copy(arpCache.MacAddr[:], macAddr)
	arpCache.ExpTime = i.Router.TimeNow + 300
	i.ArpCacheTable.Set(IpAddrHash(ipAddrU), arpCache)
}

// HandleArp 处理收到的 ARP 报文并按需回应请求
func (i *NetIf) HandleArp(ethPayload []byte, ethSrcMac []byte) {
	arpOption, arpSrcMac, arpSrcAddr, _, arpDstAddr, err := protocol.ParseArpPkt(ethPayload)
	if err != nil {
		Log(fmt.Sprintf("parse arp packet error: %v\n", err))
		return
	}
	if !bytes.Equal(arpSrcMac, ethSrcMac) {
		// 二层源地址和 ARP 声明不一致时拒绝污染缓存
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

// ArpTableRefresh 定期刷新即将过期的 ARP 缓存
func (i *NetIf) ArpTableRefresh() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Router.Stop.Load() {
			break
		}
		i.ArpLock.Lock()
		i.ArpCacheTable.For(func(key IpAddrHash, value *ArpCache) (next bool) {
			// 到期前主动询问可减少活跃邻居在过期瞬间的发包中断
			if i.Router.TimeNow > value.ExpTime-10 {
				i.SendArpReq(protocol.UToIpAddr(value.IpAddr))
			}
			return true
		})
		i.ArpLock.Unlock()
	}
	i.Router.StopWaitGroup.Done()
}

// ArpTableClear 定期清理过期的 ARP 缓存
func (i *NetIf) ArpTableClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Router.Stop.Load() {
			break
		}
		i.ArpLock.Lock()
		i.ArpCacheTable.For(func(key IpAddrHash, value *ArpCache) (next bool) {
			if i.Router.TimeNow > value.ExpTime {
				i.ArpCacheTable.Del(key)
				mem.FreeType[ArpCache](i.Router.StaticAllocator, value)
			}
			return true
		})
		i.ArpLock.Unlock()
	}
	i.Router.StopWaitGroup.Done()
}

// ListArp 返回当前 ARP 缓存的副本
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
