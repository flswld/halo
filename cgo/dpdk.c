const char *APP_VERSION = "1.1.1";

#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>
#include <string.h>
#include <stdatomic.h>

#include <unistd.h>

#include <rte_eal.h>
#include <rte_common.h>
#include <rte_ethdev.h>
#include <rte_lcore.h>
#include <rte_log.h>
#include <rte_mbuf.h>
#include <rte_kni.h>
#include <rte_malloc.h>

#include "ring_buffer.h"

#define NUM_MBUFS 8191
#define MBUF_CACHE_SIZE 250
#define RX_RING_SIZE 1024
#define TX_RING_SIZE 1024
#define BURST_SIZE 32
#define MAX_PACKET_SIZE 1514
#define RTE_LOGTYPE_APP RTE_LOGTYPE_USER1

// dpdk_config 保存 Go 层传入的 DPDK 运行参数
struct dpdk_config {
    int eal_argc; // EAL 参数数量
    char **eal_argv; // EAL 参数列表
    int dpdk_cpu_core_num; // DPDK 使用的 CPU 核心数量
    int *dpdk_cpu_core_list; // DPDK 使用的 CPU 核心编号列表
    int port_id_num; // 网卡端口 ID 数量
    int *port_id_list; // 使用的网卡端口 ID 列表
    int queue_num; // 每个网卡端口的队列数量
    int ring_buffer_size; // 环形缓冲区数据区字节大小
    bool debug_log; // 是否输出调试日志
    bool idle_sleep; // 空闲时是否睡眠
    bool single_core; // 是否使用单核模式
    bool kni_enable; // 是否启用 KNI
    bool rx_checksum; // 是否启用接收硬件校验和
    bool tx_checksum; // 是否启用发送硬件校验和
    bool tx_only; // 是否仅启动网卡发包工作线程
};

// ring_buffer 保存一组 DPDK 收发环形缓冲区
struct ring_buffer {
    void *send_ring_mem; // 发送环形缓冲区底层内存
    void *recv_ring_mem; // 接收环形缓冲区底层内存
    ring_buffer_t *send_ring_buffer; // 发送环形缓冲区
    ring_buffer_t *recv_ring_buffer; // 接收环形缓冲区
    ring_buffer_consumer_t send_consumer; // Go 到 DPDK 的独占消费者
    ring_buffer_producer_t recv_producer; // DPDK 到 Go 的独占生产者
};

// lcore_arg 保存工作核心处理的网卡端口和队列索引
struct lcore_arg {
    int port_index; // 网卡端口配置索引
    int queue_id; // 网卡队列 ID
};

_Atomic
bool running = false;

struct dpdk_config global_config = {0};
struct rte_eth_conf *port_conf = NULL;
struct ring_buffer *port_ring_buffer = NULL;
struct ring_buffer kni_ring_buffer = {0};
struct rte_mempool *mbuf_pool = NULL;
static struct rte_kni *kni = NULL;

// cgo_get_stats 获取指定网卡端口的累计统计信息
int cgo_get_stats(const int port_index, struct rte_eth_stats *stats) {
    if (!stats || port_index < 0 || port_index >= global_config.port_id_num) {
        return -1;
    }
    memset(stats, 0x00, sizeof(struct rte_eth_stats));
    return rte_eth_stats_get(global_config.port_id_list[port_index], stats);
}

// cgo_port_send_ring_buffer 获取网卡队列的发送环形缓冲区
ring_buffer_t *cgo_port_send_ring_buffer(const int port_index, const int queue_id) {
    return port_ring_buffer[port_index * global_config.queue_num + queue_id].send_ring_buffer;
}

// cgo_port_recv_ring_buffer 获取网卡队列的接收环形缓冲区
ring_buffer_t *cgo_port_recv_ring_buffer(const int port_index, const int queue_id) {
    return port_ring_buffer[port_index * global_config.queue_num + queue_id].recv_ring_buffer;
}

// cgo_kni_send_ring_buffer 获取 KNI 发送环形缓冲区
ring_buffer_t *cgo_kni_send_ring_buffer() {
    return kni_ring_buffer.send_ring_buffer;
}

// cgo_kni_recv_ring_buffer 获取 KNI 接收环形缓冲区
ring_buffer_t *cgo_kni_recv_ring_buffer() {
    return kni_ring_buffer.recv_ring_buffer;
}

