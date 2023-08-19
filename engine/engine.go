package engine

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flswld/halo/protocol"
)

type Config struct {
	DebugLog        bool           // 调试日志
	NetIfConfigList []*NetIfConfig // 网卡列表
}

type NetIfConfig struct {
	Name          string      // 网卡名
	MacAddr       string      // mac地址
	IpAddr        string      // ip地址
	NetworkMask   string      // 子网掩码
	GatewayIpAddr string      // 网关ip地址
	EthRxChan     chan []byte // 物理层接收管道
	EthTxChan     chan []byte // 物理层发送管道
}

type NetIf struct {
	Name          string
	MacAddr       []byte
	IpAddr        []byte
	NetworkMask   []byte
	GatewayIpAddr []byte
	EthRxChan     chan []byte
	EthTxChan     chan []byte
	Engine        *Engine
	// arp缓存表 key:ip value:mac
	ArpCacheTable     map[uint32]uint64
	ArpCacheTableLock sync.RWMutex
	HandleUdp         func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte)
}

type Engine struct {
	DebugLog bool
	Stop     bool
	NetIfMap map[string]*NetIf
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

	for _, netIfConfig := range config.NetIfConfigList {
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
		gatewayIpAddr, err := ParseIpAddr(netIfConfig.GatewayIpAddr)
		if err != nil {
			return nil, err
		}
		netIf := &NetIf{
			Name:              netIfConfig.Name,
			MacAddr:           macAddr,
			IpAddr:            ipAddr,
			NetworkMask:       networkMask,
			GatewayIpAddr:     gatewayIpAddr,
			EthRxChan:         netIfConfig.EthRxChan,
			EthTxChan:         netIfConfig.EthTxChan,
			Engine:            r,
			ArpCacheTable:     make(map[uint32]uint64),
			ArpCacheTableLock: sync.RWMutex{},
			HandleUdp:         nil,
		}
		r.NetIfMap[netIf.Name] = netIf
	}

	protocol.SetRandIpHeaderId()

	return r, nil
}

func (e *Engine) RunEngine() {
	for _, netIf := range e.NetIfMap {
		go netIf.PacketHandle()
		go netIf.NetworkStateCheck()
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
		ethFrm := <-i.EthRxChan
		if i.Engine.DebugLog {
			fmt.Printf("rx pkt, eth frm len: %v, eth frm data: %v\n", len(ethFrm), ethFrm)
		}
		ethPayload, ethDstMac, ethSrcMac, ethProto, err := protocol.ParseEthFrm(ethFrm)
		if err != nil {
			fmt.Printf("parse ethernet frame error: %v\n", err)
			continue
		}
		if !bytes.Equal(ethDstMac, BROADCAST_MAC_ADDR) && !bytes.Equal(ethDstMac, i.MacAddr) {
			continue
		}
		switch ethProto {
		case protocol.ETH_PROTO_ARP:
			i.HandleArp(ethPayload, ethSrcMac)
		case protocol.ETH_PROTO_IPV4:
			i.RxIpv4(ethPayload)
		default:
		}
	}
}

func (i *NetIf) NetworkStateCheck() {
	ticker := time.NewTicker(time.Second * 1)
	seq := uint16(0)
	// 定时ping本地网关
	for {
		if i.Engine.Stop {
			break
		}
		<-ticker.C
		seq++
		i.TxIcmp(protocol.ICMP_DEFAULT_PAYLOAD, seq, i.GatewayIpAddr)
	}
}
