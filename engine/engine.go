package engine

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flswld/halo/cpu"
	"github.com/flswld/halo/protocol"
)

var (
	DefaultLogWriter io.Writer = nil
)

func Log(msg string) {
	if DefaultLogWriter != nil {
		_, _ = DefaultLogWriter.Write([]byte(msg))
	}
}

// Config 协议栈配置
type Config struct {
	DebugLog        bool                  // 调试日志
	CheckSumDisable bool                  // 禁用校验和检查
	NetIfList       []*NetIfConfig        // 网卡列表
	RoutingTable    []*RoutingEntryConfig // 路由表
}

// NetIfConfig 网卡配置
type NetIfConfig struct {
	Name                string                       // 网卡名
	MacAddr             string                       // mac地址
	IpAddr              string                       // ip地址
	NetworkMask         string                       // 子网掩码
	NatEnable           bool                         // 开启网络地址转换
	NatType             int                          // 网络地址转换类型
	NatPortMappingTable []*NatPortMappingEntryConfig // 网络地址转换端口映射表
	DnsServerAddr       string                       // dns服务器地址
	DhcpServerEnable    bool                         // 开启dhcp服务器
	DhcpClientEnable    bool                         // 开启dhcp客户端
	EthRxFunc           func() (pkt []byte)          // 网卡收包方法
	EthTxFunc           func(pkt []byte)             // 网卡发包方法
	BindCpuCore         int                          // 绑定的cpu核心
}

// NatPortMappingEntryConfig NAT端口映射配置
type NatPortMappingEntryConfig struct {
	WanPort       uint16 // wan口端口
	LanHostIpAddr string // lan口主机ip地址
	LanHostPort   uint16 // lan口主机端口
	Ipv4HeadProto uint8  // ip头部协议
}

// RoutingEntryConfig 路由条目配置
type RoutingEntryConfig struct {
	DstIpAddr   string // 目的ip地址
	NetworkMask string // 网络掩码
	NextHop     string // 下一跳
	NetIf       string // 出接口
}

// NetIf 网卡
type NetIf struct {
	Config                  *NetIfConfig
	MacAddr                 []byte                      // mac地址
	IpAddr                  []byte                      // ip地址
	NetworkMask             []byte                      // 子网掩码
	EthTxBuffer             []byte                      // 网卡发包缓冲区
	EthTxLock               cpu.SpinLock                // 网卡发包锁
	LoChan                  chan []byte                 // 本地回环管道
	Engine                  *Engine                     // 归属Engine指针
	ArpCacheTable           map[uint32][]byte           // arp缓存表 key:ip value:mac
	ArpLock                 sync.RWMutex                // arp锁
	NatWanFlowTable         map[NatWanFlowHash]*NatFlow // wan口回程包nat流映射表
	NatFlowTable            map[NatFlowHash]*NatFlow    // nat流表
	NatPortAlloc            map[uint32]*PortAlloc       // nat端口分配表 key:目的ip value:端口分配信息
	NatPortMappingTable     []*NatPortMappingEntry      // 网络地址转换端口映射表
	NatLock                 sync.RWMutex                // nat锁
	DnsServerAddr           []byte                      // dns服务器地址
	DhcpLeaseMap            map[uint32]*DhcpLease       // dhcp租期表
	DhcpLock                sync.RWMutex                // dhcp锁
	DhcpClientTransactionId []byte                      // dhcp客户端事务id
	HandleUdp               func(payload []byte, srcPort uint16, dstPort uint16, srcAddr []byte)
	HandleTcp               func(payload []byte, srcPort uint16, dstPort uint16, seqNum uint32, ackNum uint32, flags uint8, srcAddr []byte)
}

// Engine 协议栈
type Engine struct {
	Config         *Config
	Stop           atomic.Bool                                       // 停止标志
	NetIfMap       map[string]*NetIf                                 // 网络接口集合 key:接口名 value:接口实例
	RouteTable     *RouteTable                                       // 路由表
	Ipv4PktFwdHook func(raw []byte, dir int) (drop bool, mod []byte) // ip报文转发钩子
}

