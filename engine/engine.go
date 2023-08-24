package engine

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flswld/halo/protocol"
)

type Config struct {
	DebugLog     bool                  // 调试日志
	NetIfList    []*NetIfConfig        // 网卡列表
	RoutingTable []*RoutingEntryConfig // 路由表
}

type NetIfConfig struct {
	Name        string      // 网卡名
	MacAddr     string      // mac地址
	IpAddr      string      // ip地址
	NetworkMask string      // 子网掩码
	NatEnable   bool        // 网络地址转换
	EthRxChan   chan []byte // 物理层接收管道
	EthTxChan   chan []byte // 物理层发送管道
}

type OutNatEntry struct {
	DstIpAddr uint32
	DstPort   uint16
	SrcIpAddr uint32
	SrcPort   uint16
}

type InNatEntry struct {
	IpAddr        uint32
	Port          uint16
	LastAliveTime uint32
}

type NetIf struct {
	Name        string
	MacAddr     []byte
	IpAddr      []byte
	NetworkMask []byte
	EthRxChan   chan []byte
	EthTxChan   chan []byte
	LoChan      chan []byte
	Engine      *Engine
	// arp缓存表 key:ip value:mac
	ArpCacheTable     map[uint32]uint64
	ArpCacheTableLock sync.RWMutex
	HandleUdp         func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte)
	NatEnable         bool
	NatTable          map[OutNatEntry]*InNatEntry
	NatTableLock      sync.RWMutex
}

type RoutingEntryConfig struct {
	DstIpAddr   string // 目的ip地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳
	NetIf       string // 出接口
}

type RoutingEntry struct {
	DstIpAddr   []byte
	NetworkMask []byte
	NextHop     []byte
	NetIf       string
}

type Engine struct {
	DebugLog       bool
	Stop           bool
	NetIfMap       map[string]*NetIf
	RoutingTable   []*RoutingEntry
	Ipv4PktFwdHook func(ipv4Pkt []byte) []byte
}

var (
	BROADCAST_MAC_ADDR = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
)

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

func InitEngine(config *Config) (*Engine, error) {
	r := new(Engine)
	r.DebugLog = config.DebugLog
	r.Stop = false
	r.NetIfMap = make(map[string]*NetIf)
	r.RoutingTable = make([]*RoutingEntry, 0)
	r.Ipv4PktFwdHook = nil
	// 网卡列表
	for _, netIfConfig := range config.NetIfList {
		macAddr, err := ParseMacAddr(netIfConfig.MacAddr)
		if err != nil {
			return nil, err
		}
		ipAddr, err := ParseIpAddr(netIfConfig.IpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := ParseIpAddr(netIfConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		netIf := &NetIf{
			Name:              netIfConfig.Name,
			MacAddr:           macAddr,
			IpAddr:            ipAddr,
			NetworkMask:       networkMask,
			EthRxChan:         netIfConfig.EthRxChan,
			EthTxChan:         netIfConfig.EthTxChan,
			LoChan:            make(chan []byte, 1024),
			Engine:            r,
			ArpCacheTable:     make(map[uint32]uint64),
			ArpCacheTableLock: sync.RWMutex{},
			HandleUdp:         nil,
			NatEnable:         netIfConfig.NatEnable,
			NatTable:          make(map[OutNatEntry]*InNatEntry),
			NatTableLock:      sync.RWMutex{},
		}
		r.NetIfMap[netIf.Name] = netIf
	}
	// 路由表
	for _, routingEntryConfig := range config.RoutingTable {
		dstIpAddr, err := ParseIpAddr(routingEntryConfig.DstIpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := ParseIpAddr(routingEntryConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		nextHop, err := ParseIpAddr(routingEntryConfig.NextHop)
		if err != nil {
			return nil, err
		}
		r.RoutingTable = append(r.RoutingTable, &RoutingEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: networkMask,
			NextHop:     nextHop,
			NetIf:       routingEntryConfig.NetIf,
		})
	}
	// 直连路由
	for _, netIf := range r.NetIfMap {
		dstIpAddrU := protocol.IpAddrToU(netIf.IpAddr) & protocol.IpAddrToU(netIf.NetworkMask)
		dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
		r.RoutingTable = append(r.RoutingTable, &RoutingEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: netIf.NetworkMask,
			NextHop:     nil,
			NetIf:       netIf.Name,
		})
	}
	sort.Slice(r.RoutingTable, func(i, j int) bool {
		ii := protocol.IpAddrToU(r.RoutingTable[i].DstIpAddr) & protocol.IpAddrToU(r.RoutingTable[i].NetworkMask)
		jj := protocol.IpAddrToU(r.RoutingTable[j].DstIpAddr) & protocol.IpAddrToU(r.RoutingTable[j].NetworkMask)
		return ii > jj
	})
	protocol.SetRandIpHeaderId()
	return r, nil
}

func (e *Engine) RunEngine() {
	for _, netIf := range e.NetIfMap {
		go netIf.PacketHandle()
	}
	time.Sleep(time.Second * 10)
}

func (e *Engine) GetNetIf(name string) *NetIf {
	return e.NetIfMap[name]
}

func (e *Engine) StopEngine() {
	e.Stop = true
}

func (i *NetIf) PacketHandle() {
	for {
		if i.Engine.Stop {
			break
		}
		select {
		case ethFrm := <-i.EthRxChan:
			i.RxEthernet(ethFrm)
		case ipv4Pkt := <-i.LoChan:
			ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ipv4Pkt)
			if err != nil {
				fmt.Printf("parse ip packet error: %v\n", err)
				continue
			}
			if !bytes.Equal(ipv4DstAddr, i.IpAddr) {
				continue
			}
			switch ipv4HeadProto {
			case protocol.IPH_PROTO_ICMP:
				i.RxIcmp(ipv4Payload, ipv4SrcAddr)
			case protocol.IPH_PROTO_UDP:
				i.RxUdp(ipv4Payload, ipv4SrcAddr)
			case protocol.IPH_PROTO_TCP:
				i.RxTcp()
			default:
			}
		}
	}
}
