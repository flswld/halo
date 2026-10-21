package engine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

// DhcpLeaseTime 是引擎默认分配的租期 单位为秒
const DhcpLeaseTime = 3600

// DhcpOfferTime 是待确认地址的保留时间 单位为秒
const DhcpOfferTime = 60

// DhcpLease 保存一条 DHCP 租约及其最近一次 OFFER 信息
type DhcpLease struct {
	IpAddr             protocol.Ipv4Addr  // IP 地址
	MacAddr            protocol.MacAddr   // MAC 地址
	ExpTime            uint32             // 正式租约到期时间 0 表示尚未确认
	HostName           mem.StaticString64 // 主机名
	OfferExpTime       uint32             // OFFER 到期时间 0 表示没有 OFFER 记录
	OfferTransactionId [4]byte            // 最近一次 OFFER 的事务 ID 确认后保留以处理重传
}

// RxDhcp 接收并处理 DHCP 客户端或服务器消息
func (i *NetIf) RxDhcp(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr protocol.Ipv4Addr) {
	if udpSrcPort == protocol.DhcpClientPort && udpDstPort == protocol.DhcpServerPort {
		// 客户端到服务器方向仅在启用 DHCP 服务的接口处理
		if !i.Config.DhcpServerEnable {
			return
		}
		packet, err := protocol.ParseDhcpPkt(udpPayload)
		if err != nil {
			Log(fmt.Sprintf("parse dhcp packet error: %v\n", err))
			return
		}
		if packet.BootMsgType != protocol.DhcpBootMsgTypeRequest {
			return
		}
		optionMsgType := packet.OptionMap[protocol.DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		// 携带服务器标识的消息只由指定服务器处理
		optionServerIdentifier := packet.OptionMap[protocol.DhcpOptionServerIdentifier]
		if optionServerIdentifier != nil && optionServerIdentifier.IpAddr != i.IpAddr {
			return
		}
		i.DhcpLock.Lock()
		defer i.DhcpLock.Unlock()
		now := i.Router.TimeNow
		selfIpAddrU := protocol.IpAddrToU(i.IpAddr)
		networkMaskU := protocol.IpAddrToU(i.NetworkMask)
		networkU := selfIpAddrU & networkMaskU
		broadcastU := networkU | ^networkMaskU
		switch optionMsgType.MsgType {
		case protocol.DhcpOptionMsgTypeDiscover:
			// 同一 MAC 优先复用有效正式租约 其次复用待确认地址
			clientIpAddrU := uint32(0)
			var dhcpLease *DhcpLease
			i.DhcpLeaseTable.For(func(key IpAddrHash, lease *DhcpLease) (next bool) {
				ipAddrU := uint32(key)
				if lease.MacAddr != packet.ClientMacAddr {
					return true
				}
				if lease.ExpTime <= now && lease.OfferExpTime <= now {
					return true
				}
				if ipAddrU <= networkU || ipAddrU >= broadcastU || ipAddrU == selfIpAddrU {
					return true
				}
				if ipAddrU&networkMaskU != networkU {
					return true
				}
				if dhcpLease == nil || lease.ExpTime > now {
					dhcpLease = lease
					clientIpAddrU = ipAddrU
				}
				return lease.ExpTime <= now
			})
			if clientIpAddrU == 0 {
				// 先排除 /31 和 /32 再用 64 位运算避免主机数及回绕计算溢出
				if uint64(broadcastU)-uint64(networkU) <= 1 {
					return
				}
				hostCount := uint64(broadcastU) - uint64(networkU) - 1
				start := MacAddrHash(packet.ClientMacAddr).GetHashCode() % hostCount
				// 从 MAC 哈希确定的起点循环查找 每个候选地址最多检查一次
				for offset := uint64(0); offset < hostCount; offset++ {
					ipAddrU := uint32(uint64(networkU) + 1 + (start+offset)%hostCount)
					if ipAddrU == selfIpAddrU || ipAddrU&networkMaskU != networkU {
						continue
					}
					lease, exist := i.DhcpLeaseTable.Get(IpAddrHash(ipAddrU))
					if exist {
						if lease.ExpTime > now || lease.OfferExpTime > now {
							continue
						}
						i.DhcpLeaseTable.Del(IpAddrHash(ipAddrU))
						mem.FreeType[DhcpLease](i.Router.StaticAllocator, lease)
					}
					clientIpAddrU = ipAddrU
					break
				}
				if clientIpAddrU == 0 {
					Log(fmt.Sprintf("dhcp server no ip found\n"))
					return
				}
			}
			clientIpAddr := protocol.UToIpAddr(clientIpAddrU)
			if dhcpLease == nil {
				dhcpLease = mem.MallocType[DhcpLease](i.Router.StaticAllocator, 1)
				if dhcpLease == nil {
					return
				}
				*dhcpLease = DhcpLease{IpAddr: clientIpAddr, MacAddr: packet.ClientMacAddr}
				if !i.DhcpLeaseTable.Set(IpAddrHash(clientIpAddrU), dhcpLease) {
					mem.FreeType[DhcpLease](i.Router.StaticAllocator, dhcpLease)
					return
				}
			}
			// 只更新 OFFER 信息 不能缩短或提前续期已有正式租约
			dhcpLease.OfferExpTime = now + DhcpOfferTime
			copy(dhcpLease.OfferTransactionId[:], packet.TransactionId)
			hostName := ""
			optionHostName := packet.OptionMap[protocol.DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			Log(fmt.Sprintf("dhcp server offer ip: %v, name: %v, mac: % 02x\n", clientIpAddr, hostName, packet.ClientMacAddr))
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, protocol.DhcpPkt{
				BootMsgType:   protocol.DhcpBootMsgTypeReply,
				TransactionId: packet.TransactionId,
				YourIpAddr:    clientIpAddr,
				ClientMacAddr: packet.ClientMacAddr,
				OptionMap: map[uint8]*protocol.DhcpOption{
					protocol.DhcpOptionMsgType:            {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeOffer},
					protocol.DhcpOptionSubnetMask:         {Type: protocol.DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
					protocol.DhcpOptionRouter:             {Type: protocol.DhcpOptionRouter, ServerIpAddr: i.IpAddr},
					protocol.DhcpOptionDomainNameServer:   {Type: protocol.DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
					protocol.DhcpOptionIpAddrLeaseTime:    {Type: protocol.DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
					protocol.DhcpOptionRebindingTimeValue: {Type: protocol.DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
					protocol.DhcpOptionRenewalTimeValue:   {Type: protocol.DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
					protocol.DhcpOptionServerIdentifier:   {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
				},
			}, protocol.BROADCAST_IPV4_ADDR)
		case protocol.DhcpOptionMsgTypeRequest:
			// SELECTING 和 INIT-REBOOT 使用请求地址 续租和重绑定使用 ciaddr
			reqIpAddr := packet.ClientIpAddr
			dstIpAddr := packet.ClientIpAddr
			optionReqIpAddr := packet.OptionMap[protocol.DhcpOptionReqIpAddr]
			if reqIpAddr == (protocol.Ipv4Addr{}) {
				if optionReqIpAddr == nil {
					return
				}
				reqIpAddr = optionReqIpAddr.IpAddr
				dstIpAddr = protocol.BROADCAST_IPV4_ADDR
			} else if optionServerIdentifier != nil || optionReqIpAddr != nil {
				return
			}
			hostName := ""
			optionHostName := packet.OptionMap[protocol.DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			var dhcpLease *DhcpLease
			var exist bool
			reqIpAddrU := protocol.IpAddrToU(reqIpAddr)
			// 不允许分配本机地址 网络地址或子网广播地址
			if reqIpAddrU <= networkU || reqIpAddrU >= broadcastU || reqIpAddrU == selfIpAddrU {
				goto dhcp_nak
			}
			if networkU != reqIpAddrU&networkMaskU {
				goto dhcp_nak
			}
			dhcpLease, exist = i.DhcpLeaseTable.Get(IpAddrHash(reqIpAddrU))
			if optionServerIdentifier != nil {
				if !exist || dhcpLease.MacAddr != packet.ClientMacAddr || dhcpLease.OfferExpTime == 0 {
					goto dhcp_nak
				}
				if !bytes.Equal(dhcpLease.OfferTransactionId[:], packet.TransactionId) {
					return
				}
				if dhcpLease.OfferExpTime <= now && dhcpLease.ExpTime <= now {
					goto dhcp_nak
				}
			} else {
				if !exist || dhcpLease.MacAddr != packet.ClientMacAddr || dhcpLease.ExpTime == 0 {
					knownClient := false
					i.DhcpLeaseTable.For(func(key IpAddrHash, lease *DhcpLease) (next bool) {
						if lease.MacAddr == packet.ClientMacAddr && lease.ExpTime != 0 {
							knownClient = true
							return false
						}
						return true
					})
					if !knownClient {
						return
					}
					goto dhcp_nak
				}
				if dhcpLease.ExpTime <= now {
					goto dhcp_nak
				}
			}
			dhcpLease.ExpTime = now + DhcpLeaseTime
			if hostName == "" {
				dhcpLease.HostName = mem.StaticString64{}
			} else {
				dhcpLease.HostName.Set(hostName)
			}
			Log(fmt.Sprintf("dhcp server ack ip: %v, name: %v, mac: % 02x\n", reqIpAddr, hostName, packet.ClientMacAddr))
			// 续租和重绑定向 ciaddr 单播 ACK 首次分配仍广播
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, protocol.DhcpPkt{
				BootMsgType:   protocol.DhcpBootMsgTypeReply,
				TransactionId: packet.TransactionId,
				ClientIpAddr:  packet.ClientIpAddr,
				YourIpAddr:    reqIpAddr,
				ClientMacAddr: packet.ClientMacAddr,
				OptionMap: map[uint8]*protocol.DhcpOption{
					protocol.DhcpOptionMsgType:            {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeAck},
					protocol.DhcpOptionSubnetMask:         {Type: protocol.DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
					protocol.DhcpOptionRouter:             {Type: protocol.DhcpOptionRouter, ServerIpAddr: i.IpAddr},
					protocol.DhcpOptionDomainNameServer:   {Type: protocol.DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
					protocol.DhcpOptionIpAddrLeaseTime:    {Type: protocol.DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
					protocol.DhcpOptionRebindingTimeValue: {Type: protocol.DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
					protocol.DhcpOptionRenewalTimeValue:   {Type: protocol.DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
					protocol.DhcpOptionServerIdentifier:   {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
				},
			}, dstIpAddr)
			return
		dhcp_nak:
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, protocol.DhcpPkt{
				BootMsgType:   protocol.DhcpBootMsgTypeReply,
				TransactionId: packet.TransactionId,
				ClientMacAddr: packet.ClientMacAddr,
				OptionMap: map[uint8]*protocol.DhcpOption{
					protocol.DhcpOptionMsgType:          {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeNak},
					protocol.DhcpOptionServerIdentifier: {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
				},
			}, protocol.BROADCAST_IPV4_ADDR)
			return
		case protocol.DhcpOptionMsgTypeRelease:
			if packet.ClientIpAddr == (protocol.Ipv4Addr{}) {
				return
			}
			clientIpAddrU := protocol.IpAddrToU(packet.ClientIpAddr)
			dhcpLease, exist := i.DhcpLeaseTable.Get(IpAddrHash(clientIpAddrU))
			if exist && dhcpLease.MacAddr == packet.ClientMacAddr && dhcpLease.ExpTime != 0 {
				Log(fmt.Sprintf("dhcp server release ip: %v, name: %v, mac: % 02x\n", dhcpLease.IpAddr, dhcpLease.HostName, packet.ClientMacAddr))
				i.DhcpLeaseTable.Del(IpAddrHash(clientIpAddrU))
				mem.FreeType[DhcpLease](i.Router.StaticAllocator, dhcpLease)
			}
		default:
		}
	} else if udpSrcPort == protocol.DhcpServerPort && udpDstPort == protocol.DhcpClientPort {
		// 仅未获得地址的 DHCP 客户端接受服务器方向报文
		if !i.Config.DhcpClientEnable {
			return
		}
		if protocol.IpAddrToU(i.IpAddr) != 0 {
			return
		}
		packet, err := protocol.ParseDhcpPkt(udpPayload)
		if err != nil {
			Log(fmt.Sprintf("parse dhcp packet error: %v\n", err))
			return
		}
		if packet.BootMsgType != protocol.DhcpBootMsgTypeReply || packet.ClientMacAddr != i.MacAddr {
			return
		}
		if !bytes.Equal(packet.TransactionId, i.DhcpClientTransactionId) {
			return
		}
		optionMsgType := packet.OptionMap[protocol.DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		switch optionMsgType.MsgType {
		case protocol.DhcpOptionMsgTypeOffer:
			optionServerIdentifier := packet.OptionMap[protocol.DhcpOptionServerIdentifier]
			if optionServerIdentifier == nil {
				return
			}
			// 接受 OFFER 后携带服务器标识和请求地址发出 REQUEST
			i.TxDhcp(protocol.DhcpClientPort, protocol.DhcpServerPort, protocol.DhcpPkt{
				BootMsgType:   protocol.DhcpBootMsgTypeRequest,
				TransactionId: packet.TransactionId,
				ClientMacAddr: i.MacAddr,
				OptionMap: map[uint8]*protocol.DhcpOption{
					protocol.DhcpOptionMsgType:              {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeRequest},
					protocol.DhcpOptionClientIdentifier:     {Type: protocol.DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
					protocol.DhcpOptionReqIpAddr:            {Type: protocol.DhcpOptionReqIpAddr, IpAddr: packet.YourIpAddr},
					protocol.DhcpOptionServerIdentifier:     {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: optionServerIdentifier.IpAddr},
					protocol.DhcpOptionParameterRequestList: {Type: protocol.DhcpOptionParameterRequestList},
				},
			}, protocol.BROADCAST_IPV4_ADDR)
		case protocol.DhcpOptionMsgTypeAck:
			networkMask := i.NetworkMask
			optionSubnetMask := packet.OptionMap[protocol.DhcpOptionSubnetMask]
			if optionSubnetMask != nil {
				networkMask = optionSubnetMask.SubnetMask
			}
			if packet.YourIpAddr == (protocol.Ipv4Addr{}) || networkMask == (protocol.Ipv4Addr{}) {
				return
			}
			// ACK 将租约参数写入运行时接口并安装默认路由与直连路由
			i.IpAddr = packet.YourIpAddr
			i.NetworkMask = networkMask
			Log(fmt.Sprintf("dhcp client get ip: %v\n", i.IpAddr))
			Log(fmt.Sprintf("dhcp client get subnet mask: %v\n", i.NetworkMask))
			optionRouter := packet.OptionMap[protocol.DhcpOptionRouter]
			if optionRouter != nil {
				i.Gateway = optionRouter.IpAddr
				i.HasGateway = true
				i.Router.RouteTable.AddRoute(&RouteEntry{
					DstIpAddr:   protocol.Ipv4Addr{},
					NetworkMask: protocol.Ipv4Addr{},
					NextHop:     i.Gateway,
					HasNextHop:  true,
					NetIf:       i.Config.Name,
				})
				Log(fmt.Sprintf("dhcp client get router: %v\n", i.Gateway))
			}
			dstIpAddrU := protocol.IpAddrToU(i.IpAddr) & protocol.IpAddrToU(i.NetworkMask)
			dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
			i.Router.RouteTable.AddRoute(&RouteEntry{
				DstIpAddr:   dstIpAddr,
				NetworkMask: i.NetworkMask,
				HasNextHop:  false,
				NetIf:       i.Config.Name,
			})
			optionDomainNameServer := packet.OptionMap[protocol.DhcpOptionDomainNameServer]
			if optionDomainNameServer != nil {
				// WAN 获得的 DNS 地址同步给本路由器上的 DHCP 服务接口
				i.DnsServerAddr = optionDomainNameServer.IpAddr
				Log(fmt.Sprintf("dhcp client get dns: %v\n", optionDomainNameServer.IpAddr))
				for _, netIf := range i.Router.NetIfMap {
					if !netIf.Config.DhcpServerEnable {
						continue
					}
					netIf.DnsServerAddr = optionDomainNameServer.IpAddr
				}
			}
			i.SendFreeArp()
		default:
		}
	}
}

// TxDhcp 构建 DHCP 消息并通过现有 UDP 路由和 ARP 路径发送
// 广播时 ipv4DstAddr 传入 protocol.BROADCAST_IPV4_ADDR 其余地址按单播发送
func (i *NetIf) TxDhcp(udpSrcPort uint16, udpDstPort uint16, packet protocol.DhcpPkt, ipv4DstAddr protocol.Ipv4Addr) bool {
	dhcpPkt, err := protocol.BuildDhcpPkt(nil, packet)
	if err != nil {
		Log(fmt.Sprintf("build dhcp packet error: %v\n", err))
		return false
	}
	return i.TxUdp(dhcpPkt, udpSrcPort, udpDstPort, ipv4DstAddr)
}

// DhcpDiscover 发起 DHCP 地址发现
func (i *NetIf) DhcpDiscover() {
	i.DhcpClientTransactionId = make([]byte, 4)
	// 事务 ID 用于过滤其他客户端或旧发现流程的服务器响应
	_, err := rand.Read(i.DhcpClientTransactionId)
	if err != nil {
		i.DhcpClientTransactionId[0] = 0x45
		i.DhcpClientTransactionId[1] = 0x67
		i.DhcpClientTransactionId[2] = 0x89
		i.DhcpClientTransactionId[3] = 0xab
	}
	i.TxDhcp(protocol.DhcpClientPort, protocol.DhcpServerPort, protocol.DhcpPkt{
		BootMsgType:   protocol.DhcpBootMsgTypeRequest,
		TransactionId: i.DhcpClientTransactionId,
		ClientMacAddr: i.MacAddr,
		OptionMap: map[uint8]*protocol.DhcpOption{
			protocol.DhcpOptionMsgType:              {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeDiscover},
			protocol.DhcpOptionClientIdentifier:     {Type: protocol.DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
			protocol.DhcpOptionParameterRequestList: {Type: protocol.DhcpOptionParameterRequestList},
		},
	}, protocol.BROADCAST_IPV4_ADDR)
}

// ListDhcp 返回有效正式租约的副本 不包含待确认地址
func (i *NetIf) ListDhcp() []*DhcpLease {
	i.DhcpLock.Lock()
	defer i.DhcpLock.Unlock()
	ret := make([]*DhcpLease, 0)
	i.DhcpLeaseTable.For(func(key IpAddrHash, value *DhcpLease) (next bool) {
		if value.ExpTime <= i.Router.TimeNow {
			return true
		}
		v := *value
		ret = append(ret, &v)
		return true
	})
	return ret
}

// DhcpLeaseClear 定期回收正式租约和 OFFER 均已过期的记录
func (i *NetIf) DhcpLeaseClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Router.Stop.Load() {
			break
		}
		i.DhcpLock.Lock()
		i.DhcpLeaseTable.For(func(ipAddrU IpAddrHash, dhcpLease *DhcpLease) (next bool) {
			if dhcpLease.ExpTime <= i.Router.TimeNow && dhcpLease.OfferExpTime <= i.Router.TimeNow {
				i.DhcpLeaseTable.Del(ipAddrU)
				mem.FreeType[DhcpLease](i.Router.StaticAllocator, dhcpLease)
			}
			return true
		})
		i.DhcpLock.Unlock()
	}
	ticker.Stop()
	i.Router.StopWaitGroup.Done()
}
