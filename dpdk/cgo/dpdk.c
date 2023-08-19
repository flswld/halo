const char *VERSION = "1.0.0";

#define _GNU_SOURCE

#include <sched.h>
#include <pthread.h>

int bind_cpu_core(int core) {
    cpu_set_t mask;
    CPU_ZERO(&mask);
    CPU_SET(core, &mask);
    int ret = pthread_setaffinity_np(pthread_self(), sizeof(mask), &mask);
    return ret;
}

#undef _GNU_SOURCE

#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>
#include <string.h>

#include <unistd.h>
#include <sys/time.h>
#include <arpa/inet.h>

#include <rte_eal.h>
#include <rte_common.h>
#include <rte_ethdev.h>
#include <rte_cycles.h>
#include <rte_lcore.h>
#include <rte_log.h>
#include <rte_mbuf.h>
#include <rte_bus_pci.h>
#include <rte_kni.h>
#include <rte_malloc.h>

#define KNI_ENET_HEADER_SIZE 14
#define KNI_ENET_FCS_SIZE 4
#define KNI_RX_RING_SIZE 1024
#define KNI_MAX_PACKET_SIZE 2048

#define NUM_MBUFS 8191
#define MBUF_CACHE_SIZE 250
#define RX_QUEUE 1
#define TX_QUEUE 1
#define RX_RING_SIZE 1024
#define TX_RING_SIZE 1024
#define BURST_SIZE 32
#define RTE_LOGTYPE_APP RTE_LOGTYPE_USER1
#define RTE_DEV_NAME_MAX_LEN 512

int ring_buffer_size = 0;
bool debug = false;
bool idle_sleep = false;
bool single_core = false;
bool kni_bypass = false;
uint32_t kni_bypass_target_ip = 0;

struct ring_buffer {
    uint8_t *mem_send_head;
    uint8_t *mem_send_cur;
    uint8_t *mem_recv_head;
    uint8_t *mem_recv_cur;
    volatile uint8_t *send_pos_pointer;
    volatile uint8_t *recv_pos_pointer;
};

int port_count = 0;
uint16_t *port_id_list = NULL;
struct ring_buffer *port_ring_buffer = NULL;
struct rte_eth_stats *port_old_stats = NULL;

static struct rte_eth_conf port_conf_default;
static struct rte_kni *kni;
struct rte_mempool *mbuf_pool = NULL;
volatile bool force_quit = false;

/*
0				8				16			24				32				...
+---------------------------------------------------------------------------+
|	len low		|	len high	|	finish	|	mem align	|	raw data	|
+---------------------------------------------------------------------------+
*/

// 发送缓冲区头部指针位置
void *mem_send_head_pointer(int port_index) {
    return (void *) (port_ring_buffer[port_index].mem_send_head);
}

// 接收缓冲区头部指针位置
void *mem_recv_head_pointer(int port_index) {
    return (void *) (port_ring_buffer[port_index].mem_recv_head);
}

// 发送缓冲区当前已读指针位置
void **send_pos_pointer_addr(int port_index) {
    return (void **) (&(port_ring_buffer[port_index].send_pos_pointer));
}

// 接收缓冲区当前已读指针位置
void **recv_pos_pointer_addr(int port_index) {
    return (void **) (&(port_ring_buffer[port_index].recv_pos_pointer));
}