// cgo_exit_signal_handler 停止数据面并释放 DPDK 资源
void cgo_exit_signal_handler(void) {
    RTE_LOG(INFO, APP, "exit signal received, exit...\n");
    atomic_store(&running, false);
    // 先等待所有工作核心停止访问共享对象 再释放网卡和环形缓冲区
    rte_eal_mp_wait_lcore();
    if (global_config.kni_enable) {
        if (rte_kni_release(kni)) {
            RTE_LOG(ERR, APP, "fail to release kni\n");
        }
        ring_buffer_destroy(kni_ring_buffer.send_ring_buffer);
        ring_buffer_destroy(kni_ring_buffer.recv_ring_buffer);
        rte_free(kni_ring_buffer.send_ring_mem);
        rte_free(kni_ring_buffer.recv_ring_mem);
    }
    for (int port_index = 0; port_index < global_config.port_id_num; port_index++) {
        const uint16_t port_id = global_config.port_id_list[port_index];
        rte_eth_dev_stop(port_id);
        rte_eth_dev_close(port_id);
        for (int queue_id = 0; queue_id < global_config.queue_num; queue_id++) {
            const int i = port_index * global_config.queue_num + queue_id;
            ring_buffer_destroy(port_ring_buffer[i].send_ring_buffer);
            ring_buffer_destroy(port_ring_buffer[i].recv_ring_buffer);
            rte_free(port_ring_buffer[i].send_ring_mem);
            rte_free(port_ring_buffer[i].recv_ring_mem);
        }
    }
    memset(&global_config, 0x00, sizeof(struct dpdk_config));
    free(port_conf);
    port_conf = NULL;
    rte_free(port_ring_buffer);
    port_ring_buffer = NULL;
    memset(&kni_ring_buffer, 0x00, sizeof(struct ring_buffer));
    mbuf_pool = NULL;
    kni = NULL;
}

// cgo_kni_handle 处理 KNI 控制请求
void cgo_kni_handle(void) {
    rte_kni_handle_request(kni);
}

// port_init 配置并启动指定 DPDK 网卡端口
int port_init(const int port_index, const uint16_t port_id, const uint16_t queue_num) {
    // 配置设备
    struct rte_eth_dev_info dev_info;
    int ret = rte_eth_dev_info_get(port_id, &dev_info);
    if (ret != 0) {
        RTE_LOG(ERR, APP, "rte_eth_dev_info_get failed\n");
        return ret;
    }
    port_conf[port_index].rxmode.max_rx_pkt_len = 1518;
    // 按应用配置启用设备明确声明支持的接收校验和卸载能力
    if (global_config.rx_checksum) {
        if (dev_info.rx_offload_capa & DEV_RX_OFFLOAD_IPV4_CKSUM) {
            port_conf[port_index].rxmode.offloads |= DEV_RX_OFFLOAD_IPV4_CKSUM;
        }
        if (dev_info.rx_offload_capa & DEV_RX_OFFLOAD_TCP_CKSUM) {
            port_conf[port_index].rxmode.offloads |= DEV_RX_OFFLOAD_TCP_CKSUM;
        }
        if (dev_info.rx_offload_capa & DEV_RX_OFFLOAD_UDP_CKSUM) {
            port_conf[port_index].rxmode.offloads |= DEV_RX_OFFLOAD_UDP_CKSUM;
        }
    }
    if (dev_info.rx_offload_capa & DEV_RX_OFFLOAD_RSS_HASH) {
        port_conf[port_index].rxmode.offloads |= DEV_RX_OFFLOAD_RSS_HASH;
        port_conf[port_index].rxmode.mq_mode = ETH_MQ_RX_RSS;
        port_conf[port_index].rx_adv_conf.rss_conf.rss_hf = dev_info.flow_type_rss_offloads;
    }
    // 按应用配置启用设备明确声明支持的发送校验和卸载能力
    if (global_config.tx_checksum) {
        if (dev_info.tx_offload_capa & DEV_TX_OFFLOAD_IPV4_CKSUM) {
            port_conf[port_index].txmode.offloads |= DEV_TX_OFFLOAD_IPV4_CKSUM;
        }
        if (dev_info.tx_offload_capa & DEV_TX_OFFLOAD_TCP_CKSUM) {
            port_conf[port_index].txmode.offloads |= DEV_TX_OFFLOAD_TCP_CKSUM;
        }
        if (dev_info.tx_offload_capa & DEV_TX_OFFLOAD_UDP_CKSUM) {
            port_conf[port_index].txmode.offloads |= DEV_TX_OFFLOAD_UDP_CKSUM;
        }
    }
    RTE_LOG(INFO, APP, "port init, port_id: %u, queue_num: %u, rx_offload_capa: %lu, tx_offload_capa: %lu, rx_offloads: %lu, tx_offloads: %lu\n",
            port_id, queue_num, dev_info.rx_offload_capa, dev_info.tx_offload_capa,
            port_conf[port_index].rxmode.offloads, port_conf[port_index].txmode.offloads);
    ret = rte_eth_dev_configure(port_id, queue_num, queue_num, port_conf + port_index);
    if (ret != 0) {
        RTE_LOG(ERR, APP, "rte_eth_dev_configure failed\n");
        return ret;
    }
    uint16_t nb_rx_desc = RX_RING_SIZE;
    uint16_t nb_tx_desc = TX_RING_SIZE;
    rte_eth_dev_adjust_nb_rx_tx_desc(port_id, &nb_rx_desc, &nb_tx_desc);
    // 配置收包队列
    for (uint16_t queue_id = 0; queue_id < queue_num; queue_id++) {
        ret = rte_eth_rx_queue_setup(port_id, queue_id, nb_rx_desc, rte_eth_dev_socket_id(port_id), NULL, mbuf_pool);
        if (ret < 0) {
            RTE_LOG(ERR, APP, "rte_eth_rx_queue_setup failed\n");
            return ret;
        }
    }
    // 配置发包队列
    for (uint16_t queue_id = 0; queue_id < queue_num; queue_id++) {
        ret = rte_eth_tx_queue_setup(port_id, queue_id, nb_tx_desc, rte_eth_dev_socket_id(port_id), NULL);
        if (ret < 0) {
            RTE_LOG(ERR, APP, "rte_eth_tx_queue_setup failed\n");
            return ret;
        }
    }
    // 启动设备
    ret = rte_eth_dev_start(port_id);
    if (ret < 0) {
        RTE_LOG(ERR, APP, "rte_eth_dev_start failed\n");
        return ret;
    }
    // 开启混杂模式
    rte_eth_promiscuous_enable(port_id);
    return 0;
}

