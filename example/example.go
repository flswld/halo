package example

import (
	"fmt"
	"time"

	"github.com/flswld/halo/dpdk"
	"github.com/flswld/halo/engine"
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
		GolangCpuCoreList: []int{5, 6},
		StatsLog:          true,
		DpdkCpuCoreList:   []int{1, 2, 3, 4},
		DpdkMemChanNum:    4,
		PortIdList:        []int{0},
		RingBufferSize:    1024 * 1024 * 128,
	})

	// 初始化协议栈
	e, err := engine.InitEngine(&engine.Config{
		DebugLog: false, // 调试日志
		// 网卡列表
		NetIfConfigList: []*engine.NetIfConfig{
			{
				Name:          "eth0",              // 网卡名
				MacAddr:       "AA:AA:AA:AA:AA:AA", // mac地址
				IpAddr:        "192.168.100.100",   // ip地址
				NetworkMask:   "255.255.255.0",     // 子网掩码
				GatewayIpAddr: "192.168.100.1",     // 网关ip地址
				EthRxChan:     dpdk.Rx(0),          // 物理层接收管道
				EthTxChan:     dpdk.Tx(0),          // 物理层发送管道
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// 启动协议栈
	e.RunEngine()

	// kcp协议栈测试
	e.GetNetIf("eth0").HandleUdp = kcp.UdpRx
	kcp.UdpTx = e.GetNetIf("eth0").TxUdp
	go kcpServer()
	time.Sleep(time.Second)
	go kcpClient()
	time.Sleep(time.Minute)

	// 停止协议栈
	e.StopEngine()

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
		NetIfConfigList: []*engine.NetIfConfig{
			{
				Name:          "eth0",
				MacAddr:       "AA:AA:AA:AA:AA:AA",
				IpAddr:        "192.168.100.100",
				NetworkMask:   "255.255.255.0",
				GatewayIpAddr: "192.168.100.1",
				EthRxChan:     dpdk.Rx(0),
				EthTxChan:     dpdk.Tx(0),
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

func kcpServer() {
	listener, err := kcp.ListenWithOptions("0.0.0.0:22222")
	if err != nil {
		return
	}
	for {
		enetNotify := <-listener.EnetNotify
		if enetNotify.ConnType == kcp.ConnEnetSyn {
			listener.SendEnetNotifyToPeer(&kcp.Enet{
				Addr:      enetNotify.Addr,
				SessionId: 1,
				Conv:      1,
				ConnType:  kcp.ConnEnetEst,
				EnetType:  enetNotify.EnetType,
			})
			break
		}
	}
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
		fmt.Printf("recv kcp data: %v\n", buf)
		_, err = conn.Write([]byte{0x01, 0x23, 0xcd, 0xef})
		if err != nil {
			break
		}
	}
	_ = conn.Close()
	conn.SendEnetNotifyToPeer(&kcp.Enet{
		Addr:      conn.RemoteAddr().String(),
		SessionId: conn.GetSessionId(),
		Conv:      conn.GetConv(),
		ConnType:  kcp.ConnEnetFin,
		EnetType:  kcp.EnetClientClose,
	})
}

func kcpClient() {
	conn, err := kcp.DialWithOptions("192.168.100.100:22222", "0.0.0.0:30000")
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
		fmt.Printf("recv kcp data: %v\n", buf)
	}
	_ = conn.Close()
	conn.SendEnetNotifyToPeer(&kcp.Enet{
		Addr:      conn.RemoteAddr().String(),
		SessionId: conn.GetSessionId(),
		Conv:      conn.GetConv(),
		ConnType:  kcp.ConnEnetFin,
		EnetType:  kcp.EnetClientClose,
	})
}