// 环状缓冲区写入
uint8_t write_recv_mem_core(int port_index, uint8_t *data, uint16_t len) {
    // 内存对齐
    uint8_t aling_size = len % 4;
    if (aling_size != 0) {
        aling_size = 4 - aling_size;
    }
    len += aling_size;
    uint32_t head_u32 = 0;
    head_u32 = (((uint32_t) data[0]) << 0) +
               (((uint32_t) data[1]) << 8) +
               (((uint32_t) data[2]) << 16) +
               (((uint32_t) data[3]) << 24);
    int32_t overflow = (port_ring_buffer[port_index].mem_recv_cur + len) - (port_ring_buffer[port_index].mem_recv_head + ring_buffer_size);
    if (debug) {
        printf("[write_recv_mem_core] port_index: %d, overflow: %d, len: %d, mem_recv_cur: %p, mem_recv_head: %p, recv_pos_pointer: %p\n", port_index, overflow, len,
               port_ring_buffer[port_index].mem_recv_cur, port_ring_buffer[port_index].mem_recv_head, port_ring_buffer[port_index].recv_pos_pointer);
    }
    if (overflow >= 0) {
        // 有溢出
        if (port_ring_buffer[port_index].mem_recv_cur < port_ring_buffer[port_index].recv_pos_pointer) {
            // 已经处于读写指针交叉状态 丢弃数据
            return 1;
        }
        if (overflow >= (port_ring_buffer[port_index].recv_pos_pointer - port_ring_buffer[port_index].mem_recv_head)) {
            // 即使进入读写指针交叉状态 剩余内存依然不足 丢弃数据
            return 1;
        }
        uint32_t *head_ptr = (uint32_t *) port_ring_buffer[port_index].mem_recv_cur;
        // 写入头部 原子操作
        *head_ptr = head_u32;
        if ((len - overflow) > 4) {
            // 拷贝前半段数据
            memcpy(port_ring_buffer[port_index].mem_recv_cur + 4, data + 4, (len - overflow) - 4);
        }
        port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head;
        // 拷贝后半段数据
        memcpy(port_ring_buffer[port_index].mem_recv_cur, data + 4 + ((len - overflow) - 4), overflow);
        port_ring_buffer[port_index].mem_recv_cur += overflow;
        if (port_ring_buffer[port_index].mem_recv_cur >= port_ring_buffer[port_index].mem_recv_head + ring_buffer_size) {
            port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head;
        }
    } else {
        // 无溢出
        if ((port_ring_buffer[port_index].mem_recv_cur < port_ring_buffer[port_index].recv_pos_pointer) &&
            ((port_ring_buffer[port_index].mem_recv_cur + len) >= port_ring_buffer[port_index].recv_pos_pointer)) {
            // 状态下剩余内存不足 丢弃数据
            return 1;
        }
        uint32_t *head_ptr = (uint32_t *) port_ring_buffer[port_index].mem_recv_cur;
        // 写入头部 原子操作
        *head_ptr = head_u32;
        memcpy(port_ring_buffer[port_index].mem_recv_cur + 4, data + 4, len - 4);
        port_ring_buffer[port_index].mem_recv_cur += len;
        if (port_ring_buffer[port_index].mem_recv_cur >= port_ring_buffer[port_index].mem_recv_head + ring_buffer_size) {
            port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head;
        }
    }
    return 0;
}

// 写入接收缓冲区
void write_recv_mem(int port_index, uint8_t *data, uint16_t len) {
    if (port_ring_buffer[port_index].mem_recv_cur == NULL) {
        port_ring_buffer[port_index].mem_recv_cur = port_ring_buffer[port_index].mem_recv_head;
    }
    if (len > 1514) {
        return;
    }
    // 4字节头部 + 最大1514字节数据 + 2字节内存对齐
    uint8_t data_pkg[4 + 1514 + 2];
    memset(data_pkg, 0x00, 1520);
    // 数据长度标识
    data_pkg[0] = (uint8_t)(len);
    data_pkg[1] = (uint8_t)(len >> 8);
    // 写入完成标识
    uint8_t *finish_flag_pointer = port_ring_buffer[port_index].mem_recv_cur + 2;
    data_pkg[2] = 0x00;
    // 内存对齐
    data_pkg[3] = 0x00;
    // 写入数据
    memcpy(data_pkg + 4, data, len);
    uint8_t ret = write_recv_mem_core(port_index, data_pkg, len + 4);
    if (ret == 1) {
        return;
    }
    // 将后4个字节即长度标识与写入完成标识的内存置为0x00
    port_ring_buffer[port_index].mem_recv_cur[0] = 0x00;
    port_ring_buffer[port_index].mem_recv_cur[1] = 0x00;
    port_ring_buffer[port_index].mem_recv_cur[2] = 0x00;
    port_ring_buffer[port_index].mem_recv_cur[3] = 0x00;
    // 修改写入完成标识 原子操作
    *finish_flag_pointer = 0x01;
    return;
}

