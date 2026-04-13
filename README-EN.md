<p align="center"><a href="https://github.com/flswld/halo" target="_blank"><img src="docs/logo.png"></a></p>

<p align="center">
<a href="https://pkg.go.dev/github.com/flswld/halo"><img src="https://pkg.go.dev/badge/github.com/flswld/halo" alt="GoDoc"></a>
<a href="https://goreportcard.com/report/github.com/flswld/halo"><img src="https://goreportcard.com/badge/github.com/flswld/halo" alt="Go Report Card"></a>
<a href="https://github.com/flswld/halo/blob/main/LICENSE"><img src="https://img.shields.io/github/license/flswld/halo" alt="License"></a>
</p>

Translations: [English](README-EN.md) | [Simplified Chinese](README.md)

# halo

High-performance lightweight Golang network packet send/receive framework

### Features

* Packet transmission performance per single NIC queue can exceed 10 Mpps
* Complete router protocol stack implementation
* Cross-platform out-of-the-box utility packages: `logger (logging)`, `cpu (goroutine CPU binding / spinlock)`, `mem (memory allocator / ring buffer)`, `hashmap/list (custom memory allocator)`

## Environment setup

### Want to try it quickly on Windows/macOS? Npcap/libpcap driver support has been added! Go install Wireshark.

```go
// UsePcapDev Use a pcap device
func UsePcapDev() {
	logger.InitLogger(nil)
	defer logger.CloseLogger()

	// Start pcap device
	devList, err := pcap.FindAllDevs()
	if err != nil {
		panic(err)
	}
	devName := ""
	for _, dev := range devList {
		if dev.Description == "Realtek PCIe GbE Family Controller" {
			devName = dev.Name
		}
	}
	if devName == "" {
		panic("dev not found")
	}
	pcapHandle, err := pcap.OpenLive(devName, 65536, true, pcap.BlockForever)
	if err != nil {
		panic(err)
	}
	selfMacAddr, _ := protocol.ParseMacAddr("AA:AA:AA:AA:AA:AA")
	devRxFunc := func() (pkt []byte) {
		data, ci, err := pcapHandle.ReadPacketData()
		_ = ci
		if err != nil {
			logger.Error("pcap handle read packet error: %v", err)
			return nil
		}
		if bytes.Equal(data[6:12], selfMacAddr) {
			return nil
		}
		return data
	}
	devTxFunc := func(pkt []byte) {
		err := pcapHandle.WritePacketData(pkt)
		if err != nil {
			logger.Error("pcap handle write packet error: %v", err)
			return
		}
	}

	// Initialize router
	protocol.CheckSumEnable = true
	r, err := engine.InitRouter(&engine.RouterConfig{
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "eth0",
				MacAddr:     "AA:AA:AA:AA:AA:AA",
				IpAddr:      "192.168.100.100",
				NetworkMask: "255.255.255.0",
				EthRxFunc:   devRxFunc,
				EthTxFunc:   devTxFunc,
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

	// Start router
	r.RunRouter()

	r.GetNetIf("eth0").Ping([]byte{192, 168, 100, 1}, 3)

	// Stop router
	r.StopRouter()

	// Close pcap device
	pcapHandle.Close()
}

```

### Linux DPDK environment setup

```shell
# Ubuntu 20.04 is recommended

# Install DPDK
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
# Check the PCIe device ID of the NIC to bind
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

## How to use

```shell
go get github.com/flswld/halo

```

### Code examples

```go
// DirectDpdk Directly use DPDK to send/receive network packets
func DirectDpdk() {
	// Start DPDK
	dpdk.Run(&dpdk.Config{
		DpdkCpuCoreList: []int{0, 1, 2, 3, 4, 5, 6, 7, 8}, // List of CPU core IDs used by DPDK. The main thread uses the first core; each NIC queue uses one core
		DpdkMemChanNum:  4,                                // Number of DPDK memory channels
		PortIdList:      []int{0, 1},                      // List of NIC port IDs to use
		QueueNum:        2,                                // Number of NIC queues to enable
		RingBufferSize:  128 * mem.MB,                     // Ring buffer size
		AfPacketDevList: nil,                              // List of af_packet virtual NICs to use
		StatsLog:        true,                             // Packet statistics logging
		DebugLog:        false,                            // Packet debug logging
		IdleSleep:       false,                            // Idle sleep to reduce CPU usage
		SingleCore:      false,                            // Single-core mode, use only CPU 0
		KniEnable:       false,                            // Enable KNI kernel NIC
	})

	// Use EthQueueRxPkt and EthQueueTxPkt methods to receive and transmit raw Ethernet frames
	var exit atomic.Bool
	forward := func(core int, rxPort int, txPort int, queue int) {
		cpu.BindCpuCore(core)
		for {
			if exit.Load() {
				break
			}
			pkt := dpdk.EthQueueRxPkt(rxPort, queue)
			if pkt == nil {
				continue
			}
			dpdk.EthQueueTxPkt(txPort, queue, pkt)
		}
	}
	go forward(9, 0, 1, 0)
	go forward(10, 0, 1, 1)
	go forward(11, 1, 0, 0)
	go forward(12, 1, 0, 1)
	time.Sleep(time.Minute)
	exit.Store(true)
	time.Sleep(time.Second)

	// Stop DPDK
	dpdk.Exit()
}

// EthernetRouter Ethernet router
func EthernetRouter() {
	logger.InitLogger(nil)
	defer logger.CloseLogger()

	// Start DPDK
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
		DebugLog: false, // debug logging
		// Network interface list
		NetIfList: []*engine.NetIfConfig{
			{
				Name:        "wan0",                 // Interface name
				MacAddr:     "AA:AA:AA:AA:AA:AA",    // MAC address
				IpAddr:      "192.168.100.100",      // IP address
				NetworkMask: "255.255.255.0",        // Network mask
				NatEnable:   true,                   // Enable network address translation (NAT)
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
				EthRxFunc:        func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // NIC receive function
				EthTxFunc:        func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // NIC transmit function
				BindCpuCore:      0,                                               // Bound CPU core
				StaticMemSize:    8 * mem.MB,                                      // Static memory size
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
				NetIf:       "wan0",            // Outgoing interface
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

	// Stop DPDK
	dpdk.Exit()
}

// EthernetSwitch Ethernet switch
func EthernetSwitch() {
	// Start DPDK
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
				EthRxFunc:   func() (pkt []byte) { return dpdk.EthRxPkt(0) }, // Port receive function
				EthTxFunc:   func(pkt []byte) { dpdk.EthTxPkt(0, pkt) },      // Port transmit function
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
		StaticMemSize: 8 * mem.MB, // Static memory size
	})
	if err != nil {
		panic(err)
	}

	// Start switch
	s.RunSwitch()

	time.Sleep(time.Minute)

	// Stop switch
	s.StopSwitch()

	// Stop DPDK
	dpdk.Exit()
}

```

## TODO

- [X] Simple ARP + IPv4 + ICMP protocol stack
- [X] KCP protocol stack
- [X] Multi-NIC support
- [X] Routing forwarding functionality
- [X] NAT functionality
- [X] NIC multi-queue support
- [X] DHCP functionality
- [ ] PPPoE functionality
- [ ] IPv6 support
- [ ] Lightweight TCP protocol stack support

## Star History

<a href="https://www.star-history.com/?repos=flswld%2Fhalo&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/image?repos=flswld/halo&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/image?repos=flswld/halo&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/image?repos=flswld/halo&type=date&legend=top-left" />
 </picture>
</a>