func InitEngine(config *Config) (*Engine, error) {
	e := &Engine{
		Config:   config,
		Stop:     atomic.Bool{},
		NetIfMap: make(map[string]*NetIf),
		RouteTable: &RouteTable{
			Root: new(TrieNode),
		},
		Ipv4PktFwdHook: nil,
	}
	// 网卡列表
	for _, netIfConfig := range config.NetIfList {
		macAddr, err := protocol.ParseMacAddr(netIfConfig.MacAddr)
		if err != nil {
			return nil, err
		}
		ipAddr := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.IpAddr != "" {
			ipAddr, err = protocol.ParseIpAddr(netIfConfig.IpAddr)
			if err != nil {
				return nil, err
			}
		}
		networkMask := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.NetworkMask != "" {
			networkMask, err = protocol.ParseIpAddr(netIfConfig.NetworkMask)
			if err != nil {
				return nil, err
			}
		}
		dnsServerAddr := []byte{0x00, 0x00, 0x00, 0x00}
		if netIfConfig.DnsServerAddr != "" {
			dnsServerAddr, err = protocol.ParseIpAddr(netIfConfig.DnsServerAddr)
			if err != nil {
				return nil, err
			}
		}
		netIf := &NetIf{
			Config:                  netIfConfig,
			MacAddr:                 macAddr,
			IpAddr:                  ipAddr,
			NetworkMask:             networkMask,
			EthTxBuffer:             make([]byte, 0, 1514),
			LoChan:                  make(chan []byte, 1024),
			Engine:                  e,
			ArpCacheTable:           make(map[uint32][]byte),
			NatWanFlowTable:         make(map[NatWanFlowHash]*NatFlow),
			NatFlowTable:            make(map[NatFlowHash]*NatFlow),
			NatPortAlloc:            make(map[uint32]*PortAlloc),
			NatPortMappingTable:     make([]*NatPortMappingEntry, 0),
			DnsServerAddr:           dnsServerAddr,
			DhcpLeaseMap:            make(map[uint32]*DhcpLease),
			DhcpClientTransactionId: nil,
			HandleUdp:               nil,
			HandleTcp:               nil,
		}
		for _, natPortMappingEntryConfig := range netIfConfig.NatPortMappingTable {
			lanHostIpAddr, err := protocol.ParseIpAddr(natPortMappingEntryConfig.LanHostIpAddr)
			if err != nil {
				return nil, err
			}
			netIf.NatPortMappingTable = append(netIf.NatPortMappingTable, &NatPortMappingEntry{
				WanPort:       natPortMappingEntryConfig.WanPort,
				LanHostIpAddr: protocol.IpAddrToU(lanHostIpAddr),
				LanHostPort:   natPortMappingEntryConfig.LanHostPort,
				Ipv4HeadProto: natPortMappingEntryConfig.Ipv4HeadProto,
			})
		}
		e.NetIfMap[netIf.Config.Name] = netIf
	}
	// 路由表
	for _, routingEntryConfig := range config.RoutingTable {
		dstIpAddr, err := protocol.ParseIpAddr(routingEntryConfig.DstIpAddr)
		if err != nil {
			return nil, err
		}
		networkMask, err := protocol.ParseIpAddr(routingEntryConfig.NetworkMask)
		if err != nil {
			return nil, err
		}
		nextHop, err := protocol.ParseIpAddr(routingEntryConfig.NextHop)
		if err != nil {
			return nil, err
		}
		e.RouteTable.AddRoute(&RouteEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: networkMask,
			NextHop:     nextHop,
			NetIf:       routingEntryConfig.NetIf,
		})
	}
	// 直连路由
	for _, netIf := range e.NetIfMap {
		if netIf.Config.DhcpClientEnable {
			continue
		}
		dstIpAddrU := protocol.IpAddrToU(netIf.IpAddr) & protocol.IpAddrToU(netIf.NetworkMask)
		dstIpAddr := protocol.UToIpAddr(dstIpAddrU)
		e.RouteTable.AddRoute(&RouteEntry{
			DstIpAddr:   dstIpAddr,
			NetworkMask: netIf.NetworkMask,
			NextHop:     nil,
			NetIf:       netIf.Config.Name,
		})
	}
	if config.CheckSumDisable {
		protocol.CheckSumDisable = true
	}
	protocol.SetRandIpHeaderId()
	return e, nil
}