// 读取发送缓冲区
void read_send_mem(int port_index, uint8_t *data, uint16_t *len) {
    if (port_ring_buffer[port_index].mem_send_cur == NULL) {
        port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head;
    }
    *len = 0;
    // 读取头部 原子操作
    uint32_t *head_ptr = (uint32_t *) port_ring_buffer[port_index].mem_send_cur;
    uint32_t head_u32 = *head_ptr;
    *len = (uint16_t) head_u32;
    if (*len == 0) {
        // 没有新数据
        return;
    }
    uint8_t finish_flag = (uint8_t)(head_u32 >> 16);
    if (finish_flag == 0x00) {
        // 数据尚未写入完成
        *len = 0;
        return;
    }
    port_ring_buffer[port_index].mem_send_cur += 4;
    if (port_ring_buffer[port_index].mem_send_cur >= port_ring_buffer[port_index].mem_send_head + ring_buffer_size) {
        port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head;
    }
    int32_t overflow = (port_ring_buffer[port_index].mem_send_cur + *len) - (port_ring_buffer[port_index].mem_send_head + ring_buffer_size);
    if (debug) {
        printf("[read_send_mem] port_index: %d, overflow: %d, len: %d, mem_send_cur: %p, mem_send_head: %p, send_pos_pointer: %p\n", port_index, overflow, *len,
               port_ring_buffer[port_index].mem_send_cur, port_ring_buffer[port_index].mem_send_head, port_ring_buffer[port_index].send_pos_pointer);
    }
    if (overflow >= 0) {
        // 拷贝前半段数据
        memcpy(data, port_ring_buffer[port_index].mem_send_cur, *len - overflow);
        port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head;
        // 拷贝后半段数据
        memcpy(data + (*len - overflow), port_ring_buffer[port_index].mem_send_cur, *len - (*len - overflow));
        // 内存对齐
        uint8_t aling_size = overflow % 4;
        if (aling_size != 0) {
            aling_size = 4 - aling_size;
        }
        port_ring_buffer[port_index].mem_send_cur += overflow + aling_size;
        if (port_ring_buffer[port_index].mem_send_cur >= port_ring_buffer[port_index].mem_send_head + ring_buffer_size) {
            port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head;
        }
        port_ring_buffer[port_index].send_pos_pointer = port_ring_buffer[port_index].mem_send_cur;
    } else {
        memcpy(data, port_ring_buffer[port_index].mem_send_cur, *len);
        // 内存对齐
        uint8_t aling_size = *len % 4;
        if (aling_size != 0) {
            aling_size = 4 - aling_size;
        }
        port_ring_buffer[port_index].mem_send_cur += *len + aling_size;
        if (port_ring_buffer[port_index].mem_send_cur >= port_ring_buffer[port_index].mem_send_head + ring_buffer_size) {
            port_ring_buffer[port_index].mem_send_cur = port_ring_buffer[port_index].mem_send_head;
        }
        port_ring_buffer[port_index].send_pos_pointer = port_ring_buffer[port_index].mem_send_cur;
    }
    return;
}

static int kni_change_mtu(uint16_t port_id, unsigned int new_mtu) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }

    RTE_LOG(INFO, APP, "change mtu of port: %u, mtu: %u\n", port_id, new_mtu);

    rte_eth_dev_stop(port_id);

    struct rte_eth_conf port_conf = port_conf_default;
    port_conf.txmode.mq_mode = ETH_MQ_TX_NONE;
    if (new_mtu > ETHER_MAX_LEN) {
        port_conf.rxmode.offloads |= DEV_RX_OFFLOAD_JUMBO_FRAME;
    } else {
        port_conf.rxmode.offloads &= ~DEV_RX_OFFLOAD_JUMBO_FRAME;
    }
    port_conf.rxmode.max_rx_pkt_len = new_mtu + KNI_ENET_HEADER_SIZE + KNI_ENET_FCS_SIZE;

    int ret = rte_eth_dev_configure(port_id, 1, 1, &port_conf);
    if (ret < 0) {
        RTE_LOG(ERR, APP, "fail to reconfigure port: %u\n", port_id);
        return ret;
    }

    uint16_t nb_rxd = KNI_RX_RING_SIZE;
    ret = rte_eth_dev_adjust_nb_rx_tx_desc(port_id, &nb_rxd, NULL);
    if (ret < 0) {
        rte_exit(EXIT_FAILURE, "could not adjust number of descriptors for port: %u\n", port_id, ret);
    }

    struct rte_eth_dev_info dev_info;
    rte_eth_dev_info_get(port_id, &dev_info);

    struct rte_eth_rxconf rxq_conf;
    rxq_conf = dev_info.default_rxconf;
    rxq_conf.offloads = port_conf.rxmode.offloads;

    ret = rte_eth_rx_queue_setup(port_id, 0, nb_rxd, rte_eth_dev_socket_id(port_id), &rxq_conf, mbuf_pool);
    if (ret < 0) {
        RTE_LOG(ERR, APP, "fail to setup rx queue of port: %u\n", port_id);
        return ret;
    }

    ret = rte_eth_dev_start(port_id);
    if (ret < 0) {
        RTE_LOG(ERR, APP, "fail to restart port: %u\n", port_id);
        return ret;
    }

    return 0;
}

static int kni_config_network_interface(uint16_t port_id, uint8_t if_up) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }

    RTE_LOG(INFO, APP, "kni config network interface of %u %s\n", port_id, if_up ? "up" : "down");

    return 0;
}

