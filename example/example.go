package example

import (
	"sync/atomic"
	"time"

	"github.com/flswld/halo/cpu"
	"github.com/flswld/halo/dpdk"
	"github.com/flswld/halo/engine"
	"github.com/flswld/halo/logger"
	"github.com/flswld/halo/mem"
	"github.com/flswld/halo/protocol"
	"github.com/flswld/halo/protocol/kcp"
)

// DirectDpdk 直接使用dpdk收发网络报文
func DirectDpdk() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		DpdkCpuCoreList: []int{0, 1, 2, 3, 4, 5, 6, 7, 8}, // dpdk使用的核心编号列表 主线程第一个核心 每个网卡队列一个核心
		DpdkMemChanNum:  4,                                // dpdk内存通道数
		PortIdList:      []int{0, 1},                      // 使用的网卡id列表
		QueueNum:        2,                                // 启用的网卡队列数
		RingBufferSize:  128 * mem.MB,                     // 环状缓冲区大小
		AfPacketDevList: nil,                              // 使用的af_packet虚拟网卡列表
		StatsLog:        true,                             // 收发包统计日志
		DebugLog:        false,                            // 收发包调试日志
		IdleSleep:       false,                            // 空闲睡眠 降低cpu占用
		SingleCore:      false,                            // 单核模式 只使用cpu0
		KniEnable:       false,                            // 开启kni内核网卡
	})

	// 通过EthQueueRxPkt和EthQueueTxPkt方法发送接收原始以太网报文
	var exit atomic.Bool
	go func() {
		cpu.BindCpuCore(9)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthQueueRxPkt(0, 0)
			if pkt == nil {
				continue
			}
			dpdk.EthQueueTxPkt(1, 0, pkt)
		}
	}()
	go func() {
		cpu.BindCpuCore(10)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthQueueRxPkt(0, 1)
			if pkt == nil {
				continue
			}
			dpdk.EthQueueTxPkt(1, 1, pkt)
		}
	}()
	go func() {
		cpu.BindCpuCore(11)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthQueueRxPkt(1, 0)
			if pkt == nil {
				continue
			}
			dpdk.EthQueueTxPkt(0, 0, pkt)
		}
	}()
	go func() {
		cpu.BindCpuCore(12)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthQueueRxPkt(1, 1)
			if pkt == nil {
				continue
			}
			dpdk.EthQueueTxPkt(0, 1, pkt)
		}
	}()
	time.Sleep(time.Minute)
	exit.Store(true)
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
		DpdkCpuCoreList: nil,
		DpdkMemChanNum:  1,
		PortIdList:      []int{0, 1},
		QueueNum:        1,
		RingBufferSize:  128 * mem.MB,
		AfPacketDevList: []string{"eth0", "wlan0"},
		StatsLog:        true,
		DebugLog:        false,
		IdleSleep:       true,
		SingleCore:      true,
		KniEnable:       true,
	})

	// 初始化路由器
	engine.DefaultLogWriter = new(logger.LogWriter)
	r, err := engine.InitRouter(&engine.RouterConfig{
		DebugLog: false, // 调试日志
		// 网卡列表
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",                 // 网卡名
				MacAddr:     "AA:AA:AA:AA:AA:AA",    // mac地址
				IpAddr:      "192.168.100.100",      // ip地址
				NetworkMask: "255.255.255.0",        // 子网掩码
				NatEnable:   true,                   // 开启网络地址转换
				NatType:     engine.NatTypeFullCone, // 网络地址转换类型
				// 网络地址转换端口映射表
				NatPortMappingList: []*engine.NatPortMappingEntryConfig{
					{
						WanPort:       22,                     // wan口端口
						LanHostIpAddr: "192.168.111.222",      // lan口主机ip地址
						LanHostPort:   22,                     // lan口主机端口
						Ipv4HeadProto: protocol.IPH_PROTO_TCP, // ip头部协议
					},
				},
				DnsServerAddr:    "",                                              // dns服务器地址
				DhcpServerEnable: false,                                           // 开启dhcp服务器
				DhcpClientEnable: false,                                           // 开启dhcp客户端
				EthRxFunc:        func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // 网卡收包方法
				EthTxFunc:        func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // 网卡发包方法
				BindCpuCore:      0,                                               // 绑定的cpu核心
				StaticHeapSize:   8 * mem.MB,                                      // 静态堆内存大小
			},
			{
				Name:             "wan1",
				MacAddr:          "AA:AA:AA:AA:AA:BB",
				NatEnable:        true,
				NatType:          engine.NatTypeFullCone,
				DhcpClientEnable: true,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(1)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(1, pkt)
				},
			},
			{
				Name:             "lan0",
				MacAddr:          "AA:AA:AA:AA:AA:CC",
				IpAddr:           "192.168.111.1",
				NetworkMask:      "255.255.255.0",
				DnsServerAddr:    "223.5.5.5",
				DhcpServerEnable: true,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.KniRxPkt()
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.KniTxPkt(pkt)
				},
			},
		},
		// 静态路由列表
		RouteList: []*engine.RouteEntryConfig{
			{
				DstIpAddr:   "114.114.114.114", // 目的ip地址
				NetworkMask: "255.255.255.255", // 网络掩码
				NextHop:     "192.168.100.1",   // 下一跳
				NetIf:       "wan0",            // 出接口
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动路由器
	r.RunRouter()

	r.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
		payload, _, srcAddr, dstAddr, err := protocol.ParseIpv4Pkt(raw)
		if err == nil {
			logger.Debug("[IPV4 ROUTE FWD] src: %v -> dst: %v, len: %v", srcAddr, dstAddr, len(payload))
		}
		return false, raw
	}

	time.Sleep(time.Minute)

	// 停止路由器
	r.StopRouter()

	// 停止dpdk
	dpdk.Exit()
}

