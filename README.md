# halo

## Golang高性能轻量级网络包收发框架

* 使用环状内存缓冲区避免频繁CGO调用
* 网卡单个队列发包性能可达3Mpps
* 路由模式下提供针对IP报文的自由抓包/丢包/改包/发包功能

### dpdk环境搭建

```shell
# 建议使用Ubuntu18.04或Ubuntu20.04

# 安装numactl
apt install libnuma-dev

# 安装dpdk-18.11.11
cd /root
wget https://fast.dpdk.org/rel/dpdk-18.11.11.tar.xz
tar -xvf dpdk-18.11.11.tar.xz
cd dpdk-stable-18.11.11
# 添加环境变量
export RTE_SDK="/root/dpdk-stable-18.11.11"
export RTE_TARGET="x86_64-native-linuxapp-gcc"
# 编译DPDK
make config T=$RTE_TARGET
make -j 8 install T=$RTE_TARGET

# 配置dpdk

# UIO和VFIO二选一

# UIO
modprobe uio
insmod $RTE_SDK/$RTE_TARGET/kmod/igb_uio.ko
ifconfig eth0 down
$RTE_SDK/usertools/dpdk-devbind.py --bind=igb_uio eth0

# VFIO Aliyun适用 参考链接:https://help.aliyun.com/document_detail/310880.htm
vim /etc/default/grub
# "GRUB_CMDLINE_LINUX" 追加 "intel_iommu=on"
update-grub2
reboot
modprobe vfio && modprobe vfio-pci
echo 1 >/sys/module/vfio/parameters/enable_unsafe_noiommu_mode
# 查看要绑定网卡的pcie设备号
$RTE_SDK/usertools/dpdk-devbind.py --status
ifconfig eth0 down
$RTE_SDK/usertools/dpdk-devbind.py -b vfio-pci 0000:00:05.0

# KNI
insmod $RTE_SDK/$RTE_TARGET/kmod/rte_kni.ko carrier=on

# 内存大页
echo 1024 >/sys/devices/system/node/node0/hugepages/hugepages-2048kB/nr_hugepages
mkdir -p /mnt/huge_2M
mount -t hugetlbfs none /mnt/huge_2M -o pagesize=2M
echo 0 >/sys/devices/system/node/node0/hugepages/hugepages-1048576kB/nr_hugepages
mkdir -p /mnt/huge_1G
mount -t hugetlbfs none /mnt/huge_1G -o pagesize=1G

```

### 如何使用

```shell
go get github.com/flswld/halo
# 确保CGO开启并添加以下环境变量
export CGO_CFLAGS="-I$RTE_SDK/$RTE_TARGET/include"
export CGO_LDFLAGS="-L$RTE_SDK/$RTE_TARGET/lib"
export CGO_CFLAGS_ALLOW=.*
export CGO_LDFLAGS_ALLOW=.*

```

### 使用示例

```go
// 详见example

package example

import (
	"time"

	"github.com/flswld/halo/dpdk"
	"github.com/flswld/halo/engine"
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
				Name:        "eth0",              // 网卡名
				MacAddr:     "AA:AA:AA:AA:AA:AA", // mac地址
				IpAddr:      "192.168.100.100",   // ip地址
				NetworkMask: "255.255.255.0",     // 子网掩码
				NatEnable:   false,               // 网络地址转换
				EthRxChan:   dpdk.Rx(0),          // 物理层接收管道
				EthTxChan:   dpdk.Tx(0),          // 物理层发送管道
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

```

### TODO

- [X] 简易ARP+IPV4+ICMP协议栈
- [X] KCP协议栈
- [X] 多网卡支持
- [X] 路由转发功能
- [X] NAT功能
- [ ] 网卡多队列支持
- [ ] DHCP功能