static int kni_config_mac_address(uint16_t port_id, uint8_t mac_addr[]) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }

    RTE_LOG(INFO, APP, "configure mac address of port: %u\n", port_id);

    int ret = rte_eth_dev_default_mac_addr_set(port_id, (struct ether_addr *) mac_addr);
    if (ret < 0) {
        RTE_LOG(ERR, APP, "failed to config mac_addr for port: %u\n", port_id);
    }

    return ret;
}

int kni_alloc(uint16_t port_id) {
    struct rte_kni_conf conf;
    memset(&conf, 0, sizeof(conf));

    // conf.core_id: lcore_kthread
    conf.core_id = 0;
    conf.force_bind = 1;
    snprintf(conf.name, RTE_KNI_NAMESIZE, "veth%u", port_id);
    conf.group_id = port_id;
    conf.mbuf_size = KNI_MAX_PACKET_SIZE;

    struct rte_eth_dev_info dev_info;
    memset(&dev_info, 0, sizeof(dev_info));
    rte_eth_dev_info_get(port_id, &dev_info);
    if (dev_info.device) {
        struct rte_bus *bus = rte_bus_find_by_device(dev_info.device);
        if (bus && !strcmp(bus->name, "pci")) {
            struct rte_pci_device *pci_dev = RTE_DEV_TO_PCI(dev_info.device);
            conf.addr = pci_dev->addr;
            conf.id = pci_dev->id;
        }
    }

    rte_eth_macaddr_get(port_id, (struct ether_addr *) &conf.mac_addr);
    rte_eth_dev_get_mtu(port_id, &conf.mtu);

    struct rte_kni_ops ops;
    memset(&ops, 0, sizeof(ops));
    ops.port_id = port_id;
    ops.change_mtu = kni_change_mtu;
    ops.config_network_if = kni_config_network_interface;
    ops.config_mac_address = kni_config_mac_address;

    kni = rte_kni_alloc(mbuf_pool, &conf, &ops);
    if (!kni) {
        rte_exit(EXIT_FAILURE, "fail to create kni for port: %u\n", port_id);
    }

    return 0;
}

double cost_time_ms(struct timeval time_begin, struct timeval time_end) {
    double time_begin_us = time_begin.tv_sec * 1000000 + time_begin.tv_usec;
    double time_end_us = time_end.tv_sec * 1000000 + time_end.tv_usec;
    return (time_end_us - time_begin_us) / 1000;
}

// 打印收发包统计信息
void print_stats(int port_index) {
    uint16_t port_id = port_id_list[port_index];
    struct rte_eth_stats old_stats = port_old_stats[port_index];
    struct rte_eth_stats new_stats;
    rte_eth_stats_get(port_id, &new_stats);
    printf("[rte_eth_stats]\tport:%2u | "
           "rx:%10llu (pps) | "
           "tx:%10llu (pps) | "
           "drop:%10llu (pps) | "
           "rx:%20llu (byte/s) | "
           "tx:%20llu (byte/s)",
           port_id,
           new_stats.ipackets - old_stats.ipackets,
           new_stats.opackets - old_stats.opackets,
           new_stats.imissed - old_stats.imissed,
           new_stats.ibytes - old_stats.ibytes,
           new_stats.obytes - old_stats.obytes);
    port_old_stats[port_index] = new_stats;
}

// 处理退出信号并终止程序
void exit_signal_handler(void) {
    printf("exit signal received, exit...\n");
    force_quit = true;
}

