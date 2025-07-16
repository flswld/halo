package engine

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"math/bits"
	"time"

	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
)

const (
	DhcpClientPort = 68
	DhcpServerPort = 67
	DhcpLeaseTime  = 3600

	DhcpOptionSubnetMask       = 1
	DhcpOptionRouter           = 3
	DhcpOptionDomainNameServer = 6
	DhcpOptionHostName         = 12
	DhcpOptionReqIpAddr        = 50
	DhcpOptionIpAddrLeaseTime  = 51

	DhcpOptionMsgType         = 53
	DhcpOptionMsgTypeDiscover = 1
	DhcpOptionMsgTypeOffer    = 2
	DhcpOptionMsgTypeRequest  = 3
	DhcpOptionMsgTypeAck      = 5
	DhcpOptionMsgTypeNak      = 6
	DhcpOptionMsgTypeRelease  = 7

	DhcpOptionServerIdentifier     = 54
	DhcpOptionParameterRequestList = 55
	DhcpOptionRenewalTimeValue     = 58
	DhcpOptionRebindingTimeValue   = 59
	DhcpOptionClientIdentifier     = 61

	DhcpBootMsgTypeRequest = 1
	DhcpBootMsgTypeReply   = 2
)

var (
	DhcpMagicCookie = []byte{0x63, 0x82, 0x53, 0x63}
)

// DhcpLease DHCP租期
type DhcpLease struct {
	IpAddr   [4]byte            // ip地址
	MacAddr  [6]byte            // mac地址
	ExpTime  uint32             // 过期时间
	HostName mem.StaticString64 // 主机名
}

// DhcpOption DHCP选项
type DhcpOption struct {
	Type         uint8
	MsgType      uint8
	IpAddr       []byte
	SubnetMask   []byte
	ServerIpAddr []byte
	TimeValue    uint32
	HostName     string
	MacAddr      []byte
}

func ParseDhcpOption(optionData []byte) map[uint8]*DhcpOption {
	dhcpOptionMap := make(map[uint8]*DhcpOption)
	i := 0
	for {
		if optionData[i] == 0xff {
			break
		}
		if i+1 >= len(optionData) {
			break
		}
		code := optionData[i]
		length := int(optionData[i+1])
		if i+2+length > len(optionData) {
			break
		}
		data := optionData[i+2 : i+2+length]
		switch code {
		case DhcpOptionSubnetMask:
			dhcpOptionMap[code] = &DhcpOption{
				Type:       code,
				SubnetMask: data,
			}
		case DhcpOptionRouter:
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: data,
			}
		case DhcpOptionDomainNameServer:
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: data[0:4],
			}
		case DhcpOptionHostName:
			dhcpOptionMap[code] = &DhcpOption{
				Type:     code,
				HostName: string(data),
			}
		case DhcpOptionReqIpAddr:
			dhcpOptionMap[code] = &DhcpOption{
				Type:   code,
				IpAddr: data,
			}
		case DhcpOptionMsgType:
			dhcpOptionMap[code] = &DhcpOption{
				Type:    code,
				MsgType: data[0],
			}
		}
		i += 2 + length
	}
	return dhcpOptionMap
}