// EthernetSwitch 以太网交换机
func EthernetSwitch() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		PortIdList: []int{0, 1, 2, 3},
		StatsLog:   true,
		IdleSleep:  true,
		SingleCore: true,
	})

	// 初始化交换机
	s, err := engine.InitSwitch(&engine.SwitchConfig{
		// 端口列表
		SwitchPortList: []*engine.SwitchPortConfig{
			{
				Name:        "port0",                                         // 端口名
				EthRxFunc:   func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // 端口收包方法
				EthTxFunc:   func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // 端口发包方法
				VlanId:      1,                                               // vlan号
				BindCpuCore: 0,                                               // 绑定的cpu核心
			},
			{
				Name:      "port1",
				EthRxFunc: func() (pkt []byte) { return dpdk.EthRxPkt(1) },
				EthTxFunc: func(pkt []byte) { dpdk.EthTxPkt(1, pkt) },
				VlanId:    1,
			},
			{
				Name:      "port2",
				EthRxFunc: func() (pkt []byte) { return dpdk.EthRxPkt(2) },
				EthTxFunc: func(pkt []byte) { dpdk.EthTxPkt(2, pkt) },
				VlanId:    2,
			},
			{
				Name:      "port3",
				EthRxFunc: func() (pkt []byte) { return dpdk.EthRxPkt(3) },
				EthTxFunc: func(pkt []byte) { dpdk.EthTxPkt(3, pkt) },
				VlanId:    2,
			},
		},
		StaticHeapSize: 8 * mem.MB, // 静态堆内存大小
	})
	if err != nil {
		panic(err)
	}

	// 启动交换机
	s.RunSwitch()

	time.Sleep(time.Minute)

	// 停止交换机
	s.StopSwitch()

	// 停止dpdk
	dpdk.Exit()
}