// 初始化网口 配置收发队列
int port_init(uint16_t port_id) {
    uint8_t nb_ports = rte_eth_dev_count();
    if (port_id < 0 || port_id >= nb_ports) {
        printf("port is not right\n");
        return -1;
    }

    struct rte_eth_conf port_conf = port_conf_default;
    port_conf.rxmode.max_rx_pkt_len = ETHER_MAX_LEN;

    struct rte_eth_dev_info dev_info;
    rte_eth_dev_info_get(port_id, &dev_info);
    if (dev_info.tx_offload_capa & DEV_TX_OFFLOAD_MBUF_FAST_FREE) {
        port_conf.txmode.offloads |= DEV_TX_OFFLOAD_MBUF_FAST_FREE;
    }

    const uint16_t nb_rx_queues = RX_QUEUE;
    const uint16_t nb_tx_queues = TX_QUEUE;
    int ret;
    uint16_t q;

    // 配置设备
    ret = rte_eth_dev_configure(port_id, nb_rx_queues, nb_tx_queues, &port_conf);
    if (ret != 0) {
        printf("rte_eth_dev_configure failed\n");
        return ret;
    }

    uint16_t nb_rxd = RX_RING_SIZE;
    uint16_t nb_txd = TX_RING_SIZE;
    rte_eth_dev_adjust_nb_rx_tx_desc(port_id, &nb_rxd, &nb_txd);

    // 配置收包队列
    for (q = 0; q < nb_rx_queues; q++) {
        ret = rte_eth_rx_queue_setup(port_id, q, nb_rxd, rte_eth_dev_socket_id(port_id), NULL, mbuf_pool);
        if (ret < 0) {
            printf("rte_eth_rx_queue_setup failed\n");
            return ret;
        }
    }

    // 配置发包队列
    for (q = 0; q < nb_tx_queues; q++) {
        ret = rte_eth_tx_queue_setup(port_id, q, nb_txd, rte_eth_dev_socket_id(port_id), NULL);
        if (ret < 0) {
            printf("rte_eth_tx_queue_setup failed\n");
            return ret;
        }
    }

    // 启动设备
    ret = rte_eth_dev_start(port_id);
    if (ret < 0) {
        printf("rte_eth_dev_start failed\n");
        return ret;
    }

    // 开启混杂模式
    rte_eth_promiscuous_enable(port_id);

    return 0;
}

void lcore_misc_loop(void) {
    // KNI回调处理
    rte_kni_handle_request(kni);
}

int lcore_misc(void *arg) {
    unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_misc run in lcore: %u\n", lcore_id);
    while (!force_quit) {
        lcore_misc_loop();
    }
    RTE_LOG(INFO, APP, "lcore_misc exit in lcore: %u\n", lcore_id);
    return 0;
}

bool lcore_rx_loop(int port_index, uint16_t port_id) {
    // 接收多个网卡数据帧
    struct rte_mbuf *bufs_recv[BURST_SIZE];
    uint16_t nb_rx = rte_eth_rx_burst(port_id, 0, bufs_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(nb_rx == 0)) {
            return false;
        }
    }

    // 有网卡接收数据
    if (likely(nb_rx != 0)) {
        struct rte_mbuf *bufs_recv_to_kni[BURST_SIZE];
        uint16_t nb_rx_to_kni = 0;
        for (int i = 0; i < nb_rx; i++) {
            uint8_t *pktbuf = rte_pktmbuf_mtod(bufs_recv[i], uint8_t * );
            if (debug) {
                // 打印网卡接收到的原始数据
                printf("[nic recv], port_index: %d, len: %d, data: ", port_index, bufs_recv[i]->data_len);
                for (int j = 0; j < bufs_recv[i]->data_len; j++) {
                    printf("%02x ", pktbuf[j]);
                }
                printf("\n\n");
            }
            if (kni_bypass) {
                bool is_kni_data = false;
                if (unlikely(bufs_recv[i]->data_len < 14)) {
                    printf("error ethernet frm, len < 14\n");
                    continue;
                }
                if (likely(pktbuf[12] == 0x08) && likely(pktbuf[13] == 0x00)) {
                    // IP报文
                    if (unlikely(bufs_recv[i]->data_len < 34)) {
                        printf("error ip pkt, len < 34\n");
                        continue;
                    }
                    uint32_t src_addr = 0;
                    src_addr += ((uint32_t)(pktbuf[29]) << 24);
                    src_addr += ((uint32_t)(pktbuf[28]) << 16);
                    src_addr += ((uint32_t)(pktbuf[27]) << 8);
                    src_addr += ((uint32_t)(pktbuf[26]) << 0);
                    if (likely(src_addr == kni_bypass_target_ip)) {
                        // 环状缓冲区数据发送
                        uint16_t ring_send_len = bufs_recv[i]->data_len;
                        write_recv_mem(port_index, pktbuf, ring_send_len);
                        rte_pktmbuf_free(bufs_recv[i]);
                    } else {
                        is_kni_data = true;
                    }
                } else {
                    // 非IP报文
                    is_kni_data = true;
                }
                if (unlikely(is_kni_data)) {
                    // KNI数据
                    bufs_recv_to_kni[nb_rx_to_kni] = bufs_recv[i];
                    nb_rx_to_kni++;
                }
            } else {
                // 环状缓冲区数据发送
                uint16_t ring_send_len = bufs_recv[i]->data_len;
                write_recv_mem(port_index, pktbuf, ring_send_len);
                rte_pktmbuf_free(bufs_recv[i]);
            }
        }
        // KNI数据发送
        uint16_t nb_kni_tx = rte_kni_tx_burst(kni, bufs_recv_to_kni, nb_rx_to_kni);
        if (unlikely(nb_kni_tx < nb_rx_to_kni)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_kni_tx; i < nb_rx_to_kni; i++) {
                rte_pktmbuf_free(bufs_recv_to_kni[i]);
            }
        }
    }
    return true;
}