// kni_change_mtu 处理 KNI 修改 MTU 的回调
static int kni_change_mtu(const uint16_t port_id, const unsigned int new_mtu) {
    RTE_LOG(INFO, APP, "kni change mtu of port: %u, mtu: %u\n", port_id, new_mtu);
    return 0;
}

// kni_config_network_if 处理 KNI 网络接口状态变更回调
static int kni_config_network_if(const uint16_t port_id, const uint8_t if_up) {
    RTE_LOG(INFO, APP, "kni config network if of port: %u if_up: %d\n", port_id, if_up);
    return 0;
}

// kni_config_mac_address 处理 KNI MAC 地址变更回调
static int kni_config_mac_address(const uint16_t port_id, uint8_t mac_addr[]) {
    char mac[64];
    sprintf(mac, "%02X:%02X:%02X:%02X:%02X:%02X", mac_addr[0], mac_addr[1], mac_addr[2], mac_addr[3], mac_addr[4], mac_addr[5]);
    RTE_LOG(INFO, APP, "kni config mac address of port: %u, mac: %s\n", port_id, mac);
    return 0;
}

// kni_config_promiscusity 处理 KNI 混杂模式变更回调
static int kni_config_promiscusity(const uint16_t port_id, const uint8_t to_on) {
    RTE_LOG(INFO, APP, "kni config promiscusity of port: %u to_on: %d\n", port_id, to_on);
    return 0;
}

// kni_init 初始化 KNI 虚拟网卡
int kni_init() {
    rte_kni_init(1);
    struct rte_kni_conf conf = {0};
    sprintf(conf.name, "ethkni");
    conf.core_id = 0;
    conf.group_id = 0;
    conf.mbuf_size = 2048;
    conf.force_bind = 0;
    conf.mac_addr[0] = 0x65;
    conf.mac_addr[1] = 0x74;
    conf.mac_addr[2] = 0x68;
    conf.mac_addr[3] = 0x6b;
    conf.mac_addr[4] = 0x6e;
    conf.mac_addr[5] = 0x69;
    conf.mtu = 1500;
    struct rte_kni_ops ops = {0};
    ops.port_id = 0;
    ops.change_mtu = kni_change_mtu;
    ops.config_network_if = kni_config_network_if;
    ops.config_mac_address = kni_config_mac_address;
    ops.config_promiscusity = kni_config_promiscusity;
    kni = rte_kni_alloc(mbuf_pool, &conf, &ops);
    if (!kni) {
        rte_exit(EXIT_FAILURE, "fail to create kni\n");
    }
    return 0;
}

