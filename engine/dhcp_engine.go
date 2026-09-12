package engine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"math/bits"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

// DhcpLeaseTime 是引擎默认分配的租期 单位为秒
const DhcpLeaseTime = 3600

// DhcpLease 保存一条 DHCP 租约
type DhcpLease struct {
	IpAddr   protocol.Ipv4Addr  // IP 地址
	MacAddr  protocol.MacAddr   // MAC 地址
	ExpTime  uint32             // 过期时间
	HostName mem.StaticString64 // 主机名
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
		optionMsgType := packet.OptionMap[protocol.DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		i.DhcpLock.Lock()
		defer i.DhcpLock.Unlock()
		switch optionMsgType.MsgType {
		case protocol.DhcpOptionMsgTypeDiscover:
			// 优先复用同一客户端请求且仍归属于该 MAC 地址的租约
			clientIpAddrU := uint32(0)
			optionReqIpAddr := packet.OptionMap[protocol.DhcpOptionReqIpAddr]
			if optionReqIpAddr != nil {
				reqIpAddrU := protocol.IpAddrToU(optionReqIpAddr.IpAddr)
				dhcpLease, exist := i.DhcpLeaseTable.Get(IpAddrHash(reqIpAddrU))
				if exist && dhcpLease.MacAddr == packet.ClientMacAddr {
					clientIpAddrU = reqIpAddrU
				}
			}
			if clientIpAddrU == 0 {
				// 未能复用租约时从接口所在子网顺序寻找未分配主机地址
				selfIpAddrU := protocol.IpAddrToU(i.IpAddr)
				networkU := selfIpAddrU & protocol.IpAddrToU(i.NetworkMask)
				hostBits := 32 - bits.OnesCount32(networkU)
				if hostBits < 32 {
					for ii := uint32(0); ii < uint32(1)<<hostBits; ii++ {
						ipAddrU := networkU + ii
						if uint8(ipAddrU) == 0 || uint8(ipAddrU) == 255 {
							continue
						}
						if ipAddrU == selfIpAddrU {
							continue
						}
						_, exist := i.DhcpLeaseTable.Get(IpAddrHash(ipAddrU))
						if exist {
							continue
						}
						clientIpAddrU = ipAddrU
						break
					}
				}
			}
			if clientIpAddrU == 0 {
				Log(fmt.Sprintf("dhcp server no ip found\n"))
				return
			}
			clientIpAddr := protocol.UToIpAddr(clientIpAddrU)
			hostName := ""
			optionHostName := packet.OptionMap[protocol.DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			Log(fmt.Sprintf("dhcp server offer ip: %v, name: %v, mac: % 02x\n", clientIpAddr, hostName, packet.ClientMacAddr))
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, packet.TransactionId, clientIpAddr, packet.ClientMacAddr, map[uint8]*protocol.DhcpOption{
				protocol.DhcpOptionMsgType:            {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeOffer},
				protocol.DhcpOptionSubnetMask:         {Type: protocol.DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
				protocol.DhcpOptionRouter:             {Type: protocol.DhcpOptionRouter, ServerIpAddr: i.IpAddr},
				protocol.DhcpOptionDomainNameServer:   {Type: protocol.DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
				protocol.DhcpOptionIpAddrLeaseTime:    {Type: protocol.DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
				protocol.DhcpOptionRebindingTimeValue: {Type: protocol.DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
				protocol.DhcpOptionRenewalTimeValue:   {Type: protocol.DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
				protocol.DhcpOptionServerIdentifier:   {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
		case protocol.DhcpOptionMsgTypeRequest:
			// REQUEST 只有在地址属于本子网且未被其他 MAC 占用时才确认
			optionReqIpAddr := packet.OptionMap[protocol.DhcpOptionReqIpAddr]
			if optionReqIpAddr == nil {
				return
			}
			hostName := ""
			optionHostName := packet.OptionMap[protocol.DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			var dhcpLease *DhcpLease
			var exist bool
			var ok bool
			reqIpAddrU := protocol.IpAddrToU(optionReqIpAddr.IpAddr)
			selfIpAddrU := protocol.IpAddrToU(i.IpAddr)
			networkMaskU := protocol.IpAddrToU(i.NetworkMask)
			if reqIpAddrU == 0 {
				goto dhcp_nak
			}
			if selfIpAddrU&networkMaskU != reqIpAddrU&networkMaskU {
				goto dhcp_nak
			}
			dhcpLease, exist = i.DhcpLeaseTable.Get(IpAddrHash(reqIpAddrU))
			if exist && dhcpLease.MacAddr != packet.ClientMacAddr {
				goto dhcp_nak
			}
			if !exist {
				dhcpLease = mem.MallocType[DhcpLease](i.Router.StaticAllocator, 1)
				if dhcpLease == nil {
					goto dhcp_nak
				}
			}
			dhcpLease.IpAddr = optionReqIpAddr.IpAddr
			dhcpLease.MacAddr = packet.ClientMacAddr
			dhcpLease.ExpTime = i.Router.TimeNow + DhcpLeaseTime
			dhcpLease.HostName.Set(hostName)
			ok = i.DhcpLeaseTable.Set(IpAddrHash(reqIpAddrU), dhcpLease)
			if !ok {
				goto dhcp_nak
			}
			Log(fmt.Sprintf("dhcp server ack ip: %v, name: %v, mac: % 02x\n", optionReqIpAddr.IpAddr, hostName, packet.ClientMacAddr))
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, packet.TransactionId, optionReqIpAddr.IpAddr, packet.ClientMacAddr, map[uint8]*protocol.DhcpOption{
				protocol.DhcpOptionMsgType:            {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeAck},
				protocol.DhcpOptionSubnetMask:         {Type: protocol.DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
				protocol.DhcpOptionRouter:             {Type: protocol.DhcpOptionRouter, ServerIpAddr: i.IpAddr},
				protocol.DhcpOptionDomainNameServer:   {Type: protocol.DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
				protocol.DhcpOptionIpAddrLeaseTime:    {Type: protocol.DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
				protocol.DhcpOptionRebindingTimeValue: {Type: protocol.DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
				protocol.DhcpOptionRenewalTimeValue:   {Type: protocol.DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
				protocol.DhcpOptionServerIdentifier:   {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
		dhcp_nak:
			// 任一校验或内存分配失败均向客户端返回 NAK
			i.TxDhcp(protocol.DhcpServerPort, protocol.DhcpClientPort, packet.TransactionId, protocol.Ipv4Addr{}, packet.ClientMacAddr, map[uint8]*protocol.DhcpOption{
				protocol.DhcpOptionMsgType:          {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeNak},
				protocol.DhcpOptionServerIdentifier: {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
			return
		case protocol.DhcpOptionMsgTypeRelease:
			ipv4SrcAddrU := protocol.IpAddrToU(ipv4SrcAddr)
			dhcpLease, exist := i.DhcpLeaseTable.Get(IpAddrHash(ipv4SrcAddrU))
			if exist {
				Log(fmt.Sprintf("dhcp server release ip: %v, name: %v, mac: % 02x\n", dhcpLease.IpAddr, dhcpLease.HostName, packet.ClientMacAddr))
				i.DhcpLeaseTable.Del(IpAddrHash(ipv4SrcAddrU))
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
		if !bytes.Equal(packet.TransactionId, i.DhcpClientTransactionId) {
			return
		}
		optionMsgType := packet.OptionMap[protocol.DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		switch optionMsgType.MsgType {
		case protocol.DhcpOptionMsgTypeOffer:
			// 接受 OFFER 后携带服务器标识和请求地址发出 REQUEST
			i.TxDhcp(protocol.DhcpClientPort, protocol.DhcpServerPort, packet.TransactionId, protocol.Ipv4Addr{}, i.MacAddr, map[uint8]*protocol.DhcpOption{
				protocol.DhcpOptionMsgType:              {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeRequest},
				protocol.DhcpOptionClientIdentifier:     {Type: protocol.DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
				protocol.DhcpOptionReqIpAddr:            {Type: protocol.DhcpOptionReqIpAddr, IpAddr: packet.YourIpAddr},
				protocol.DhcpOptionServerIdentifier:     {Type: protocol.DhcpOptionServerIdentifier, ServerIpAddr: ipv4SrcAddr},
				protocol.DhcpOptionParameterRequestList: {Type: protocol.DhcpOptionParameterRequestList},
			})
		case protocol.DhcpOptionMsgTypeAck:
			// ACK 将租约参数写入运行时接口并安装默认路由与直连路由
			i.IpAddr = packet.YourIpAddr
			Log(fmt.Sprintf("dhcp client get ip: %v\n", packet.YourIpAddr))
			optionSubnetMask := packet.OptionMap[protocol.DhcpOptionSubnetMask]
			if optionSubnetMask != nil {
				i.NetworkMask = optionSubnetMask.SubnetMask
				Log(fmt.Sprintf("dhcp client get subnet mask: %v\n", optionSubnetMask.SubnetMask))
			}
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
				dstIpAddrU := protocol.IpAddrToU(i.IpAddr) & protocol.IpAddrToU(i.NetworkMask)
				dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
				i.Router.RouteTable.AddRoute(&RouteEntry{
					DstIpAddr:   dstIpAddr,
					NetworkMask: i.NetworkMask,
					HasNextHop:  false,
					NetIf:       i.Config.Name,
				})
				Log(fmt.Sprintf("dhcp client get router: %v\n", i.Gateway))
			}
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

// TxDhcp 构建并广播 DHCP 消息
func (i *NetIf) TxDhcp(udpSrcPort uint16, udpDstPort uint16, transactionId []byte, yourIpAddr protocol.Ipv4Addr, clientMacAddr protocol.MacAddr, dhcpOptionMap map[uint8]*protocol.DhcpOption) bool {
	dhcpBootMsgType := uint8(0)
	if udpSrcPort == protocol.DhcpClientPort && udpDstPort == protocol.DhcpServerPort {
		dhcpBootMsgType = protocol.DhcpBootMsgTypeRequest
	} else if udpSrcPort == protocol.DhcpServerPort && udpDstPort == protocol.DhcpClientPort {
		dhcpBootMsgType = protocol.DhcpBootMsgTypeReply
	}
	dhcpPkt, err := protocol.BuildDhcpPkt(nil, protocol.DhcpPkt{
		BootMsgType:   dhcpBootMsgType,
		TransactionId: transactionId,
		YourIpAddr:    yourIpAddr,
		ClientMacAddr: clientMacAddr,
		OptionMap:     dhcpOptionMap,
	})
	if err != nil {
		Log(fmt.Sprintf("build dhcp packet error: %v\n", err))
		return false
	}
	return i.TxUdp(dhcpPkt, udpSrcPort, udpDstPort, protocol.Ipv4Addr{255, 255, 255, 255})
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
	i.TxDhcp(protocol.DhcpClientPort, protocol.DhcpServerPort, i.DhcpClientTransactionId, protocol.Ipv4Addr{}, i.MacAddr, map[uint8]*protocol.DhcpOption{
		protocol.DhcpOptionMsgType:              {Type: protocol.DhcpOptionMsgType, MsgType: protocol.DhcpOptionMsgTypeDiscover},
		protocol.DhcpOptionClientIdentifier:     {Type: protocol.DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
		protocol.DhcpOptionParameterRequestList: {Type: protocol.DhcpOptionParameterRequestList},
	})
}

// ListDhcp 返回当前 DHCP 租约表的副本
func (i *NetIf) ListDhcp() []*DhcpLease {
	i.DhcpLock.Lock()
	defer i.DhcpLock.Unlock()
	ret := make([]*DhcpLease, 0)
	i.DhcpLeaseTable.For(func(key IpAddrHash, value *DhcpLease) (next bool) {
		v := *value
		ret = append(ret, &v)
		return true
	})
	return ret
}

// DhcpLeaseClear 定期清理过期的 DHCP 租约
func (i *NetIf) DhcpLeaseClear() {
	ticker := time.NewTicker(time.Second * 1)
	for {
		<-ticker.C
		if i.Router.Stop.Load() {
			break
		}
		i.DhcpLock.Lock()
		i.DhcpLeaseTable.For(func(ipAddrU IpAddrHash, dhcpLease *DhcpLease) (next bool) {
			if i.Router.TimeNow > dhcpLease.ExpTime {
				i.DhcpLeaseTable.Del(ipAddrU)
				mem.FreeType[DhcpLease](i.Router.StaticAllocator, dhcpLease)
			}
			return true
		})
		i.DhcpLock.Unlock()
	}
	i.Router.StopWaitGroup.Done()
}
