package example

import (
	"log"
	"time"

	"github.com/flswld/halo/dpdk"
	"github.com/flswld/halo/engine"
	"github.com/flswld/halo/logger"
	"github.com/flswld/halo/protocol"
	"github.com/flswld/halo/protocol/kcp"
)

// DirectDpdk 直接使用dpdk收发网络报文
func DirectDpdk() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{5, 6},       // golang侧使用的核心编号列表 每个网口两个核心
		StatsLog:          true,              // 收发包统计日志
		DpdkCpuCoreList:   []int{1, 2, 3, 4}, // dpdk侧使用的核心编号列表 主线程第一个核心 杂项线程第二个核心 每个网口两个核心
		DpdkMemChanNum:    4,                 // dpdk内存通道数
		PortIdList:        []int{0},          // 使用网口id列表
		RingBufferSize:    1024 * 1024 * 128, // 环状缓冲区大小
		DebugLog:          false,             // 收发包调试日志
		IdleSleep:         false,             // 空闲睡眠 降低cpu占用
		SingleCore:        false,             // 单核模式 物理单核机器需要开启
		KniBypass:         false,             // kni旁路目标ip 只接收来自目标ip的包 其他的包全部送到kni网卡
		KniBypassTargetIp: "",                // kni旁路目标ip地址
	})

	// 通过RX和TX管道发送接收原始以太网报文
	pkt := <-dpdk.Rx(0)
	dpdk.Tx(0) <- pkt
	time.Sleep(time.Second)

	// 停止dpdk
	dpdk.Exit()
}

// NetworkEngine 简易网络协议栈
func NetworkEngine() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{7, 8, 9, 10},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4, 5, 6},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0, 1},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 初始化协议栈
	e1, err := engine.InitEngine(&engine.Config{
		DebugLog: false, // 调试日志
		// 网卡列表
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",                 // 网卡名
				MacAddr:     "AA:AA:AA:AA:AA:AA",    // mac地址
				IpAddr:      "192.168.100.100",      // ip地址
				NetworkMask: "255.255.255.0",        // 子网掩码
				NatEnable:   false,                  // 网络地址转换
				NatType:     engine.NatTypeFullCone, // 网络地址转换类型
				EthRxChan:   dpdk.Rx(0),             // 物理层接收管道
				EthTxChan:   dpdk.Tx(0),             // 物理层发送管道
			},
		},
		// 路由表
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",       // 目的ip地址
				NetworkMask: "0.0.0.0",       // 网络掩码
				NextHop:     "192.168.100.1", // 下一跳
				NetIf:       "eth0",          // 出接口
			},
		},
	})
	if err != nil {
		panic(err)
	}
	e2, err := engine.InitEngine(&engine.Config{
		DebugLog: false,
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:BB",
				IpAddr:      "192.168.111.111",
				NetworkMask: "255.255.255.0",
				EthRxChan:   dpdk.Rx(1),
				EthTxChan:   dpdk.Tx(1),
			},
		},
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",
				NetworkMask: "0.0.0.0",
				NextHop:     "192.168.111.1",
				NetIf:       "eth0",
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动协议栈
	e1.RunEngine()
	e2.RunEngine()

	// kcp协议栈测试
	go kcpServer(e1.GetNetIf("eth0"))
	time.Sleep(time.Second)
	go kcpClient(e2.GetNetIf("eth0"))
	time.Sleep(time.Minute)

	// 停止协议栈
	e1.StopEngine()
	e2.StopEngine()

	// 停止dpdk
	dpdk.Exit()
}

// EthernetHub 以太网集线器
func EthernetHub() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{7, 8, 9, 10},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4, 5, 6},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0, 1},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 转发
	exit := false
	go func() {
		dpdk.BindCpuCore(11)
		for {
			if exit {
				break
			}
			pkt := <-dpdk.Rx(0)
			if pkt == nil {
				continue
			}
			dpdk.Tx(1) <- pkt
		}
	}()
	go func() {
		dpdk.BindCpuCore(12)
		for {
			if exit {
				break
			}
			pkt := <-dpdk.Rx(1)
			if pkt == nil {
				continue
			}
			dpdk.Tx(0) <- pkt
		}
	}()
	time.Sleep(time.Minute)
	exit = true
	time.Sleep(time.Second)

	// 停止dpdk
	dpdk.Exit()
}