// eth_rx 从网卡队列批量接收报文并写入环形缓冲区
static bool eth_rx(const int port_index, const uint16_t port_id, const uint16_t queue_id) {
    // 接收多个网卡数据帧
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_eth_rx_burst(port_id, queue_id, mbuf_recv, BURST_SIZE);
    if (unlikely(nb_rx == 0)) {
        return false;
    }
    // 环状缓冲区数据发送
    for (int i = 0; i < nb_rx; i++) {
        const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
        const uint16_t recv_len = mbuf_recv[i]->data_len;
        if (unlikely(recv_len > MAX_PACKET_SIZE)) {
            rte_pktmbuf_free(mbuf_recv[i]);
            continue;
        }
        ring_buffer_producer_write_packet(&port_ring_buffer[port_index * global_config.queue_num + queue_id].recv_producer, recv_data, recv_len);
        // 打印网卡接收到的原始数据
        if (unlikely(global_config.debug_log)) {
            printf("[nic recv], port_index: %d, len: %d, data: ", port_index, mbuf_recv[i]->data_len);
            for (int j = 0; j < mbuf_recv[i]->data_len; j++) {
                printf("%02x", recv_data[j]);
            }
            printf("\n\n");
        }
        rte_pktmbuf_free(mbuf_recv[i]);
    }
    return true;
}

// eth_tx 从环形缓冲区读取报文并批量发送到网卡队列
static bool eth_tx(const int port_index, const uint16_t port_id, const uint16_t queue_id) {
    // 环状缓冲区数据接收
    struct rte_mbuf *mbuf_send[BURST_SIZE];
    int mbuf_send_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        mbuf_send[i] = rte_pktmbuf_alloc(mbuf_pool);
        if (unlikely(mbuf_send[i] == NULL)) {
            break;
        }
        uint8_t *send_data = rte_pktmbuf_mtod(mbuf_send[i], uint8_t *);
        uint32_t send_len = 0;
        const uint32_t send_capacity = rte_pktmbuf_tailroom(mbuf_send[i]);
        const bool ok = ring_buffer_consumer_read_packet(
            &port_ring_buffer[port_index * global_config.queue_num + queue_id].send_consumer,
            send_data, send_capacity, &send_len);
        if (unlikely(!ok)) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        // 未启用硬件卸载时保持标志为零 使 PMD 可以选择无卸载发送快路径
        mbuf_send[i]->ol_flags = 0;
        struct rte_ether_hdr *ether_hdr = rte_pktmbuf_mtod(mbuf_send[i], struct rte_ether_hdr *);
        // 校验和
        if (rte_be_to_cpu_16(ether_hdr->ether_type) == RTE_ETHER_TYPE_IPV4) {
            // 仅在实际启用发送卸载时设置分层长度和协议标志
            if (port_conf[port_index].txmode.offloads != 0) {
                mbuf_send[i]->l2_len = sizeof(struct rte_ether_hdr);
                mbuf_send[i]->l3_len = sizeof(struct rte_ipv4_hdr);
                mbuf_send[i]->ol_flags |= PKT_TX_IPV4;
            }
            struct rte_ipv4_hdr *ipv4_hdr = (struct rte_ipv4_hdr *) ((uint8_t *) ether_hdr + sizeof(struct rte_ether_hdr));
            ipv4_hdr->hdr_checksum = 0;
            if (port_conf[port_index].txmode.offloads & DEV_TX_OFFLOAD_IPV4_CKSUM) {
                mbuf_send[i]->ol_flags |= PKT_TX_IP_CKSUM;
            } else {
                ipv4_hdr->hdr_checksum = rte_ipv4_cksum(ipv4_hdr);
            }
            if (ipv4_hdr->next_proto_id == IPPROTO_UDP) {
                struct rte_udp_hdr *udp_hdr = (struct rte_udp_hdr *) ((uint8_t *) ipv4_hdr + sizeof(struct rte_ipv4_hdr));
                udp_hdr->dgram_cksum = 0;
                if (port_conf[port_index].txmode.offloads & DEV_TX_OFFLOAD_UDP_CKSUM) {
                    mbuf_send[i]->ol_flags |= PKT_TX_UDP_CKSUM;
                    udp_hdr->dgram_cksum = rte_ipv4_phdr_cksum(ipv4_hdr, mbuf_send[i]->ol_flags);
                } else {
                    udp_hdr->dgram_cksum = rte_ipv4_udptcp_cksum(ipv4_hdr, udp_hdr);
                }
            } else if (ipv4_hdr->next_proto_id == IPPROTO_TCP) {
                struct rte_tcp_hdr *tcp_hdr = (struct rte_tcp_hdr *) ((uint8_t *) ipv4_hdr + sizeof(struct rte_ipv4_hdr));
                tcp_hdr->cksum = 0;
                if (port_conf[port_index].txmode.offloads & DEV_TX_OFFLOAD_TCP_CKSUM) {
                    mbuf_send[i]->ol_flags |= PKT_TX_TCP_CKSUM;
                    tcp_hdr->cksum = rte_ipv4_phdr_cksum(ipv4_hdr, mbuf_send[i]->ol_flags);
                } else {
                    tcp_hdr->cksum = rte_ipv4_udptcp_cksum(ipv4_hdr, tcp_hdr);
                }
            }
        }
        mbuf_send[i]->pkt_len = send_len;
        mbuf_send[i]->data_len = (uint16_t) send_len;
        mbuf_send_size++;
        // 打印环状缓冲区数据
        if (unlikely(global_config.debug_log)) {
            printf("[ring recv], port_index: %d, len: %u, data: ", port_index, send_len);
            for (uint32_t j = 0; j < send_len; j++) {
                printf("%02x", send_data[j]);
            }
            printf("\n\n");
        }
    }
    if (unlikely(mbuf_send_size == 0)) {
        return false;
    }
    // 发送多个网卡数据帧
    const uint16_t nb_tx = rte_eth_tx_burst(port_id, queue_id, mbuf_send, mbuf_send_size);
    // 把没发送成功的mbuf释放掉
    if (unlikely(nb_tx < mbuf_send_size)) {
        for (int i = nb_tx; i < mbuf_send_size; i++) {
            rte_pktmbuf_free(mbuf_send[i]);
        }
    }
    return true;
}

