# halo

## Golang高性能轻量级网络包收发框架

* 网卡单个队列发包性能可超过10Mpps
* 完整的路由器协议栈实现
* 全平台开箱即用的工具包：`logger(日志)`、`cpu(协程绑核/自旋锁)`、`mem(内存分配器/环状缓冲区)`、`hashmap/list(自定义内存分配器)`

### dpdk环境搭建

```shell
# 建议使用Ubuntu20.04

# 安装dpdk
cd /root
wget https://fast.dpdk.org/rel/dpdk-20.11.10.tar.gz
tar -zxvf dpdk-20.11.10.tar.gz
cd dpdk-stable-20.11.10
# 添加环境变量
export RTE_SDK="/root/dpdk-stable-20.11.10"
# 编译DPDK
meson build
cd build
meson configure -Denable_kmods=true
ninja
ninja install
ldconfig

# UIO
modprobe uio
insmod $RTE_SDK/build/kernel/linux/igb_uio/igb_uio.ko
ifconfig eth0 down
$RTE_SDK/usertools/dpdk-devbind.py --bind=igb_uio eth0

# VFIO
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
insmod $RTE_SDK/build/kernel/linux/kni/rte_kni.ko carrier=on

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

```

### 使用示例

```go
// 详见example/example.go

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
				NatEnable:   true,                   // 开启网络地址转换
				NatType:     engine.NatTypeFullCone, // 网络地址转换类型
				// 网络地址转换端口映射表
				NatPortMappingTable: []*engine.NatPortMappingEntryConfig{
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
				BindCpuCore:      -1,                                              // 绑定的cpu核心
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
				BindCpuCore: -1,
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
				BindCpuCore: -1,
			},
		},
		// 路由表
		RoutingTable: []*engine.RoutingEntryConfig{
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

	// 启动协议栈
	e.RunEngine()

	e.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
		payload, _, srcAddr, dstAddr, err := protocol.ParseIpv4Pkt(raw)
		if err == nil {
			logger.Debug("[IPV4 ROUTE FWD] src: %v -> dst: %v, len: %v", srcAddr, dstAddr, len(payload))
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
- [X] 网卡多队列支持
- [X] DHCP功能
- [ ] PPPOE功能
- [ ] IPV6支持