// EthernetRouter 以太网路由器
func EthernetRouter() {
	logger.InitLogger(nil)
	defer logger.CloseLogger()

	// 启动dpdk
	dpdk.DefaultLogWriter = new(logger.LogWriter)
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{9, 10, 11, 12, 13, 14},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4, 5, 6, 7, 8},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0, 1, 2},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 初始化协议栈
	engine.DefaultLogWriter = new(logger.LogWriter)
	e, err := engine.InitEngine(&engine.Config{
		DebugLog: false,
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				NatEnable:   true,
				NatType:     engine.NatTypeFullCone,
				NatPortMappingTable: []*engine.NatPortMappingEntryConfig{
					{
						WanIpAddr:     "192.168.100.100",
						WanPort:       22,
						LanHostIpAddr: "192.168.111.222",
						LanHostPort:   22,
					},
				},
				EthRxChan: dpdk.Rx(0),
				EthTxChan: dpdk.Tx(0),
			},
			{
				Name:        "wan1",
				MacAddr:     "AA:AA:AA:AA:AA:BB",
				IpAddr:      "192.168.99.99",
				NetworkMask: "255.255.255.0",
				NatEnable:   true,
				NatType:     engine.NatTypeFullCone,
				EthRxChan:   dpdk.Rx(1),
				EthTxChan:   dpdk.Tx(1),
			},
			{
				Name:        "lan0",
				MacAddr:     "AA:AA:AA:AA:AA:CC",
				IpAddr:      "192.168.111.111",
				NetworkMask: "255.255.255.0",
				EthRxChan:   dpdk.Rx(2),
				EthTxChan:   dpdk.Tx(2),
			},
		},
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",
				NetworkMask: "0.0.0.0",
				NextHop:     "192.168.100.1",
				NetIf:       "wan0",
			},
			{
				DstIpAddr:   "114.114.114.114",
				NetworkMask: "255.255.255.255",
				NextHop:     "192.168.99.1",
				NetIf:       "wan1",
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动协议栈
	e.RunEngine()

	e.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
		payload, _, srcAddr, dstAddr, err := protocol.ParseIpv4Pkt(raw)
		if err == nil {
			logger.Debug("[IPV4 ROUTE FWD] src: %v -> dst: %v, len: %v\n", srcAddr, dstAddr, len(payload))
		}
		return false, raw
	}

	time.Sleep(time.Minute)

	// 停止协议栈
	e.StopEngine()

	// 停止dpdk
	dpdk.Exit()
}

// DDoS 压力测试
func DDoS() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{5, 6},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 初始化协议栈
	e, err := engine.InitEngine(&engine.Config{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				EthRxChan:   dpdk.Rx(0),
				EthTxChan:   dpdk.Tx(0),
			},
		},
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",
				NetworkMask: "0.0.0.0",
				NextHop:     "192.168.100.1",
				NetIf:       "eth0",
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动协议栈
	e.RunEngine()

	// 一分钟icmp洪水攻击
	var pkt []byte = nil
	for {
		pkt = e.GetNetIf("eth0").TxIcmp(protocol.ICMP_DEFAULT_PAYLOAD, 1, []byte{192, 168, 100, 1})
		if pkt != nil {
			break
		}
		time.Sleep(time.Second)
	}
	exit := false
	go func() {
		dpdk.BindCpuCore(7)
		for {
			if exit {
				break
			}
			dpdk.Tx(0) <- pkt
		}
	}()
	time.Sleep(time.Minute)
	exit = true
	time.Sleep(time.Second)

	// 停止协议栈
	e.StopEngine()

	// 停止dpdk
	dpdk.Exit()
}