int lcore_rx(void *arg) {
    unsigned int lcore_id = rte_lcore_id();
    int port_index = *((int *) arg);
    uint16_t port_id = port_id_list[port_index];
    RTE_LOG(INFO, APP, "lcore_rx run in lcore: %u, port: %u\n", lcore_id, port_id);

    uint64_t loop_times = 0;
    struct timeval time_begin, time_end;
    while (!force_quit) {
        if (debug) {
            loop_times++;
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_begin, NULL);
            }
        }
        bool have_rx_data = lcore_rx_loop(port_index, port_id);
        if (!have_rx_data) {
            usleep(1000 * 1);
        }
        if (debug) {
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_end, NULL);
                double cost_time = cost_time_ms(time_begin, time_end);
                printf("lcore_rx_loop total cost: %f ms, lcore: %u, port_index: %d\n", cost_time, lcore_id, port_index);
            }
        }
    }

    RTE_LOG(INFO, APP, "lcore_rx exit in lcore: %u, port: %u\n", lcore_id, port_id);
    return 0;
}

bool lcore_tx_loop(int port_index, uint16_t port_id) {
    // 环状缓冲区数据接收
    uint8_t ring_recv_buf[BURST_SIZE][1514];
    uint16_t ring_recv_buf_len[BURST_SIZE];
    int ring_recv_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        read_send_mem(port_index, ring_recv_buf[i], &ring_recv_buf_len[i]);
        if (unlikely(ring_recv_buf_len[i] == 0)) {
            break;
        }
        ring_recv_size++;
    }

    // KNI数据接收
    struct rte_mbuf *bufs_kni_recv[BURST_SIZE];
    uint16_t nb_kni_rx = rte_kni_rx_burst(kni, bufs_kni_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(ring_recv_size == 0) && unlikely(nb_kni_rx == 0)) {
            return false;
        }
    }

    // 有环状缓冲区数据
    if (likely(ring_recv_size > 0)) {
        if (debug) {
            // 打印环状缓冲区数据
            for (int i = 0; i < ring_recv_size; i++) {
                printf("[ring recv], port_index: %d, len: %d, data: ", port_index, ring_recv_buf_len[i]);
                for (int j = 0; j < ring_recv_buf_len[i]; j++) {
                    printf("%02x ", ring_recv_buf[i][j]);
                }
                printf("\n\n");
            }
        }

        struct rte_mbuf *bufs_send[BURST_SIZE];

        // 数据拷贝
        for (int i = 0; i < ring_recv_size; i++) {
            bufs_send[i] = rte_pktmbuf_alloc(mbuf_pool);
            uint8_t *send_data = (uint8_t *) rte_pktmbuf_append(bufs_send[i], ring_recv_buf_len[i] * sizeof(uint8_t));
            memcpy(send_data, ring_recv_buf[i], ring_recv_buf_len[i]);
        }

        // 发送多个网卡数据帧
        uint16_t nb_tx = rte_eth_tx_burst(port_id, 0, bufs_send, ring_recv_size);

        if (unlikely(nb_tx < ring_recv_size)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_tx; i < ring_recv_size; i++) {
                rte_pktmbuf_free(bufs_send[i]);
            }
        }
    }

    // 有KNI数据
    if (likely(nb_kni_rx > 0)) {
        if (debug) {
            // 打印KNI数据
            for (int i = 0; i < nb_kni_rx; i++) {
                printf("[kni recv], port_index: %d, len: %d, data: ", port_index, bufs_kni_recv[i]->data_len);
                for (int j = 0; j < bufs_kni_recv[i]->data_len; j++) {
                    uint8_t *pktbuf = rte_pktmbuf_mtod(bufs_kni_recv[i], uint8_t * );
                    printf("%02x ", pktbuf[j]);
                }
                printf("\n\n");
            }
        }
        // 发送多个网卡数据帧
        uint16_t nb_tx = rte_eth_tx_burst(port_id, 0, bufs_kni_recv, nb_kni_rx);
        if (unlikely(nb_tx < nb_kni_rx)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_tx; i < nb_kni_rx; i++) {
                rte_pktmbuf_free(bufs_kni_recv[i]);
            }
        }
    }
    return true;
}

