# halo

## Golang高性能轻量级网络包收发框架

* 使用环状内存缓冲区避免频繁CGO调用
* 网卡单个队列发包性能可达3Mpps

### dpdk环境搭建

```shell
# 建议使用Ubuntu18.04或Ubuntu20.04

# 安装numactl
# https://github.com/numactl/numactl
unzip numactl-master.zip
cd numactl-master
./autogen.sh
./configure
make -j 8
make install
ldconfig

# 安装dpdk-18.11.11
# https://github.com/DPDK/dpdk
tar -xvf dpdk-18.11.11.tar.xz
cd dpdk-stable-18.11.11
# 环境变量
export RTE_SDK="/root/dpdk-stable-18.11.11/"
export RTE_TARGET="x86_64-native-linuxapp-gcc"
# 编译DPDK
make config T=$RTE_TARGET
make -j 8 install T=$RTE_TARGET

# 配置dpdk

# UIO和VFIO二选一

# UIO
modprobe uio
insmod /root/dpdk-stable-18.11.11/x86_64-native-linuxapp-gcc/kmod/igb_uio.ko
/root/dpdk-stable-18.11.11/usertools/dpdk-devbind.py --bind=igb_uio eth0

# VFIO Aliyun适用
# https://help.aliyun.com/document_detail/310880.htm
cat /proc/cmdline
vim /etc/default/grub
# "GRUB_CMDLINE_LINUX" 追加 "intel_iommu=on"
update-grub2
reboot
# 检查是否开启成功
cat /proc/cmdline
modprobe vfio && modprobe vfio-pci
echo 1 >/sys/module/vfio/parameters/enable_unsafe_noiommu_mode
ethtool -i eth0
# vfio-pci后的参数即为ethtool所查看的网卡的pcie设备号
/root/dpdk-stable-18.11.11/usertools/dpdk-devbind.py -b vfio-pci 0000:00:05.0

# KNI
insmod /root/dpdk-stable-18.11.11/x86_64-native-linuxapp-gcc/kmod/rte_kni.ko carrier=on

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
	e, _ := engine.InitEngine(&engine.Config{
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

```

### TODO

- [X] 简易ARP+IPV4+ICMP协议栈
- [X] KCP协议栈
- [X] 多网卡支持
- [ ] 路由转发功能
- [ ] NAT功能
- [ ] 网卡多队列支持
