const char *VERSION = "1.0.0";

#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>
#include <string.h>
#include <stdatomic.h>

#include <unistd.h>
#include <sys/time.h>

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

#include "ring_buffer.h"

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

_Atomic
bool dpdk_start = false;

int ring_buffer_size = 0;
bool debug = false;
bool idle_sleep = false;
bool single_core = false;

struct ring_buffer {
    void *send_ring_mem;
    void *recv_ring_mem;
    ring_buffer_t *send_ring_buffer;
    ring_buffer_t *recv_ring_buffer;
};

int port_count = 0;
uint16_t *port_id_list = NULL;
struct ring_buffer *port_ring_buffer = NULL;
struct ring_buffer kni_ring_buffer = {0};
struct rte_eth_stats *port_old_stats = NULL;

static struct rte_eth_conf port_conf_default = {0};
static struct rte_kni *kni;
struct rte_mempool *mbuf_pool = NULL;
_Atomic
bool running = false;

ring_buffer_t *cgo_port_send_ring_buffer(const int port_index) {
    return port_ring_buffer[port_index].send_ring_buffer;
}

ring_buffer_t *cgo_port_recv_ring_buffer(const int port_index) {
    return port_ring_buffer[port_index].recv_ring_buffer;
}

ring_buffer_t *cgo_kni_send_ring_buffer() {
    return kni_ring_buffer.send_ring_buffer;
}

ring_buffer_t *cgo_kni_recv_ring_buffer() {
    return kni_ring_buffer.recv_ring_buffer;
}

// 写入接收缓冲区
void write_recv_mem(const struct ring_buffer *buffer, const uint8_t *data, const uint16_t len) {
    write_packet(buffer->recv_ring_buffer, data, len);
}

// 读取发送缓冲区
void read_send_mem(const struct ring_buffer *buffer, uint8_t *data, uint16_t *len) {
    const bool ok = read_packet(buffer->send_ring_buffer, data, len);
    if (!ok) {
        *len = 0;
    }
}

static int kni_change_mtu(const uint16_t port_id, const unsigned int new_mtu) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }
    RTE_LOG(INFO, APP, "kni change mtu of port: %u, mtu: %u\n", port_id, new_mtu);
    return 0;
}

static int kni_config_network_if(const uint16_t port_id, const uint8_t if_up) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }
    RTE_LOG(INFO, APP, "kni config network if of port: %u if_up: %d\n", port_id, if_up);
    return 0;
}

static int kni_config_mac_address(const uint16_t port_id, uint8_t mac_addr[]) {
    if (!rte_eth_dev_is_valid_port(port_id)) {
        RTE_LOG(ERR, APP, "invalid port id: %u\n", port_id);
        return -EINVAL;
    }
    char mac[64];
    sprintf(mac, "%02X:%02X:%02X:%02X:%02X:%02X", mac_addr[0], mac_addr[1], mac_addr[2], mac_addr[3], mac_addr[4], mac_addr[5]);
    RTE_LOG(INFO, APP, "kni config mac address of port: %u, mac: %s\n", port_id, mac);
    return 0;
}

int kni_alloc(uint16_t port_id) {
    struct rte_kni_conf conf = {0};

    // conf.core_id: lcore_kthread
    conf.core_id = 0;
    conf.force_bind = 1;
    sprintf(conf.name, "ethkni");
    conf.group_id = port_id;
    conf.mbuf_size = KNI_MAX_PACKET_SIZE;

    struct rte_eth_dev_info dev_info = {0};
    rte_eth_dev_info_get(port_id, &dev_info);
    if (dev_info.device) {
        struct rte_bus *bus = rte_bus_find_by_device(dev_info.device);
        if (bus && !strcmp(bus->name, "pci")) {
            struct rte_pci_device *pci_dev = RTE_DEV_TO_PCI(dev_info.device);
            conf.addr = pci_dev->addr;
            conf.id = pci_dev->id;
        }
    }

    conf.mac_addr[0] = 0x65;
    conf.mac_addr[1] = 0x74;
    conf.mac_addr[2] = 0x68;
    conf.mac_addr[3] = 0x6b;
    conf.mac_addr[4] = 0x6e;
    conf.mac_addr[5] = 0x69;

    rte_eth_dev_get_mtu(port_id, &conf.mtu);

    struct rte_kni_ops ops = {0};
    ops.port_id = port_id;
    ops.change_mtu = kni_change_mtu;
    ops.config_network_if = kni_config_network_if;
    ops.config_mac_address = kni_config_mac_address;

    kni = rte_kni_alloc(mbuf_pool, &conf, &ops);
    if (!kni) {
        rte_exit(EXIT_FAILURE, "fail to create kni for port: %u\n", port_id);
    }

    return 0;
}

