# halo

## Golang High-Performance Lightweight Network Packet Transmission Framework

* Single NIC queue transmission performance can exceed 10Mpps
* Complete Router Protocol Stack Implementation
* Cross-platform ready-to-use toolkit: `logger`, `cpu (goroutine core binding/spinlock)`, `mem (memory allocator/ring buffer)`, `hashmap/list (custom memory allocator)`

### dpdk Environment Setup

```shell
# Recommended: Ubuntu 20.04

# Install dpdk
cd /root
wget https://fast.dpdk.org/rel/dpdk-20.11.10.tar.gz
tar -zxvf dpdk-20.11.10.tar.gz
cd dpdk-stable-20.11.10
# Add environment variable
export RTE_SDK="/root/dpdk-stable-20.11.10"
# Build DPDK
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
# Append "intel_iommu=on" to "GRUB_CMDLINE_LINUX"
update-grub2
reboot
modprobe vfio && modprobe vfio-pci
echo 1 >/sys/module/vfio/parameters/enable_unsafe_noiommu_mode
# View PCIe device ID of the NIC to bind
$RTE_SDK/usertools/dpdk-devbind.py --status
ifconfig eth0 down
$RTE_SDK/usertools/dpdk-devbind.py -b vfio-pci 0000:00:05.0

# KNI
insmod $RTE_SDK/build/kernel/linux/kni/rte_kni.ko carrier=on

# Hugepages
echo 1024 >/sys/devices/system/node/node0/hugepages/hugepages-2048kB/nr_hugepages
mkdir -p /mnt/huge_2M
mount -t hugetlbfs none /mnt/huge_2M -o pagesize=2M
echo 0 >/sys/devices/system/node/node0/hugepages/hugepages-1048576kB/nr_hugepages
mkdir -p /mnt/huge_1G
mount -t hugetlbfs none /mnt/huge_1G -o pagesize=1G

```

### How to Use

```shell
go get github.com/flswld/halo

```

### Usage Examples

```go
// See example/example.go

// DirectDpdk directly sends and receives network packets using dpdk
func DirectDpdk() {
	// Start dpdk
	dpdk.Run(&dpdk.Config{
		DpdkCpuCoreList: []int{0, 1, 2, 3, 4, 5, 6, 7, 8}, // List of CPU cores used by dpdk. First core for the main thread, one core per NIC queue.
		DpdkMemChanNum:  4,                                // Number of dpdk memory channels
		PortIdList:      []int{0, 1},                      // List of NIC IDs to use
		QueueNum:        2,                                // Number of NIC queues enabled
		RingBufferSize:  128 * mem.MB,                     // Ring buffer size
		AfPacketDevList: nil,                              // List of af_packet virtual NICs
		StatsLog:        true,                             // Packet send/receive statistics log
		DebugLog:        false,                            // Packet send/receive debug log
		IdleSleep:       false,                            // Idle sleep to reduce CPU usage
		SingleCore:      false,                            // Single-core mode, only use CPU0
		KniEnable:       false,                            // Enable kni kernel NIC
	})

	// Send and receive raw Ethernet packets via EthQueueRxPkt and EthQueueTxPkt
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

	// Stop dpdk
	dpdk.Exit()
}

// EthernetRouter
func EthernetRouter() {
	logger.InitLogger(nil)
	defer logger.CloseLogger()

	// Start dpdk
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

	// Initialize router
	engine.DefaultLogWriter = new(logger.LogWriter)
	r, err := engine.InitRouter(&engine.RouterConfig{
		DebugLog: false, // Debug log
		// NIC list
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",                 // NIC name
				MacAddr:     "AA:AA:AA:AA:AA:AA",    // MAC address
				IpAddr:      "192.168.100.100",      // IP address
				NetworkMask: "255.255.255.0",        // Subnet mask
				NatEnable:   true,                   // Enable NAT
				NatType:     engine.NatTypeFullCone, // NAT type
				// NAT port mapping table
				NatPortMappingList: []*engine.NatPortMappingEntryConfig{
					{
						WanPort:       22,                     // WAN port
						LanHostIpAddr: "192.168.111.222",      // LAN host IP address
						LanHostPort:   22,                     // LAN host port
						Ipv4HeadProto: protocol.IPH_PROTO_TCP, // IP header protocol
					},
				},
				DnsServerAddr:    "",                                              // DNS server address
				DhcpServerEnable: false,                                           // Enable DHCP server
				DhcpClientEnable: false,                                           // Enable DHCP client
				EthRxFunc:        func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // NIC receive method
				EthTxFunc:        func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // NIC send method
				BindCpuCore:      0,                                               // Bound CPU core
				StaticHeapSize:   8 * mem.MB,                                      // Static heap memory size
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
		// Static route list
		RouteList: []*engine.RouteEntryConfig{
			{
				DstIpAddr:   "114.114.114.114", // Destination IP address
				NetworkMask: "255.255.255.255", // Network mask
				NextHop:     "192.168.100.1",   // Next hop
				NetIf:       "wan0",            // Outbound interface
			},
		},
	})
	if err != nil {
		panic(err)
	}

	// Start router
	r.RunRouter()

	r.Ipv4PktFwdHook = func(raw []byte, dir int) (drop bool, mod []byte) {
		payload, _, srcAddr, dstAddr, err := protocol.ParseIpv4Pkt(raw)
		if err == nil {
			logger.Debug("[IPV4 ROUTE FWD] src: %v -> dst: %v, len: %v", srcAddr, dstAddr, len(payload))
		}
		return false, raw
	}

	time.Sleep(time.Minute)

	// Stop router
	r.StopRouter()

	// Stop dpdk
	dpdk.Exit()
}

// EthernetSwitch
func EthernetSwitch() {
	// Start dpdk
	dpdk.Run(&dpdk.Config{
		PortIdList: []int{0, 1, 2, 3},
		StatsLog:   true,
		IdleSleep:  true,
		SingleCore: true,
	})

	// Initialize switch
	s, err := engine.InitSwitch(&engine.SwitchConfig{
		// Port list
		SwitchPortList: []*engine.SwitchPortConfig{
			{
				Name:        "port0",                                         // Port name
				EthRxFunc:   func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // Port receive method
				EthTxFunc:   func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // Port send method
				VlanId:      1,                                               // VLAN ID
				BindCpuCore: 0,                                               // Bound CPU core
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
		StaticHeapSize: 8 * mem.MB, // Static heap memory size
	})
	if err != nil {
		panic(err)
	}

	// Start switch
	s.RunSwitch()

	time.Sleep(time.Minute)

	// Stop switch
	s.StopSwitch()

	// Stop dpdk
	dpdk.Exit()
}

```

### TODO

- [X] Simple ARP+IPv4+ICMP Protocol Stack
- [X] KCP Protocol Stack
- [X] Multi-NIC Support
- [X] Routing Forwarding Feature
- [X] NAT Feature
- [X] Multi-queue NIC Support
- [X] DHCP Feature
- [ ] PPPOE Feature
- [ ] IPv6 Support