// kni_rx 从 KNI 批量接收报文并写入环形缓冲区
static bool kni_rx() {
    // KNI数据接收
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_kni_rx_burst(kni, mbuf_recv, BURST_SIZE);
    if (nb_rx == 0) {
        return false;
    }
    // KNI环状缓冲区数据发送
    for (int i = 0; i < nb_rx; i++) {
        const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
        const uint16_t recv_len = mbuf_recv[i]->data_len;
        if (recv_len > MAX_PACKET_SIZE) {
            rte_pktmbuf_free(mbuf_recv[i]);
            continue;
        }
        ring_buffer_producer_write_packet(&kni_ring_buffer.recv_producer, recv_data, recv_len);
        // 打印KNI接收到的原始数据
        if (global_config.debug_log) {
            printf("[kni recv], len: %d, data: ", mbuf_recv[i]->data_len);
            for (int j = 0; j < mbuf_recv[i]->data_len; j++) {
                printf("%02x", recv_data[j]);
            }
            printf("\n\n");
        }
        rte_pktmbuf_free(mbuf_recv[i]);
    }
    return true;
}

// kni_tx 从环形缓冲区读取报文并批量发送到 KNI
static bool kni_tx() {
    // KNI环状缓冲区数据接收
    struct rte_mbuf *mbuf_send[BURST_SIZE];
    int mbuf_send_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        mbuf_send[i] = rte_pktmbuf_alloc(mbuf_pool);
        if (mbuf_send[i] == NULL) {
            break;
        }
        uint8_t *send_data = rte_pktmbuf_mtod(mbuf_send[i], uint8_t *);
        uint32_t send_len = 0;
        const uint32_t send_capacity = rte_pktmbuf_tailroom(mbuf_send[i]);
        const bool ok = ring_buffer_consumer_read_packet(
            &kni_ring_buffer.send_consumer, send_data, send_capacity, &send_len);
        if (!ok) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        struct rte_ether_hdr *ether_hdr = rte_pktmbuf_mtod(mbuf_send[i], struct rte_ether_hdr *);
        // 校验和
        if (rte_be_to_cpu_16(ether_hdr->ether_type) == RTE_ETHER_TYPE_IPV4) {
            struct rte_ipv4_hdr *ipv4_hdr = (struct rte_ipv4_hdr *) ((uint8_t *) ether_hdr + sizeof(struct rte_ether_hdr));
            ipv4_hdr->hdr_checksum = 0;
            ipv4_hdr->hdr_checksum = rte_ipv4_cksum(ipv4_hdr);
            if (ipv4_hdr->next_proto_id == IPPROTO_UDP) {
                struct rte_udp_hdr *udp_hdr = (struct rte_udp_hdr *) ((uint8_t *) ipv4_hdr + sizeof(struct rte_ipv4_hdr));
                udp_hdr->dgram_cksum = 0;
                udp_hdr->dgram_cksum = rte_ipv4_udptcp_cksum(ipv4_hdr, udp_hdr);
            } else if (ipv4_hdr->next_proto_id == IPPROTO_TCP) {
                struct rte_tcp_hdr *tcp_hdr = (struct rte_tcp_hdr *) ((uint8_t *) ipv4_hdr + sizeof(struct rte_ipv4_hdr));
                tcp_hdr->cksum = 0;
                tcp_hdr->cksum = rte_ipv4_udptcp_cksum(ipv4_hdr, tcp_hdr);
            }
        }
        mbuf_send[i]->pkt_len = send_len;
        mbuf_send[i]->data_len = (uint16_t) send_len;
        mbuf_send_size++;
        // 打印KNI环状缓冲区数据
        if (global_config.debug_log) {
            printf("[kni ring recv], len: %u, data: ", send_len);
            for (uint32_t j = 0; j < send_len; j++) {
                printf("%02x", send_data[j]);
            }
            printf("\n\n");
        }
    }
    if (mbuf_send_size == 0) {
        return false;
    }
    // KNI数据发送
    const uint16_t nb_tx = rte_kni_tx_burst(kni, mbuf_send, mbuf_send_size);
    // 把没发送成功的mbuf释放掉
    if (nb_tx < mbuf_send_size) {
        for (int i = nb_tx; i < mbuf_send_size; i++) {
            rte_pktmbuf_free(mbuf_send[i]);
        }
    }
    return true;
}