int lcore_tx(void *arg) {
    unsigned int lcore_id = rte_lcore_id();
    int port_index = *((int *) arg);
    uint16_t port_id = port_id_list[port_index];
    RTE_LOG(INFO, APP, "lcore_tx run in lcore: %u, port: %u\n", lcore_id, port_id);

    uint64_t loop_times = 0;
    struct timeval time_begin, time_end;
    while (!force_quit) {
        if (debug) {
            loop_times++;
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_begin, NULL);
            }
        }
        bool have_tx_data = lcore_tx_loop(port_index, port_id);
        if (!have_tx_data) {
            usleep(1000 * 1);
        }
        if (debug) {
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_end, NULL);
                double cost_time = cost_time_ms(time_begin, time_end);
                printf("lcore_tx_loop total cost: %f ms, lcore: %u, port_index: %d\n", cost_time, lcore_id, port_index);
            }
        }
    }

    RTE_LOG(INFO, APP, "lcore_tx exit in lcore: %u, port: %u\n", lcore_id, port_id);
    return 0;
}

int lcore_misc_rx_tx() {
    unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_misc_rx_tx run in lcore: %u\n", lcore_id);

    while (!force_quit) {
        lcore_misc_loop();
        bool all_port_idle = true;
        for (int i = 0; i < port_count; i++) {
            bool have_rx_data = lcore_rx_loop(i, port_id_list[i]);
            bool have_tx_data = lcore_tx_loop(i, port_id_list[i]);
            if (have_rx_data || have_tx_data) {
                all_port_idle = false;
            }
        }
        if (all_port_idle) {
            usleep(1000 * 1);
        }
    }

    RTE_LOG(INFO, APP, "lcore_misc_rx_tx exit in lcore: %u\n", lcore_id);
    return 0;
}

void parse_args(char *args_raw, int *argc, char ***argv) {
    int args_raw_len = strlen(args_raw);
    char *args = (char *) malloc(args_raw_len + 2);
    memset(args, 0x00, args_raw_len + 2);
    memcpy(args, args_raw, args_raw_len);
    args[args_raw_len] = ' ';
    args[args_raw_len + 1] = 0x00;
    (*argc) = 0;
    int args_len = strlen(args);
    for (int i = 0; i < args_len; i++) {
        if (args[i] == ' ') {
            (*argc)++;
        }
    }
    (*argv) = (char **) malloc(sizeof(char *) * (*argc));
    memset((*argv), 0x00, sizeof(char *) * (*argc));
    char **argv_copy = (*argv);
    int head = 0;
    for (int i = 0; i < args_len; i++) {
        if (args[i] == ' ') {
            int arg_len = i - head;
            *argv_copy = (char *) malloc(arg_len + 1);
            memset(*argv_copy, 0x00, arg_len + 1);
            memcpy(*argv_copy, args + head, arg_len);
            head = i + 1;
            argv_copy++;
        }
    }
    free(args);
}

struct dpdk_config {
    char *eal_args;
    char *cpu_core_list;
    char *port_id_list;
    int ring_buffer_size;
    bool debug_log;
    bool idle_sleep;
    bool single_core;
    bool kni_bypass;
    char *kni_bypass_target_ip;
};