double cost_time_ms(const struct timeval time_begin, const struct timeval time_end) {
    const double time_begin_us = time_begin.tv_sec * 1000000 + time_begin.tv_usec;
    const double time_end_us = time_end.tv_sec * 1000000 + time_end.tv_usec;
    return (time_end_us - time_begin_us) / 1000;
}

// 打印收发包统计信息
void cgo_print_stats(const int port_index, char *msg) {
    const uint16_t port_id = port_id_list[port_index];
    const struct rte_eth_stats old_stats = port_old_stats[port_index];
    struct rte_eth_stats new_stats = {0};
    rte_eth_stats_get(port_id, &new_stats);
    sprintf(msg, "[rte_eth_stats]\tport:%2u | "
            "rx:%10lu (pps) | "
            "tx:%10lu (pps) | "
            "drop:%10lu (pps) | "
            "rx:%20lu (byte/s) | "
            "tx:%20lu (byte/s)\n",
            port_id,
            new_stats.ipackets - old_stats.ipackets,
            new_stats.opackets - old_stats.opackets,
            new_stats.imissed - old_stats.imissed,
            new_stats.ibytes - old_stats.ibytes,
            new_stats.obytes - old_stats.obytes);
    port_old_stats[port_index] = new_stats;
}

// 处理退出信号并终止程序
void cgo_exit_signal_handler(void) {
    RTE_LOG(INFO, APP, "exit signal received, exit...\n");
    atomic_store(&running, false);
}

// 初始化网口 配置收发队列
int port_init(uint16_t port_id) {
    struct rte_eth_conf port_conf = port_conf_default;
    port_conf.rxmode.max_rx_pkt_len = ETHER_MAX_LEN;

    struct rte_eth_dev_info dev_info = {0};
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
        RTE_LOG(ERR, APP, "rte_eth_dev_configure failed\n");
        return ret;
    }

    uint16_t nb_rxd = RX_RING_SIZE;
    uint16_t nb_txd = TX_RING_SIZE;
    rte_eth_dev_adjust_nb_rx_tx_desc(port_id, &nb_rxd, &nb_txd);

    // 配置收包队列
    for (q = 0; q < nb_rx_queues; q++) {
        ret = rte_eth_rx_queue_setup(port_id, q, nb_rxd, rte_eth_dev_socket_id(port_id), NULL, mbuf_pool);
        if (ret < 0) {
            RTE_LOG(ERR, APP, "rte_eth_rx_queue_setup failed\n");
            return ret;
        }
    }

    // 配置发包队列
    for (q = 0; q < nb_tx_queues; q++) {
        ret = rte_eth_tx_queue_setup(port_id, q, nb_txd, rte_eth_dev_socket_id(port_id), NULL);
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

void misc(void) {
    // KNI回调处理
    rte_kni_handle_request(kni);
}

int lcore_misc(void *arg) {
    const unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_misc run in lcore: %u\n", lcore_id);
    while (atomic_load(&running)) {
        misc();
    }
    RTE_LOG(INFO, APP, "lcore_misc exit in lcore: %u\n", lcore_id);
    return 0;
}

bool eth_rx(const int port_index, const uint16_t port_id) {
    // 接收多个网卡数据帧
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_eth_rx_burst(port_id, 0, mbuf_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(nb_rx == 0)) {
            return false;
        }
    }

    // 有网卡接收数据
    if (likely(nb_rx > 0)) {
        for (int i = 0; i < nb_rx; i++) {
            const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
            // 环状缓冲区数据发送
            const uint16_t recv_len = mbuf_recv[i]->data_len;
            write_recv_mem(&port_ring_buffer[port_index], recv_data, recv_len);
            if (debug) {
                // 打印网卡接收到的原始数据
                printf("[nic recv], port_index: %d, len: %d, data: ", port_index, mbuf_recv[i]->data_len);
                for (int j = 0; j < mbuf_recv[i]->data_len; j++) {
                    printf("%02x", recv_data[j]);
                }
                printf("\n\n");
            }
            rte_pktmbuf_free(mbuf_recv[i]);
        }
    }
    return true;
}