// lcore_rx 在工作核心上持续执行指定网卡队列的收包任务
int lcore_rx(void *arg_ptr) {
    const unsigned int lcore_id = rte_lcore_id();
    const struct lcore_arg *arg = arg_ptr;
    const int port_index = arg->port_index;
    const uint16_t port_id = global_config.port_id_list[port_index];
    const int queue_id = arg->queue_id;
    RTE_LOG(INFO, APP, "lcore_rx run in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    while (atomic_load(&running)) {
        const bool rx_pkt = eth_rx(port_index, port_id, queue_id);
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(!rx_pkt && global_config.idle_sleep)) {
            usleep(1000 * 10);
        }
    }
    RTE_LOG(INFO, APP, "lcore_rx exit in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    return 0;
}

// lcore_tx 在工作核心上持续执行指定网卡队列的发包任务
int lcore_tx(void *arg_ptr) {
    const unsigned int lcore_id = rte_lcore_id();
    const struct lcore_arg *arg = arg_ptr;
    const int port_index = arg->port_index;
    const uint16_t port_id = global_config.port_id_list[port_index];
    const int queue_id = arg->queue_id;
    RTE_LOG(INFO, APP, "lcore_tx run in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    while (atomic_load(&running)) {
        const bool tx_pkt = eth_tx(port_index, port_id, queue_id);
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(!tx_pkt && global_config.idle_sleep)) {
            usleep(1000 * 10);
        }
    }
    RTE_LOG(INFO, APP, "lcore_tx exit in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    return 0;
}

// lcore_rx_tx 在当前核心上按配置轮询处理网卡和 KNI 收发
int lcore_rx_tx(const bool handle_rx, const bool handle_tx, const bool handle_kni) {
    const unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_rx_tx run in lcore: %u\n", lcore_id);
    while (atomic_load(&running)) {
        bool no_pkt = true;
        if (handle_rx || handle_tx) {
            for (int port_index = 0; port_index < global_config.port_id_num; port_index++) {
                const uint16_t port_id = global_config.port_id_list[port_index];
                const bool rx_pkt = handle_rx && eth_rx(port_index, port_id, 0);
                const bool tx_pkt = handle_tx && eth_tx(port_index, port_id, 0);
                if (rx_pkt || tx_pkt) {
                    no_pkt = false;
                }
            }
        }
        if (handle_kni) {
            const bool kni_rx_pkt = kni_rx();
            const bool kni_tx_pkt = kni_tx();
            if (kni_rx_pkt || kni_tx_pkt) {
                no_pkt = false;
            }
        }
        // 主核心未承担数据面任务时必须休眠 避免无意义自旋争用同一物理核心
        if (no_pkt && (global_config.idle_sleep || (!handle_rx && !handle_tx && !handle_kni))) {
            usleep(1000 * 10);
        }
    }
    RTE_LOG(INFO, APP, "lcore_rx_tx exit in lcore: %u\n", lcore_id);
    return 0;
}

