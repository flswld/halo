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
// 详见example/example.go

// DirectDpdk 直接使用dpdk收发网络报文
func DirectDpdk() {
	// 启动dpdk
	dpdk.Run(&dpdk.Config{
		DpdkCpuCoreList: []int{1, 2, 3, 4, 5, 6}, // dpdk使用的核心编号列表 主线程第一个核心 杂项线程第二个核心 每个网卡两个核心
		DpdkMemChanNum:  4,                       // dpdk内存通道数
		RingBufferSize:  128 * mem.MB,            // 环状缓冲区大小
		PortIdList:      []int{0, 1},             // 使用的网卡id列表
		AfPacketDevList: nil,                     // 使用的AF_PACKET虚拟网卡列表
		StatsLog:        true,                    // 收发包统计日志
		DebugLog:        false,                   // 收发包调试日志
		IdleSleep:       false,                   // 空闲睡眠 降低cpu占用
		SingleCore:      false,                   // 单核模式 只使用cpu0
	})

	// 通过EthRxPkt和EthTxPkt方法发送接收原始以太网报文
	var exit atomic.Bool
	go func() {
		cpu.BindCpuCore(7)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthRxPkt(0)
			if pkt == nil {
				continue
			}
			dpdk.EthTxPkt(1, pkt)
		}
	}()
	go func() {
		cpu.BindCpuCore(8)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthRxPkt(1)
			if pkt == nil {
				continue
			}
			dpdk.EthTxPkt(0, pkt)
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
		RingBufferSize:  128 * mem.MB,
		PortIdList:      []int{0, 1},
		AfPacketDevList: []string{"eth0", "wlan0"},
		StatsLog:        true,
		DebugLog:        false,
		IdleSleep:       true,
		SingleCore:      true,
	})

	// 初始化协议栈
	engine.DefaultLogWriter = new(logger.LogWriter)
	e, err := engine.InitEngine(&engine.Config{
		DebugLog: false, // 调试日志
		// 网卡列表
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",                 // 网卡名
				MacAddr:     "AA:AA:AA:AA:AA:AA",    // mac地址
				IpAddr:      "192.168.100.100",      // ip地址
				NetworkMask: "255.255.255.0",        // 子网掩码
				NatEnable:   true,                   // 网络地址转换
				NatType:     engine.NatTypeFullCone, // 网络地址转换类型
				// 网络地址转换端口映射表
				NatPortMappingTable: []*engine.NatPortMappingEntryConfig{
					{
						WanIpAddr:     "192.168.100.100",      // wan口ip地址
						WanPort:       22,                     // wan口端口
						LanHostIpAddr: "192.168.111.222",      // lan口主机ip地址
						LanHostPort:   22,                     // lan口主机端口
						Ipv4HeadProto: protocol.IPH_PROTO_TCP, // ip头部协议
					},
				},
				EthRxFunc:   func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // 网卡收包方法
				EthTxFunc:   func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // 网卡发包方法
				BindCpuCore: 0,                                               // 绑定的cpu核心
			},
			{
				Name:        "wan1",
				MacAddr:     "AA:AA:AA:AA:AA:BB",
				IpAddr:      "192.168.99.99",
				NetworkMask: "255.255.255.0",
				NatEnable:   true,
				NatType:     engine.NatTypeFullCone,
				EthRxFunc: func() (pkt []byte) {
					return dpdk.EthRxPkt(1)
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.EthTxPkt(1, pkt)
				},
				BindCpuCore: 0,
			},
			{
				Name:        "lan0",
				MacAddr:     "AA:AA:AA:AA:AA:CC",
				IpAddr:      "192.168.111.111",
				NetworkMask: "255.255.255.0",
				EthRxFunc: func() (pkt []byte) {
					return dpdk.KniRxPkt()
				},
				EthTxFunc: func(pkt []byte) {
					dpdk.KniTxPkt(pkt)
				},
				BindCpuCore: 0,
			},
		},
		// 路由表
		RoutingTable: []*engine.RoutingEntryConfig{
			{
				DstIpAddr:   "0.0.0.0",       // 目的ip地址
				NetworkMask: "0.0.0.0",       // 网络掩码
				NextHop:     "192.168.100.1", // 下一跳
				NetIf:       "wan0",          // 出接口
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

```

### TODO

- [X] 简易ARP+IPV4+ICMP协议栈
- [X] KCP协议栈
- [X] 多网卡支持
- [X] 路由转发功能
- [X] NAT功能
- [ ] 网卡多队列支持
- [ ] DHCP功能