bool kni_rx() {
    // KNI数据接收
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_kni_rx_burst(kni, mbuf_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(nb_rx == 0)) {
            return false;
        }
    }

    // 有KNI接收数据
    if (likely(nb_rx > 0)) {
        for (int i = 0; i < nb_rx; i++) {
            const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
            // KNI环状缓冲区数据发送
            const uint16_t recv_len = mbuf_recv[i]->data_len;
            write_recv_mem(&kni_ring_buffer, recv_data, recv_len);
            if (debug) {
                // 打印KNI接收到的原始数据
                printf("[kni recv], len: %d, data: ", mbuf_recv[i]->data_len);
                for (int j = 0; j < mbuf_recv[i]->data_len; j++) {
                    printf("%02x", recv_data[j]);
                }
                printf("\n\n");
            }
            rte_pktmbuf_free(mbuf_recv[i]);
        }
    }
    return true;
}

int lcore_rx(void *arg) {
    const unsigned int lcore_id = rte_lcore_id();
    const int port_index = *(int *) arg;
    const uint16_t port_id = port_id_list[port_index];
    RTE_LOG(INFO, APP, "lcore_rx run in lcore: %u, port: %u\n", lcore_id, port_id);

    uint64_t loop_times = 0;
    struct timeval time_begin = {0};
    struct timeval time_end = {0};
    while (atomic_load(&running)) {
        if (debug) {
            loop_times++;
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_begin, NULL);
            }
        }
        const bool have_rx_data = eth_rx(port_index, port_id);
        const bool have_kni_rx_data = kni_rx();
        if (!have_rx_data && !have_kni_rx_data) {
            usleep(1000 * 10);
        }
        if (debug) {
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_end, NULL);
                const double cost_time = cost_time_ms(time_begin, time_end);
                printf("lcore_rx_loop total cost: %f ms, lcore: %u, port_index: %d\n", cost_time, lcore_id, port_index);
            }
        }
    }

    RTE_LOG(INFO, APP, "lcore_rx exit in lcore: %u, port: %u\n", lcore_id, port_id);
    return 0;
}

bool eth_tx(const int port_index, const uint16_t port_id) {
    // 环状缓冲区数据接收
    struct rte_mbuf *mbuf_send[BURST_SIZE];
    int mbuf_send_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        mbuf_send[i] = rte_pktmbuf_alloc(mbuf_pool);
        uint8_t *send_data = (uint8_t *) rte_pktmbuf_append(mbuf_send[i], 1514);
        uint16_t send_len = 0;
        read_send_mem(&port_ring_buffer[port_index], send_data, &send_len);
        if (unlikely(send_len == 0)) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        rte_pktmbuf_trim(mbuf_send[i], 1514 - send_len);
        mbuf_send_size++;
        if (debug) {
            // 打印环状缓冲区数据
            printf("[ring recv], port_index: %d, len: %d, data: ", port_index, send_len);
            for (int j = 0; j < send_len; j++) {
                printf("%02x", send_data[j]);
            }
            printf("\n\n");
        }
    }

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(mbuf_send_size == 0)) {
            return false;
        }
    }

    // 有环状缓冲区数据
    if (likely(mbuf_send_size > 0)) {
        // 发送多个网卡数据帧
        const uint16_t nb_tx = rte_eth_tx_burst(port_id, 0, mbuf_send, mbuf_send_size);
        if (unlikely(nb_tx < mbuf_send_size)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_tx; i < mbuf_send_size; i++) {
                rte_pktmbuf_free(mbuf_send[i]);
            }
        }
    }

    return true;
}