// EthernetRouterSwitch 以太网路由交换机
func EthernetRouterSwitch() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		PortIdList: []int{0, 1, 2},
		StatsLog:   true,
		IdleSleep:  true,
		SingleCore: true,
	})

	wireS2R := engine.NewWire(true)
	wireR2S := engine.NewWire(true)

	// 初始化路由器
	r, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:             "wan0",
				MacAddr:          "AA:AA:AA:AA:AA:AA",
				NatEnable:        true,
				NatType:          engine.NatTypeFullCone,
				DhcpClientEnable: true,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(0)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(0, pkt)
				},
			},
			{
				Name:             "lan0",
				MacAddr:          "AA:AA:AA:AA:AA:BB",
				IpAddr:           "192.168.111.1",
				NetworkMask:      "255.255.255.0",
				DhcpServerEnable: true,
				EthRxFunc:        wireS2R.Rx,
				EthTxFunc:        wireR2S.Tx,
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 初始化交换机
	s, err := engine.InitSwitch(&engine.SwitchConfig{
		SwitchPortList: []*engine.SwitchPortConfig{
			{
				Name:      "switch_port_lan",
				EthRxFunc: wireR2S.Rx,
				EthTxFunc: wireS2R.Tx,
				VlanId:    1,
			},
			{
				Name: "switch_port_1",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(1)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(1, pkt)
				},
				VlanId: 1,
			},
			{
				Name: "switch_port_2",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(2)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(2, pkt)
				},
				VlanId: 1,
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动路由器
	r.RunRouter()
	// 启动交换机
	s.RunSwitch()

	time.Sleep(time.Minute)

	// 停止路由器
	r.StopRouter()
	// 停止交换机
	s.StopSwitch()

	wireS2R.Destroy()
	wireR2S.Destroy()

	// 停止dpdk
	dpdk.Exit()
}

// DDoS 压力测试
func DDoS() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		DpdkCpuCoreList: []int{0, 1, 2, 3, 4},
		DpdkMemChanNum:  2,
		PortIdList:      []int{0},
		QueueNum:        2,
		StatsLog:        true,
	})

	var icmpReqPkt []byte = nil

	// 初始化路由器
	r, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(0)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(0, pkt)
					if len(pkt) == 74 {
						icmpReqPkt = pkt
					}
				},
				BindCpuCore: 5,
			},
		},
		RouteList: []*engine.RouteEntryConfig{
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

	// 启动路由器
	r.RunRouter()

	// 一分钟icmp洪水攻击
	for {
		ok := r.GetNetIf("eth0").TxIcmp(protocol.ICMP_DEFAULT_PAYLOAD, protocol.ICMP_REQUEST, []byte{0x00, 0x01}, 1, []byte{192, 168, 100, 1})
		if ok {
			break
		}
		time.Sleep(time.Second)
	}
	var exit atomic.Bool
	go func() {
		cpu.BindCpuCore(6)
		for {
			if exit.Load() {
				break
			}
			dpdk.EthQueueTxPkt(0, 0, icmpReqPkt)
		}
	}()
	go func() {
		cpu.BindCpuCore(7)
		for {
			if exit.Load() {
				break
			}
			dpdk.EthQueueTxPkt(0, 1, icmpReqPkt)
		}
	}()
	time.Sleep(time.Minute)
	exit.Store(true)
	time.Sleep(time.Second)

	// 停止路由器
	r.StopRouter()

	// 停止dpdk
	dpdk.Exit()
}