// cgo_dpdk_main 初始化 DPDK 资源并运行数据面
int cgo_dpdk_main(const struct dpdk_config *config) {
    printf("dpdk start, app version: %s\n", APP_VERSION);
    printf("eal argc: %d\n", config->eal_argc);
    printf("eal argv: ");
    for (int i = 0; i < config->eal_argc; i++) {
        printf("%s", config->eal_argv[i]);
        if (i < config->eal_argc - 1) {
            printf(" ");
        }
    }
    printf("\n");
    printf("cpu core num: %d\n", config->dpdk_cpu_core_num);
    printf("cpu core list: ");
    for (int i = 0; i < config->dpdk_cpu_core_num; i++) {
        printf("%d", config->dpdk_cpu_core_list[i]);
        if (i < config->dpdk_cpu_core_num - 1) {
            printf(" ");
        }
    }
    printf("\n");
    printf("port num: %d\n", config->port_id_num);
    printf("port list: ");
    for (int i = 0; i < config->port_id_num; i++) {
        printf("%d", config->port_id_list[i]);
        if (i < config->port_id_num - 1) {
            printf(" ");
        }
    }
    printf("\n");
    printf("queue num: %d\n", config->queue_num);
    printf("ring buffer size: %d\n", config->ring_buffer_size);
    printf("debug log: %d\n", config->debug_log);
    printf("idle sleep: %d\n", config->idle_sleep);
    printf("single core: %d\n", config->single_core);
    printf("kni enable: %d\n", config->kni_enable);
    printf("rx checksum: %d\n", config->rx_checksum);
    printf("tx checksum: %d\n", config->tx_checksum);
    printf("tx only: %d\n", config->tx_only);
    printf("\n");
    // Go 传入的数组由调用方固定 整个主循环期间只读配置
    global_config = *config;

    // 初始化DPDK
    atomic_store(&running, false);
    int ret = rte_eal_init(config->eal_argc, config->eal_argv);
    if (ret < 0) {
        rte_exit(EXIT_FAILURE, "eal init failed\n");
    }
    printf("\n");

    uint64_t p;
    RTE_ETH_FOREACH_DEV(p) {
        char dev_name[RTE_DEV_NAME_MAX_LEN];
        rte_eth_dev_get_name_by_port(p, dev_name);
        printf("port number: %lu, port pci: %s, ", p, dev_name);
        struct rte_ether_addr dev_eth_addr = {0};
        rte_eth_macaddr_get(p, &dev_eth_addr);
        const uint8_t *mac_addr = dev_eth_addr.addr_bytes;
        printf("mac address: %02X:%02X:%02X:%02X:%02X:%02X\n", mac_addr[0], mac_addr[1], mac_addr[2], mac_addr[3], mac_addr[4], mac_addr[5]);
    }

    // 申请mbuf内存池
    const int socket_id = (int) rte_socket_id();
    mbuf_pool = rte_pktmbuf_pool_create(
        "mbuf_pool",
        NUM_MBUFS * config->port_id_num * config->queue_num,
        MBUF_CACHE_SIZE,
        0,
        RTE_MBUF_DEFAULT_BUF_SIZE,
        socket_id
    );
    if (!mbuf_pool) {
        rte_exit(EXIT_FAILURE, "mbuf pool create failed\n");
    }

    // 网卡初始化
    port_conf = (struct rte_eth_conf *) malloc(sizeof(struct rte_eth_conf) * config->port_id_num);
    memset(port_conf, 0x00, sizeof(struct rte_eth_conf) * config->port_id_num);
    for (int port_index = 0; port_index < config->port_id_num; port_index++) {
        const uint16_t port_id = config->port_id_list[port_index];
        ret = port_init(port_index, port_id, config->queue_num);
        if (ret != 0) {
            rte_exit(EXIT_FAILURE, "port init failed, port: %u\n", port_id);
        }
    }

    // 分配环状缓冲区内存
    const uint64_t ring_buffer_size = (uint64_t) config->ring_buffer_size + sizeof(ring_buffer_t);
    // 每个端口队列分别持有一对 Go 到 DPDK 和 DPDK 到 Go 的环形缓冲区
    port_ring_buffer = rte_zmalloc("port_ring_buffer",
                                   sizeof(struct ring_buffer) * config->port_id_num * config->queue_num,
                                   CACHE_LINE_SIZE);
    if (!port_ring_buffer) {
        rte_exit(EXIT_FAILURE, "port ring buffer metadata alloc failed\n");
    }
    for (int port_index = 0; port_index < config->port_id_num; port_index++) {
        for (int queue_id = 0; queue_id < config->queue_num; queue_id++) {
            const int i = port_index * config->queue_num + queue_id;
            port_ring_buffer[i].send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
            port_ring_buffer[i].send_ring_buffer = ring_buffer_create(port_ring_buffer[i].send_ring_mem, ring_buffer_size);
            if (!port_ring_buffer[i].send_ring_buffer) {
                rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
            }
            if (!ring_buffer_consumer_init(&port_ring_buffer[i].send_consumer, port_ring_buffer[i].send_ring_buffer, 0)) {
                rte_exit(EXIT_FAILURE, "send ring buffer consumer init failed\n");
            }
            port_ring_buffer[i].recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
            port_ring_buffer[i].recv_ring_buffer = ring_buffer_create(port_ring_buffer[i].recv_ring_mem, ring_buffer_size);
            if (!port_ring_buffer[i].recv_ring_buffer) {
                rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
            }
            if (!ring_buffer_producer_init(&port_ring_buffer[i].recv_producer, port_ring_buffer[i].recv_ring_buffer, 0)) {
                rte_exit(EXIT_FAILURE, "recv ring buffer producer init failed\n");
            }
        }
    }

    if (config->kni_enable) {
        // KNI网卡初始化
        kni_init();
        // KNI分配环状缓冲区内存
        kni_ring_buffer.send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        kni_ring_buffer.send_ring_buffer = ring_buffer_create(kni_ring_buffer.send_ring_mem, ring_buffer_size);
        if (!kni_ring_buffer.send_ring_buffer) {
            rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
        }
        if (!ring_buffer_consumer_init(&kni_ring_buffer.send_consumer, kni_ring_buffer.send_ring_buffer, 0)) {
            rte_exit(EXIT_FAILURE, "send ring buffer consumer init failed\n");
        }
        kni_ring_buffer.recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        kni_ring_buffer.recv_ring_buffer = ring_buffer_create(kni_ring_buffer.recv_ring_mem, ring_buffer_size);
        if (!kni_ring_buffer.recv_ring_buffer) {
            rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
        }
        if (!ring_buffer_producer_init(&kni_ring_buffer.recv_producer, kni_ring_buffer.recv_ring_buffer, 0)) {
            rte_exit(EXIT_FAILURE, "recv ring buffer producer init failed\n");
        }
    }

    // 启动数据包处理核心线程
    atomic_store(&running, true);
    if (config->single_core) {
        // 单核模式在当前 EAL 核心串行轮询全部端口和 KNI
        lcore_rx_tx(!config->tx_only, true, config->kni_enable);
    } else {
        // 多核模式按运行配置为每个端口队列启动工作核心
        struct lcore_arg arg_list[128] = {0};
        for (int port_index = 0; port_index < config->port_id_num; port_index++) {
            for (int queue_id = 0; queue_id < config->queue_num; queue_id++) {
                const int arg_index = port_index * config->queue_num + queue_id;
                arg_list[arg_index].port_index = port_index;
                arg_list[arg_index].queue_id = queue_id;
                if (config->tx_only) {
                    rte_eal_remote_launch(lcore_tx, arg_list + arg_index, config->dpdk_cpu_core_list[1 + arg_index]);
                } else {
                    const int cpu_core_index = 1 + port_index * config->queue_num * 2 + queue_id;
                    rte_eal_remote_launch(lcore_rx, arg_list + arg_index, config->dpdk_cpu_core_list[cpu_core_index]);
                    rte_eal_remote_launch(lcore_tx, arg_list + arg_index, config->dpdk_cpu_core_list[cpu_core_index + config->queue_num]);
                }
            }
        }
        lcore_rx_tx(false, false, config->kni_enable);
    }

    rte_eal_mp_wait_lcore();
    return 0;
}