func BuildDhcpOption(dhcpOptionMap map[uint8]*DhcpOption) []byte {
	optionData := make([]byte, 0)
	for _, dhcpOption := range dhcpOptionMap {
		switch dhcpOption.Type {
		case DhcpOptionSubnetMask:
			subnetMask := dhcpOption.SubnetMask
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, subnetMask[0], subnetMask[1], subnetMask[2], subnetMask[3]}...)
		case DhcpOptionRouter:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionDomainNameServer:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionReqIpAddr:
			ipAddr := dhcpOption.IpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, ipAddr[0], ipAddr[1], ipAddr[2], ipAddr[3]}...)
		case DhcpOptionIpAddrLeaseTime:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionMsgType:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x01, dhcpOption.MsgType}...)
		case DhcpOptionServerIdentifier:
			serverIpAddr := dhcpOption.ServerIpAddr
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, serverIpAddr[0], serverIpAddr[1], serverIpAddr[2], serverIpAddr[3]}...)
		case DhcpOptionParameterRequestList:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x03, 0x01, 0x03, 0x06}...)
		case DhcpOptionRenewalTimeValue:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionRebindingTimeValue:
			timeValue := dhcpOption.TimeValue
			optionData = append(optionData, []byte{dhcpOption.Type, 0x04, uint8(timeValue >> 24), uint8(timeValue >> 16), uint8(timeValue >> 8), uint8(timeValue >> 0)}...)
		case DhcpOptionClientIdentifier:
			optionData = append(optionData, []byte{dhcpOption.Type, 0x07, 0x01}...)
			optionData = append(optionData, dhcpOption.MacAddr...)
		default:
		}
	}
	optionData = append(optionData, 0xff)
	return optionData
}

func ParseDhcpPkt(pkt []byte) (transactionId []byte, yourIpAddr []byte, clientMacAddr []byte, dhcpOptionMap map[uint8]*DhcpOption, err error) {
	if len(pkt) < 240 {
		return nil, nil, nil, nil, errors.New("dhcp packet len < 240 bytes")
	}
	if !bytes.Equal(pkt[236:240], DhcpMagicCookie) {
		return nil, nil, nil, nil, errors.New("dhcp magic cookie error")
	}
	transactionId = pkt[4:8]
	yourIpAddr = pkt[16:20]
	clientMacAddr = pkt[28:34]
	dhcpOptionMap = ParseDhcpOption(pkt[240:])
	return transactionId, yourIpAddr, clientMacAddr, dhcpOptionMap, nil
}

func BuildDhcpPkt(pkt []byte, dhcpBootMsgType uint8, transactionId []byte, yourIpAddr []byte, clientMacAddr []byte, dhcpOptionMap map[uint8]*DhcpOption) ([]byte, error) {
	if pkt == nil {
		pkt = make([]byte, 0, 240)
	}
	if len(transactionId) != 4 {
		return nil, errors.New("transaction id len is not 4 bytes")
	}
	if len(yourIpAddr) != 4 {
		return nil, errors.New("your ip addr len is not 4 bytes")
	}
	if len(clientMacAddr) != 6 {
		return nil, errors.New("client mac addr len is not 6 bytes")
	}
	pkt = append(pkt, dhcpBootMsgType, 0x01, 0x06, 0x00)
	pkt = append(pkt, transactionId...)
	pkt = append(pkt, 0x00, 0x00)
	pkt = append(pkt, 0x80, 0x00)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, yourIpAddr...)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, 0x00, 0x00, 0x00, 0x00)
	pkt = append(pkt, clientMacAddr...)
	pkt = append(pkt, make([]byte, 10)...)
	pkt = append(pkt, make([]byte, 64)...)
	pkt = append(pkt, make([]byte, 128)...)
	pkt = append(pkt, DhcpMagicCookie...)
	optionData := BuildDhcpOption(dhcpOptionMap)
	pkt = append(pkt, optionData...)
	return pkt, nil
}