// KcpServerClient KCP协议栈
func KcpServerClient() {
	logger.InitLogger(nil)
	defer logger.CloseLogger()

	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		PortIdList: []int{0, 1},
		StatsLog:   true,
		IdleSleep:  true,
		SingleCore: true,
	})

	// 初始化路由器
	r1, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(0)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(0, pkt)
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	r2, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:BB",
				IpAddr:      "192.168.100.200",
				NetworkMask: "255.255.255.0",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(1)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(1, pkt)
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动路由器
	r1.RunRouter()
	r2.RunRouter()

	r2.GetNetIf("eth0").Ping([]byte{192, 168, 100, 100}, 1)

	kcpServer := func(netIf *engine.NetIf) {
		rxChan := make(chan kcp.ChanConnMsg, 1024)
		txChan := make(chan kcp.ChanConnMsg, 1024)
		netIf.RecvUdp(22222, func(session engine.UdpSession, payload []byte) {
			pkt := make([]byte, len(payload))
			copy(pkt, payload)
			rxChan <- kcp.ChanConnMsg{
				Addr: kcp.ChanConnAddr{
					Ip:   session.RemoteIp,
					Port: session.RemotePort,
				},
				Pkt: pkt,
			}
		})
		go func() {
			for {
				msg, ok := <-txChan
				if !ok {
					break
				}
				netIf.SendUdp(22222, engine.UdpSession{
					RemoteIp:   msg.Addr.Ip,
					RemotePort: msg.Addr.Port,
				}, msg.Pkt)
			}
		}()
		listener, err := kcp.ListenChanConn(&kcp.ChanConn{
			RxChan: rxChan,
			TxChan: txChan,
			Addr: kcp.ChanConnAddr{
				Ip:   protocol.IpAddrToU([]byte{192, 168, 100, 100}),
				Port: 22222,
			},
		})
		if err != nil {
			return
		}
		conn, err := listener.AcceptKCP()
		if err != nil {
			return
		}
		conn.SetACKNoDelay(true)
		conn.SetWriteDelay(false)
		conn.SetWindowSize(256, 256)
		conn.SetMtu(1200)
		conn.SetNoDelay(1, 20, 1, 1)
		for {
			buf := make([]byte, 1472)
			size, err := conn.Read(buf)
			if err != nil {
				break
			}
			buf = buf[:size]
			logger.Debug("kcp server recv data: %02x", buf)
			_, err = conn.Write([]byte{0x01, 0x23, 0xcd, 0xef})
			if err != nil {
				break
			}
		}
		_ = conn.Close()
	}

	kcpClient := func(netIf *engine.NetIf) {
		rxChan := make(chan kcp.ChanConnMsg, 1024)
		txChan := make(chan kcp.ChanConnMsg, 1024)
		netIf.RecvUdp(33333, func(session engine.UdpSession, payload []byte) {
			pkt := make([]byte, len(payload))
			copy(pkt, payload)
			rxChan <- kcp.ChanConnMsg{
				Addr: kcp.ChanConnAddr{
					Ip:   session.RemoteIp,
					Port: session.RemotePort,
				},
				Pkt: pkt,
			}
		})
		go func() {
			for {
				msg, ok := <-txChan
				if !ok {
					break
				}
				netIf.SendUdp(33333, engine.UdpSession{
					RemoteIp:   msg.Addr.Ip,
					RemotePort: msg.Addr.Port,
				}, msg.Pkt)
			}
		}()
		conn, err := kcp.DialChanConn(&kcp.ChanConn{
			RxChan: rxChan,
			TxChan: txChan,
			Addr: kcp.ChanConnAddr{
				Ip:   protocol.IpAddrToU([]byte{192, 168, 100, 200}),
				Port: 33333,
			},
		}, kcp.ChanConnAddr{
			Ip:   protocol.IpAddrToU([]byte{192, 168, 100, 100}),
			Port: 22222,
		})
		if err != nil {
			return
		}
		conn.SetACKNoDelay(true)
		conn.SetWriteDelay(false)
		conn.SetWindowSize(256, 256)
		conn.SetMtu(1200)
		conn.SetNoDelay(1, 20, 1, 1)
		for i := 0; i < 30; i++ {
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
			logger.Debug("kcp client recv data: %02x", buf)
		}
		_ = conn.Close()
	}

	// kcp协议栈测试
	go kcpServer(r1.GetNetIf("eth0"))
	time.Sleep(time.Second)
	go kcpClient(r2.GetNetIf("eth0"))
	time.Sleep(time.Minute)

	// 停止路由器
	r1.StopRouter()
	r2.StopRouter()

	// 停止dpdk
	dpdk.Exit()
}

// MagicPacketModifier 魔法改包器
func MagicPacketModifier() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		PortIdList: []int{0, 1},
		StatsLog:   true,
		IdleSleep:  true,
		SingleCore: true,
	})

	// 初始化路由器
	r, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:             "wan0",
				MacAddr:          "AA:AA:AA:AA:AA:AA",
				NatEnable:        true,
				NatType:          engine.NatTypeFullCone,
				DhcpClientEnable: true,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(0)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(0, pkt)
				},
			},
			{
				Name:             "lan0",
				MacAddr:          "AA:AA:AA:AA:AA:BB",
				IpAddr:           "192.168.111.1",
				NetworkMask:      "255.255.255.0",
				DhcpServerEnable: true,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(1)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(1, pkt)
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动路由器
	r.RunRouter()

	r.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
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
					r.GetNetIf("wan0").SendUdpPktByFlow(engine.NatFlowHash{
						RemoteIpAddr:  protocol.IpAddrToU(srcAddr),
						RemotePort:    srcPort,
						LanHostIpAddr: protocol.IpAddrToU(dstAddr),
						LanHostPort:   dstPort,
						Ipv4HeadProto: protocol.IPH_PROTO_UDP,
					}, engine.WanToLan, udpPayload[:len(udpPayload)/2])
				}()
			}
			return false, raw
		}
	}

	time.Sleep(time.Minute)

	// 停止路由器
	r.StopRouter()

	// 停止dpdk
	dpdk.Exit()
}