func (e *Engine) RunEngine() {
	e.Stop.Store(false)
	for _, netIf := range e.NetIfMap {
		if netIf.Config.DhcpClientEnable {
			netIf.DhcpDiscover()
		} else {
			netIf.SendFreeArp()
		}
		go netIf.PacketHandle()
		if netIf.Config.NatEnable {
			go netIf.NatTableClear()
		}
		if netIf.Config.DhcpServerEnable {
			go netIf.DhcpLeaseClear()
		}
	}
}

func (e *Engine) GetNetIf(name string) *NetIf {
	return e.NetIfMap[name]
}

func (e *Engine) StopEngine() {
	e.Stop.Store(true)
}

func (i *NetIf) PacketHandle() {
	if i.Config.BindCpuCore > 0 {
		cpu.BindCpuCore(i.Config.BindCpuCore)
	}
	n := 0
	for {
		ethFrm := i.Config.EthRxFunc()
		if ethFrm != nil {
			i.RxEthernet(ethFrm)
		}
		n++
		if n%100 == 0 {
			if i.Engine.Stop.Load() {
				break
			}
			select {
			case ipv4Pkt := <-i.LoChan:
				ipv4Payload, ipv4HeadProto, ipv4SrcAddr, ipv4DstAddr, err := protocol.ParseIpv4Pkt(ipv4Pkt)
				if err != nil {
					Log(fmt.Sprintf("parse ip packet error: %v\n", err))
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
					i.RxTcp(ipv4Payload, ipv4SrcAddr)
				default:
				}
			default:
			}
		}
	}
}

func (i *NetIf) SendUdpPktByFlow(natFlowHash NatFlowHash, dir int, udpPayload []byte) {
	natFlowHash.Ipv4HeadProto = protocol.IPH_PROTO_UDP
	remoteIpAddr := protocol.UToIpAddr(natFlowHash.RemoteIpAddr)
	lanHostIpAddr := protocol.UToIpAddr(natFlowHash.LanHostIpAddr)
	natFlow := i.NatGetFlowByHash(natFlowHash)
	if natFlow == nil {
		natFlow = i.NatAddFlow(lanHostIpAddr, remoteIpAddr, natFlowHash.LanHostPort, natFlowHash.RemotePort, natFlowHash.Ipv4HeadProto)
		if natFlow == nil {
			return
		}
	}
	natFlow.LastAliveTime = uint32(time.Now().Unix())
	switch dir {
	case LanToWan:
		udpPkt := make([]byte, 0, 1480)
		udpPkt, err := protocol.BuildUdpPkt(udpPkt, udpPayload, natFlow.WanPort, natFlow.RemotePort, i.IpAddr, remoteIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt := make([]byte, 0, 1500)
		ipv4Pkt, err = protocol.BuildIpv4Pkt(ipv4Pkt, udpPkt, protocol.IPH_PROTO_UDP, i.IpAddr, remoteIpAddr)
		if err != nil {
			return
		}
		nextHopIpAddr, _ := i.FindRoute(remoteIpAddr)
		if nextHopIpAddr == nil {
			return
		}
		ethDstMac := i.GetArpCache(nextHopIpAddr)
		if ethDstMac == nil {
			return
		}
		i.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	case WanToLan:
		udpPkt := make([]byte, 0, 1480)
		udpPkt, err := protocol.BuildUdpPkt(udpPkt, udpPayload, natFlow.RemotePort, natFlow.LanHostPort, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		ipv4Pkt := make([]byte, 0, 1500)
		ipv4Pkt, err = protocol.BuildIpv4Pkt(ipv4Pkt, udpPkt, protocol.IPH_PROTO_UDP, remoteIpAddr, lanHostIpAddr)
		if err != nil {
			return
		}
		_, outNetIfName := i.FindRoute(lanHostIpAddr)
		if outNetIfName == "" {
			return
		}
		outNetIf := i.Engine.NetIfMap[outNetIfName]
		ethDstMac := outNetIf.GetArpCache(lanHostIpAddr)
		if ethDstMac == nil {
			return
		}
		outNetIf.TxEthernet(ipv4Pkt, ethDstMac, protocol.ETH_PROTO_IPV4)
	default:
	}
}