int dpdk_main(struct dpdk_config *config) {
    printf("dpdk start, version: %s\n", VERSION);
    printf("eal_args: %s\n", config->eal_args);
    printf("\n");

    // 初始化DPDK
    int argc = 0;
    char **argv = NULL;
    parse_args(config->eal_args, &argc, &argv);
    int ret = rte_eal_init(argc, argv);
    free(argv);
    if (ret < 0) {
        rte_exit(EXIT_FAILURE, "eal init failed\n");
    }
    printf("\n");

    int cpu_core_count = 0;
    char **cpu_core_value = NULL;
    int cpu_core_list[1024];
    parse_args(config->cpu_core_list, &cpu_core_count, &cpu_core_value);
    for (int i = 0; i < cpu_core_count; i++) {
        cpu_core_list[i] = atoi(cpu_core_value[i]);
        free(cpu_core_value[i]);
    }
    free(cpu_core_value);

    ring_buffer_size = config->ring_buffer_size;
    printf("ring_buffer_size: %d\n", ring_buffer_size);

    debug = config->debug_log;
    printf("debug mode: %d\n", debug);

    idle_sleep = config->idle_sleep;
    printf("idle sleep mode: %d\n", idle_sleep);

    single_core = config->single_core;
    printf("single core mode: %d\n", single_core);

    kni_bypass = config->kni_bypass;
    printf("kni bypass mode: %d\n", kni_bypass);

    kni_bypass_target_ip = inet_addr(config->kni_bypass_target_ip);
    printf("kni bypass target ip: %s, inet addr: %x\n", config->kni_bypass_target_ip, kni_bypass_target_ip);

    force_quit = false;

    uint8_t nb_ports = rte_eth_dev_count();
    for (int i = 0; i < nb_ports; i++) {
        char dev_name[RTE_DEV_NAME_MAX_LEN];
        rte_eth_dev_get_name_by_port(i, dev_name);
        printf("port number: %d, port pci: %s, ", i, dev_name);
        struct ether_addr dev_eth_addr;
        rte_eth_macaddr_get(i, &dev_eth_addr);
        printf("mac address: %02X:%02X:%02X:%02X:%02X:%02X\n",
               dev_eth_addr.addr_bytes[0],
               dev_eth_addr.addr_bytes[1],
               dev_eth_addr.addr_bytes[2],
               dev_eth_addr.addr_bytes[3],
               dev_eth_addr.addr_bytes[4],
               dev_eth_addr.addr_bytes[5]);
    }

    // 申请mbuf内存池
    mbuf_pool = rte_pktmbuf_pool_create("mbuf_pool", NUM_MBUFS, MBUF_CACHE_SIZE, 0, RTE_MBUF_DEFAULT_BUF_SIZE, rte_socket_id());
    if (mbuf_pool == NULL) {
        rte_exit(EXIT_FAILURE, "mbuf pool create failed\n");
    }

    char **port_id_value = NULL;
    parse_args(config->port_id_list, &port_count, &port_id_value);
    port_id_list = (uint16_t *) malloc(sizeof(uint16_t) * port_count);
    memset(port_id_list, 0x00, sizeof(uint16_t) * port_count);
    for (int i = 0; i < port_count; i++) {
        port_id_list[i] = atoi(port_id_value[i]);
        free(port_id_value[i]);
    }
    free(port_id_value);

    port_ring_buffer = (struct ring_buffer *) malloc(sizeof(struct ring_buffer) * port_count);
    memset(port_ring_buffer, 0x00, sizeof(struct ring_buffer) * port_count);
    port_old_stats = (struct rte_eth_stats *) malloc(sizeof(struct rte_eth_stats) * port_count);
    memset(port_old_stats, 0x00, sizeof(struct rte_eth_stats) * port_count);
    for (int i = 0; i < port_count; i++) {
        // 网口初始化
        if (port_init(port_id_list[i]) != 0) {
            rte_exit(EXIT_FAILURE, "port init failed, port: %u\n", port_id_list[i]);
        }
        // 分配环状缓冲区内存
        port_ring_buffer[i].mem_send_head = rte_malloc("send_ring_buffer", ring_buffer_size, 0);
        if (port_ring_buffer[i].mem_send_head == NULL) {
            rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
        }
        port_ring_buffer[i].mem_send_cur = NULL;
        port_ring_buffer[i].mem_recv_head = rte_malloc("recv_ring_buffer", ring_buffer_size, 0);
        if (port_ring_buffer[i].mem_recv_head == NULL) {
            rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
        }
        port_ring_buffer[i].mem_recv_cur = NULL;
        port_ring_buffer[i].send_pos_pointer = port_ring_buffer[i].mem_send_head;
        port_ring_buffer[i].recv_pos_pointer = port_ring_buffer[i].mem_recv_head;
    }
    printf("\n");

    rte_kni_init(1);
    // kni默认与第一个网口关联
    kni_alloc(port_id_list[0]);

    if (single_core) {
        // 单核心机器上无法启动DPDK多线程 暂时用主线程跑
        lcore_misc_rx_tx();
    } else {
        // 线程核心绑定 循环处理数据包
        int port_index_list[1024];
        for (int i = 0; i < port_count; i++) {
            port_index_list[i] = i;
            rte_eal_remote_launch(lcore_tx, port_index_list + i, cpu_core_list[i * 2 + 2 + 0]);
            rte_eal_remote_launch(lcore_rx, port_index_list + i, cpu_core_list[i * 2 + 2 + 1]);
        }
        // 杂项线程默认用第二个cpu
        rte_eal_remote_launch(lcore_misc, NULL, cpu_core_list[1]);
        // 主线程默认用第一个cpu
        rte_eal_mp_wait_lcore();
    }

    if (rte_kni_release(kni)) {
        printf("fail to release kni\n");
    }
    free(port_id_list);
    free(port_ring_buffer);
    free(port_old_stats);
    for (int i = 0; i < port_count; i++) {
        rte_eth_dev_stop(port_id_list[i]);
        rte_free(port_ring_buffer[i].mem_send_head);
        rte_free(port_ring_buffer[i].mem_recv_head);
    }

    return 0;
}