// MagicPacketModifier 魔法改包器
func MagicPacketModifier() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		GolangCpuCoreList: []int{7, 8, 9, 10},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4, 5, 6},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0, 1},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 初始化协议栈
	e, err := engine.InitEngine(&engine.Config{
		DebugLog: false,
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				NatEnable:   true,
				NatType:     engine.NatTypeFullCone,
				EthRxChan:   dpdk.Rx(0),
				EthTxChan:   dpdk.Tx(0),
			},
			{
				Name:        "lan0",
				MacAddr:     "AA:AA:AA:AA:AA:BB",
				IpAddr:      "192.168.111.111",
				NetworkMask: "255.255.255.0",
				EthRxChan:   dpdk.Rx(1),
				EthTxChan:   dpdk.Tx(1),
			},
		},
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",
				NetworkMask: "0.0.0.0",
				NextHop:     "192.168.100.1",
				NetIf:       "wan0",
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动协议栈
	e.RunEngine()

	e.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
		// 数据包监听回调
		ipv4Payload, ipHeadProto, srcAddr, dstAddr, err := protocol.ParseIpv4Pkt(raw)
		if err != nil {
			return false, raw
		}
		// 只对UDP包加魔法
		if ipHeadProto != protocol.IPH_PROTO_UDP {
			return false, raw
		}
		if len(raw) > 1000 {
			// 超过1000的包直接丢掉
			return true, nil
		} else if len(raw) > 500 {
			// 500-1000的包末尾大部分字节改为0x00
			mod = make([]byte, len(raw))
			for i := len(mod) - 1; i > 100; i-- {
				mod[i] = 0x00
			}
			mod = protocol.ReCalcIpv4CheckSum(mod)
			mod = protocol.ReCalcUdpCheckSum(mod)
			return false, mod
		} else {
			// 500以下的包
			if dir == engine.WanToLan {
				// 对于服务器下行包复制一份延迟一秒后再裁剪一半数据发给客户端
				udpPayload, srcPort, dstPort, err := protocol.ParseUdpPkt(ipv4Payload, srcAddr, dstAddr)
				if err != nil {
					return false, raw
				}
				go func() {
					time.Sleep(time.Second)
					e.GetNetIf("wan0").SendUdpPktByFlow(engine.NatFlowHash{
						RemoteIpAddr:  protocol.IpAddrToU(srcAddr),
						RemotePort:    srcPort,
						LanHostIpAddr: protocol.IpAddrToU(dstAddr),
						LanHostPort:   dstPort,
					}, engine.WanToLan, udpPayload[:len(udpPayload)/2])
				}()
			}
			return false, raw
		}
	}

	time.Sleep(time.Minute)

	// 停止协议栈
	e.StopEngine()

	// 停止dpdk
	dpdk.Exit()
}

func kcpServer(netIf *engine.NetIf) {
	rxChan := make(chan []byte, 1024)
	txChan := make(chan []byte, 1024)
	netIf.HandleUdp = func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte) {
		if udpDstPort != 22222 {
			return
		}
		rxChan <- udpPayload
	}
	go func() {
		for {
			pkt, ok := <-txChan
			if !ok {
				break
			}
			for {
				if netIf.TxUdp(pkt, 22222, 33333, []byte{192, 168, 111, 111}) != nil {
					break
				}
				time.Sleep(time.Second)
			}
		}
	}()
	listener, err := kcp.Listen(&kcp.Conn{
		RxChan: rxChan,
		TxChan: txChan,
	})
	if err != nil {
		return
	}
	enetNotify := <-listener.GetEnetNotifyChan()
	if enetNotify.ConnType != kcp.ConnEnetSyn {
		return
	}
	listener.SendEnetNotifyToPeer(&kcp.Enet{
		SessionId: 1,
		Conv:      1,
		ConnType:  kcp.ConnEnetEst,
		EnetType:  enetNotify.EnetType,
	})
	conn, err := listener.AcceptKCP()
	if err != nil {
		return
	}
	for i := 0; i < 30; i++ {
		buf := make([]byte, 1472)
		size, err := conn.Read(buf)
		if err != nil {
			break
		}
		buf = buf[:size]
		log.Printf("kcp server recv data: %02x\n", buf)
		_, err = conn.Write([]byte{0x01, 0x23, 0xcd, 0xef})
		if err != nil {
			break
		}
	}
	listener.SendEnetNotifyToPeer(&kcp.Enet{
		SessionId: conn.GetSessionId(),
		Conv:      conn.GetConv(),
		ConnType:  kcp.ConnEnetFin,
		EnetType:  kcp.EnetClientClose,
	})
	netIf.HandleUdp = nil
	_ = conn.Close()
}

func kcpClient(netIf *engine.NetIf) {
	rxChan := make(chan []byte, 1024)
	txChan := make(chan []byte, 1024)
	netIf.HandleUdp = func(udpPayload []byte, udpSrcPort uint16, udpDstPort uint16, ipv4SrcAddr []byte) {
		if udpDstPort != 33333 {
			return
		}
		rxChan <- udpPayload
	}
	go func() {
		for {
			pkt, ok := <-txChan
			if !ok {
				break
			}
			for {
				if netIf.TxUdp(pkt, 33333, 22222, []byte{192, 168, 100, 100}) != nil {
					break
				}
				time.Sleep(time.Second)
			}
		}
	}()
	conn, err := kcp.Dial(&kcp.Conn{
		RxChan: rxChan,
		TxChan: txChan,
	})
	if err != nil {
		return
	}
	for {
		time.Sleep(time.Second)
		_, err = conn.Write([]byte{0x45, 0x67, 0x89, 0xab})
		if err != nil {
			break
		}
		buf := make([]byte, 1472)
		size, err := conn.Read(buf)
		if err != nil {
			break
		}
		buf = buf[:size]
		log.Printf("kcp client recv data: %02x\n", buf)
	}
	netIf.HandleUdp = nil
	_ = conn.Close()
}