bool kni_tx() {
    // KNI环状缓冲区数据接收
    struct rte_mbuf *mbuf_send[BURST_SIZE];
    int mbuf_send_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        mbuf_send[i] = rte_pktmbuf_alloc(mbuf_pool);
        uint8_t *send_data = (uint8_t *) rte_pktmbuf_append(mbuf_send[i], 1514);
        uint16_t send_len = 0;
        read_send_mem(&kni_ring_buffer, send_data, &send_len);
        if (unlikely(send_len == 0)) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        rte_pktmbuf_trim(mbuf_send[i], 1514 - send_len);
        mbuf_send_size++;
        if (debug) {
            // 打印KNI环状缓冲区数据
            printf("[kni ring recv], len: %d, data: ", send_len);
            for (int j = 0; j < send_len; j++) {
                printf("%02x", send_data[j]);
            }
            printf("\n\n");
        }
    }

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(mbuf_send_size == 0)) {
            return false;
        }
    }

    // 有KNI环状缓冲区数据
    if (likely(mbuf_send_size > 0)) {
        // KNI数据发送
        const uint16_t nb_tx = rte_kni_tx_burst(kni, mbuf_send, mbuf_send_size);
        if (unlikely(nb_tx < mbuf_send_size)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_tx; i < mbuf_send_size; i++) {
                rte_pktmbuf_free(mbuf_send[i]);
            }
        }
    }

    return true;
}

int lcore_tx(void *arg) {
    const unsigned int lcore_id = rte_lcore_id();
    const int port_index = *(int *) arg;
    const uint16_t port_id = port_id_list[port_index];
    RTE_LOG(INFO, APP, "lcore_tx run in lcore: %u, port: %u\n", lcore_id, port_id);

    uint64_t loop_times = 0;
    struct timeval time_begin = {0};
    struct timeval time_end = {0};
    while (atomic_load(&running)) {
        if (debug) {
            loop_times++;
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_begin, NULL);
            }
        }
        const bool have_tx_data = eth_tx(port_index, port_id);
        if (!have_tx_data) {
            usleep(1000 * 10);
        }
        if (debug) {
            if (loop_times % (1000 * 1000 * 10) == 0) {
                gettimeofday(&time_end, NULL);
                const double cost_time = cost_time_ms(time_begin, time_end);
                printf("lcore_tx_loop total cost: %f ms, lcore: %u, port_index: %d\n", cost_time, lcore_id, port_index);
            }
        }
    }

    RTE_LOG(INFO, APP, "lcore_tx exit in lcore: %u, port: %u\n", lcore_id, port_id);
    return 0;
}

int lcore_misc_rx_tx() {
    const unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_misc_rx_tx run in lcore: %u\n", lcore_id);

    while (atomic_load(&running)) {
        misc();
        bool all_port_idle = true;
        for (int i = 0; i < port_count; i++) {
            const bool have_rx_data = eth_rx(i, port_id_list[i]);
            const bool have_tx_data = eth_tx(i, port_id_list[i]);
            const bool have_kni_rx_data = kni_rx();
            const bool have_kni_tx_data = kni_tx();
            if (have_rx_data || have_tx_data || have_kni_rx_data || have_kni_tx_data) {
                all_port_idle = false;
            }
        }
        if (all_port_idle) {
            usleep(1000 * 10);
        }
    }

    RTE_LOG(INFO, APP, "lcore_misc_rx_tx exit in lcore: %u\n", lcore_id);
    return 0;
}