func (i *NetIf) RxDhcp(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte) {
	if udpSrcPort == DhcpClientPort && udpDstPort == DhcpServerPort {
		if !i.Config.DhcpServerEnable {
			return
		}
		transactionId, _, clientMacAddr, dhcpOptionMap, err := ParseDhcpPkt(udpPayload)
		if err != nil {
			Log(fmt.Sprintf("parse dhcp packet error: %v\n", err))
			return
		}
		optionMsgType := dhcpOptionMap[DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		i.DhcpLock.Lock()
		defer i.DhcpLock.Unlock()
		switch optionMsgType.MsgType {
		case DhcpOptionMsgTypeDiscover:
			clientIpAddrU := uint32(0)
			optionReqIpAddr := dhcpOptionMap[DhcpOptionReqIpAddr]
			if optionReqIpAddr != nil {
				reqIpAddrU := protocol.IpAddrToU(optionReqIpAddr.IpAddr)
				dhcpLease, exist := i.DhcpLeaseTable.Get(IpAddrHash(reqIpAddrU))
				if exist && bytes.Equal(dhcpLease.MacAddr[:], clientMacAddr) {
					clientIpAddrU = reqIpAddrU
				}
			}
			if clientIpAddrU == 0 {
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
			optionHostName := dhcpOptionMap[DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			Log(fmt.Sprintf("dhcp server offer ip: %v, name: %v, mac: % 02x\n", clientIpAddr, hostName, clientMacAddr))
			i.TxDhcp(DhcpServerPort, DhcpClientPort, transactionId, clientIpAddr, clientMacAddr, map[uint8]*DhcpOption{
				DhcpOptionMsgType:            {Type: DhcpOptionMsgType, MsgType: DhcpOptionMsgTypeOffer},
				DhcpOptionSubnetMask:         {Type: DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
				DhcpOptionRouter:             {Type: DhcpOptionRouter, ServerIpAddr: i.IpAddr},
				DhcpOptionDomainNameServer:   {Type: DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
				DhcpOptionIpAddrLeaseTime:    {Type: DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
				DhcpOptionRebindingTimeValue: {Type: DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
				DhcpOptionRenewalTimeValue:   {Type: DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
				DhcpOptionServerIdentifier:   {Type: DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
		case DhcpOptionMsgTypeRequest:
			optionReqIpAddr := dhcpOptionMap[DhcpOptionReqIpAddr]
			if optionReqIpAddr == nil {
				return
			}
			hostName := ""
			optionHostName := dhcpOptionMap[DhcpOptionHostName]
			if optionHostName != nil {
				hostName = optionHostName.HostName
			}
			var dhcpLease *DhcpLease
			var exist bool
			var ok bool
			reqIpAddrU := protocol.IpAddrToU(optionReqIpAddr.IpAddr)
			selfIpAddrU := protocol.IpAddrToU(i.IpAddr)
			networkMaskU := protocol.IpAddrToU(i.NetworkMask)
			if selfIpAddrU&networkMaskU != reqIpAddrU&networkMaskU {
				goto dhcp_nak
			}
			dhcpLease, exist = i.DhcpLeaseTable.Get(IpAddrHash(reqIpAddrU))
			if exist && !bytes.Equal(dhcpLease.MacAddr[:], clientMacAddr) {
				goto dhcp_nak
			}
			if !exist {
				dhcpLease = mem.MallocType[DhcpLease](i.StaticHeap, 1)
				if dhcpLease == nil {
					goto dhcp_nak
				}
			}
			copy(dhcpLease.IpAddr[:], optionReqIpAddr.IpAddr)
			copy(dhcpLease.MacAddr[:], clientMacAddr)
			dhcpLease.ExpTime = i.Router.TimeNow + DhcpLeaseTime
			dhcpLease.HostName.Set(hostName)
			ok = i.DhcpLeaseTable.Set(IpAddrHash(reqIpAddrU), dhcpLease)
			if !ok {
				goto dhcp_nak
			}
			Log(fmt.Sprintf("dhcp server ack ip: %v, name: %v, mac: % 02x\n", optionReqIpAddr.IpAddr, hostName, clientMacAddr))
			i.TxDhcp(DhcpServerPort, DhcpClientPort, transactionId, optionReqIpAddr.IpAddr, clientMacAddr, map[uint8]*DhcpOption{
				DhcpOptionMsgType:            {Type: DhcpOptionMsgType, MsgType: DhcpOptionMsgTypeAck},
				DhcpOptionSubnetMask:         {Type: DhcpOptionSubnetMask, SubnetMask: i.NetworkMask},
				DhcpOptionRouter:             {Type: DhcpOptionRouter, ServerIpAddr: i.IpAddr},
				DhcpOptionDomainNameServer:   {Type: DhcpOptionDomainNameServer, ServerIpAddr: i.DnsServerAddr},
				DhcpOptionIpAddrLeaseTime:    {Type: DhcpOptionIpAddrLeaseTime, TimeValue: DhcpLeaseTime},
				DhcpOptionRebindingTimeValue: {Type: DhcpOptionRebindingTimeValue, TimeValue: DhcpLeaseTime * 0.875},
				DhcpOptionRenewalTimeValue:   {Type: DhcpOptionRenewalTimeValue, TimeValue: DhcpLeaseTime * 0.5},
				DhcpOptionServerIdentifier:   {Type: DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
		dhcp_nak:
			i.TxDhcp(DhcpServerPort, DhcpClientPort, transactionId, []byte{0x00, 0x00, 0x00, 0x00}, clientMacAddr, map[uint8]*DhcpOption{
				DhcpOptionMsgType:          {Type: DhcpOptionMsgType, MsgType: DhcpOptionMsgTypeNak},
				DhcpOptionServerIdentifier: {Type: DhcpOptionServerIdentifier, ServerIpAddr: i.IpAddr},
			})
			return
		case DhcpOptionMsgTypeRelease:
			ipv4SrcAddrU := protocol.IpAddrToU(ipv4SrcAddr)
			dhcpLease, exist := i.DhcpLeaseTable.Get(IpAddrHash(ipv4SrcAddrU))
			if exist {
				Log(fmt.Sprintf("dhcp server release ip: %v, name: %v, mac: % 02x\n", dhcpLease.IpAddr, dhcpLease.HostName, clientMacAddr))
				i.DhcpLeaseTable.Del(IpAddrHash(ipv4SrcAddrU))
				mem.FreeType[DhcpLease](i.StaticHeap, dhcpLease)
			}
		default:
		}
	} else if udpSrcPort == DhcpServerPort && udpDstPort == DhcpClientPort {
		if !i.Config.DhcpClientEnable {
			return
		}
		if protocol.IpAddrToU(i.IpAddr) != 0 {
			return
		}
		transactionId, yourIpAddr, _, dhcpOptionMap, err := ParseDhcpPkt(udpPayload)
		if err != nil {
			Log(fmt.Sprintf("parse dhcp packet error: %v\n", err))
			return
		}
		if !bytes.Equal(transactionId, i.DhcpClientTransactionId) {
			return
		}
		optionMsgType := dhcpOptionMap[DhcpOptionMsgType]
		if optionMsgType == nil {
			return
		}
		switch optionMsgType.MsgType {
		case DhcpOptionMsgTypeOffer:
			i.TxDhcp(DhcpClientPort, DhcpServerPort, transactionId, []byte{0x00, 0x00, 0x00, 0x00}, i.MacAddr, map[uint8]*DhcpOption{
				DhcpOptionMsgType:              {Type: DhcpOptionMsgType, MsgType: DhcpOptionMsgTypeRequest},
				DhcpOptionClientIdentifier:     {Type: DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
				DhcpOptionReqIpAddr:            {Type: DhcpOptionReqIpAddr, IpAddr: yourIpAddr},
				DhcpOptionServerIdentifier:     {Type: DhcpOptionServerIdentifier, ServerIpAddr: ipv4SrcAddr},
				DhcpOptionParameterRequestList: {Type: DhcpOptionParameterRequestList},
			})
		case DhcpOptionMsgTypeAck:
			copy(i.IpAddr, yourIpAddr)
			Log(fmt.Sprintf("dhcp client get ip: %v\n", yourIpAddr))
			optionSubnetMask := dhcpOptionMap[DhcpOptionSubnetMask]
			if optionSubnetMask != nil {
				copy(i.NetworkMask, optionSubnetMask.SubnetMask)
				Log(fmt.Sprintf("dhcp client get subnet mask: %v\n", optionSubnetMask.SubnetMask))
			}
			optionRouter := dhcpOptionMap[DhcpOptionRouter]
			if optionRouter != nil {
				nextHop := make([]byte, 4)
				copy(nextHop, optionRouter.IpAddr)
				i.Router.RouteTable.AddRoute(&RouteEntry{
					DstIpAddr:   []byte{0x00, 0x00, 0x00, 0x00},
					NetworkMask: []byte{0x00, 0x00, 0x00, 0x00},
					NextHop:     nextHop,
					NetIf:       i.Config.Name,
				})
				dstIpAddrU := protocol.IpAddrToU(i.IpAddr) & protocol.IpAddrToU(i.NetworkMask)
				dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
				i.Router.RouteTable.AddRoute(&RouteEntry{
					DstIpAddr:   dstIpAddr,
					NetworkMask: i.NetworkMask,
					NextHop:     nil,
					NetIf:       i.Config.Name,
				})
				Log(fmt.Sprintf("dhcp client get router: %v\n", optionRouter.IpAddr))
			}
			optionDomainNameServer := dhcpOptionMap[DhcpOptionDomainNameServer]
			if optionDomainNameServer != nil {
				copy(i.DnsServerAddr, optionDomainNameServer.IpAddr)
				Log(fmt.Sprintf("dhcp client get dns: %v\n", optionDomainNameServer.IpAddr))
				for _, netIf := range i.Router.NetIfMap {
					if !netIf.Config.DhcpServerEnable {
						continue
					}
					copy(netIf.DnsServerAddr, optionDomainNameServer.IpAddr)
				}
			}
			i.SendFreeArp()
		default:
		}
	}
}

func (i *NetIf) TxDhcp(udpSrcPort uint16, udpDstPort uint16, transactionId []byte, yourIpAddr []byte, clientMacAddr []byte, dhcpOptionMap map[uint8]*DhcpOption) bool {
	dhcpBootMsgType := uint8(0)
	if udpSrcPort == DhcpClientPort && udpDstPort == DhcpServerPort {
		dhcpBootMsgType = DhcpBootMsgTypeRequest
	} else if udpSrcPort == DhcpServerPort && udpDstPort == DhcpClientPort {
		dhcpBootMsgType = DhcpBootMsgTypeReply
	}
	dhcpPkt, err := BuildDhcpPkt(nil, dhcpBootMsgType, transactionId, yourIpAddr, clientMacAddr, dhcpOptionMap)
	if err != nil {
		Log(fmt.Sprintf("build dhcp packet error: %v\n", err))
		return false
	}
	return i.TxUdp(dhcpPkt, udpSrcPort, udpDstPort, []byte{255, 255, 255, 255})
}

func (i *NetIf) DhcpDiscover() {
	i.DhcpClientTransactionId = make([]byte, 4)
	_, err := rand.Read(i.DhcpClientTransactionId)
	if err != nil {
		i.DhcpClientTransactionId[0] = 0x45
		i.DhcpClientTransactionId[1] = 0x67
		i.DhcpClientTransactionId[2] = 0x89
		i.DhcpClientTransactionId[3] = 0xab
	}
	i.TxDhcp(DhcpClientPort, DhcpServerPort, i.DhcpClientTransactionId, []byte{0x00, 0x00, 0x00, 0x00}, i.MacAddr, map[uint8]*DhcpOption{
		DhcpOptionMsgType:              {Type: DhcpOptionMsgType, MsgType: DhcpOptionMsgTypeDiscover},
		DhcpOptionClientIdentifier:     {Type: DhcpOptionClientIdentifier, MacAddr: i.MacAddr},
		DhcpOptionParameterRequestList: {Type: DhcpOptionParameterRequestList},
	})
}

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
				mem.FreeType[DhcpLease](i.StaticHeap, dhcpLease)
			}
			return true
		})
		i.DhcpLock.Unlock()
	}
	i.Router.StopWaitGroup.Done()
}