void parse_args(const char *args_raw, int *argc, char ***argv) {
    const size_t args_raw_len = strlen(args_raw);
    char *args = malloc(args_raw_len + 2);
    memset(args, 0x00, args_raw_len + 2);
    memcpy(args, args_raw, args_raw_len);
    args[args_raw_len] = ' ';
    args[args_raw_len + 1] = 0x00;
    *argc = 0;
    const size_t args_len = strlen(args);
    for (int i = 0; i < args_len; i++) {
        if (args[i] == ' ') {
            (*argc)++;
        }
    }
    *argv = (char **) malloc(sizeof(char *) * *argc);
    memset(*argv, 0x00, sizeof(char *) * *argc);
    char **argv_copy = *argv;
    int head = 0;
    for (int i = 0; i < args_len; i++) {
        if (args[i] == ' ') {
            const int arg_len = i - head;
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
};

int cgo_dpdk_main(const struct dpdk_config *config) {
    printf("dpdk start, version: %s\n", VERSION);
    printf("eal_args: %s\n", config->eal_args);
    printf("\n");

    // 初始化DPDK
    atomic_store(&dpdk_start, false);
    int argc = 0;
    char **argv = NULL;
    parse_args(config->eal_args, &argc, &argv);
    const int ret = rte_eal_init(argc, argv);
    free(argv);
    if (ret < 0) {
        rte_exit(EXIT_FAILURE, "eal init failed\n");
    }
    printf("\n");
    atomic_store(&dpdk_start, true);

    int cpu_core_count = 0;
    char **cpu_core_value = NULL;
    int cpu_core_list[1024];
    parse_args(config->cpu_core_list, &cpu_core_count, &cpu_core_value);
    for (int i = 0; i < cpu_core_count; i++) {
        cpu_core_list[i] = strtol(cpu_core_value[i], NULL, 10);
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

    atomic_store(&running, true);

    uint64_t p;
    RTE_ETH_FOREACH_DEV(p) {
        char dev_name[RTE_DEV_NAME_MAX_LEN];
        rte_eth_dev_get_name_by_port(p, dev_name);
        printf("port number: %lu, port pci: %s, ", p, dev_name);
        struct ether_addr dev_eth_addr = {0};
        rte_eth_macaddr_get(p, &dev_eth_addr);
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
        port_id_list[i] = strtol(port_id_value[i], NULL, 10);
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
        port_ring_buffer[i].send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        port_ring_buffer[i].send_ring_buffer = ring_buffer_create(port_ring_buffer[i].send_ring_mem, ring_buffer_size);
        if (port_ring_buffer[i].send_ring_buffer == NULL) {
            rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
        }
        port_ring_buffer[i].recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        port_ring_buffer[i].recv_ring_buffer = ring_buffer_create(port_ring_buffer[i].recv_ring_mem, ring_buffer_size);
        if (port_ring_buffer[i].recv_ring_buffer == NULL) {
            rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
        }
    }
    // kni分配环状缓冲区内存
    kni_ring_buffer.send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
    kni_ring_buffer.send_ring_buffer = ring_buffer_create(kni_ring_buffer.send_ring_mem, ring_buffer_size);
    if (kni_ring_buffer.send_ring_buffer == NULL) {
        rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
    }
    kni_ring_buffer.recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
    kni_ring_buffer.recv_ring_buffer = ring_buffer_create(kni_ring_buffer.recv_ring_mem, ring_buffer_size);
    if (kni_ring_buffer.recv_ring_buffer == NULL) {
        rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
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
        RTE_LOG(ERR, APP, "fail to release kni\n");
    }
    for (int i = 0; i < port_count; i++) {
        rte_eth_dev_stop(port_id_list[i]);
        rte_free(port_ring_buffer[i].send_ring_mem);
        rte_free(port_ring_buffer[i].recv_ring_mem);
        ring_buffer_destroy(port_ring_buffer[i].send_ring_buffer);
        ring_buffer_destroy(port_ring_buffer[i].recv_ring_buffer);
    }
    rte_free(kni_ring_buffer.send_ring_mem);
    rte_free(kni_ring_buffer.recv_ring_mem);
    ring_buffer_destroy(kni_ring_buffer.send_ring_buffer);
    ring_buffer_destroy(kni_ring_buffer.recv_ring_buffer);
    free(port_id_list);
    free(port_ring_buffer);
    free(port_old_stats);

    return 0;
}
