const char *VERSION = "2.0.0";

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
#include <rte_cycles.h>
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
#define RTE_LOGTYPE_APP RTE_LOGTYPE_USER1

struct dpdk_config {
    char *eal_args;
    char *cpu_core_list;
    char *port_id_list;
    int queue_num;
    int ring_buffer_size;
    bool debug_log;
    bool idle_sleep;
    bool single_core;
    bool kni_enable;
};

struct ring_buffer {
    void *send_ring_mem;
    void *recv_ring_mem;
    ring_buffer_t *send_ring_buffer;
    ring_buffer_t *recv_ring_buffer;
};

struct eth_macaddr {
    uint8_t addr_bytes[6];
} __attribute__((__aligned__(2)));

struct lcore_arg {
    int port_index;
    int queue_id;
};

_Atomic
bool running = false;

struct dpdk_config global_config = {0};
int port_count = 0;
uint16_t *port_id_list = NULL;
struct ring_buffer *port_ring_buffer = NULL;
struct ring_buffer kni_ring_buffer = {0};
struct rte_mempool *mbuf_pool = NULL;
struct rte_eth_stats *port_old_stats = NULL;
static struct rte_kni *kni = NULL;

// 获取网卡发包环状缓冲区
ring_buffer_t *cgo_port_send_ring_buffer(const int port_index, const int queue_id) {
    return port_ring_buffer[port_index * global_config.queue_num + queue_id].send_ring_buffer;
}

// 获取网卡收包环状缓冲区
ring_buffer_t *cgo_port_recv_ring_buffer(const int port_index, const int queue_id) {
    return port_ring_buffer[port_index * global_config.queue_num + queue_id].recv_ring_buffer;
}

// 获取KNI发包环状缓冲区
ring_buffer_t *cgo_kni_send_ring_buffer() {
    return kni_ring_buffer.send_ring_buffer;
}

// 获取KNI收包环状缓冲区
ring_buffer_t *cgo_kni_recv_ring_buffer() {
    return kni_ring_buffer.recv_ring_buffer;
}

// 打印网卡收发包统计信息
void cgo_print_stats(const int port_index, char *msg) {
    const uint16_t port_id = port_id_list[port_index];
    const struct rte_eth_stats old_stats = port_old_stats[port_index];
    struct rte_eth_stats new_stats = {0};
    rte_eth_stats_get(port_id, &new_stats);
    sprintf(msg, "[rte_eth_stats]\tport:%2u | rx:%10lu (pps) | tx:%10lu (pps) | drop:%10lu (pps) | rx:%20lu (byte/s) | tx:%20lu (byte/s)\n",
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
    for (int port_index = 0; port_index < port_count; port_index++) {
        const uint16_t port_id = port_id_list[port_index];
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
    free(port_id_list);
    free(port_ring_buffer);
    free(port_old_stats);

    port_count = 0;
    port_id_list = NULL;
    port_ring_buffer = NULL;
    memset(&kni_ring_buffer, 0x00, sizeof(struct ring_buffer));
    mbuf_pool = NULL;
    port_old_stats = NULL;
    kni = NULL;
}

// KNI回调处理
void cgo_kni_handle(void) {
    rte_kni_handle_request(kni);
}

int port_init(uint16_t port_id, const uint16_t queue_num) {
    struct rte_eth_conf port_conf = {0};
    port_conf.rxmode.max_rx_pkt_len = 1518;
    struct rte_eth_dev_info dev_info = {0};
    rte_eth_dev_info_get(port_id, &dev_info);
    if (dev_info.tx_offload_capa & DEV_TX_OFFLOAD_MBUF_FAST_FREE) {
        port_conf.txmode.offloads |= DEV_TX_OFFLOAD_MBUF_FAST_FREE;
    }
    RTE_LOG(INFO, APP, "port: %u, queue: %u, rx offloads: %lu, tx offloads: %lu\n", port_id, queue_num, port_conf.rxmode.offloads, port_conf.txmode.offloads);

    // 配置设备
    int ret = rte_eth_dev_configure(port_id, queue_num, queue_num, &port_conf);
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

static int kni_change_mtu(const uint16_t port_id, const unsigned int new_mtu) {
    RTE_LOG(INFO, APP, "kni change mtu of port: %u, mtu: %u\n", port_id, new_mtu);
    return 0;
}

static int kni_config_network_if(const uint16_t port_id, const uint8_t if_up) {
    RTE_LOG(INFO, APP, "kni config network if of port: %u if_up: %d\n", port_id, if_up);
    return 0;
}

static int kni_config_mac_address(const uint16_t port_id, uint8_t mac_addr[]) {
    char mac[64];
    sprintf(mac, "%02X:%02X:%02X:%02X:%02X:%02X", mac_addr[0], mac_addr[1], mac_addr[2], mac_addr[3], mac_addr[4], mac_addr[5]);
    RTE_LOG(INFO, APP, "kni config mac address of port: %u, mac: %s\n", port_id, mac);
    return 0;
}

static int kni_config_promiscusity(const uint16_t port_id, const uint8_t to_on) {
    RTE_LOG(INFO, APP, "kni config promiscusity of port: %u to_on: %d\n", port_id, to_on);
    return 0;
}

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

static bool eth_rx(const int port_index, const uint16_t port_id, const uint16_t queue_id) {
    // 接收多个网卡数据帧
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_eth_rx_burst(port_id, queue_id, mbuf_recv, BURST_SIZE);
    if (unlikely(nb_rx == 0)) {
        return false;
    }

    // 有网卡接收数据
    for (int i = 0; i < nb_rx; i++) {
        const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
        // 环状缓冲区数据发送
        const uint16_t recv_len = mbuf_recv[i]->data_len;
        write_packet(port_ring_buffer[port_index * global_config.queue_num + queue_id].recv_ring_buffer, recv_data, recv_len);
        if (unlikely(global_config.debug_log)) {
            // 打印网卡接收到的原始数据
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
        uint16_t send_len = 0;
        const bool ok = read_packet(port_ring_buffer[port_index * global_config.queue_num + queue_id].send_ring_buffer, send_data, &send_len);
        if (unlikely(!ok)) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        mbuf_send[i]->pkt_len = send_len;
        mbuf_send[i]->data_len = send_len;
        mbuf_send_size++;
        if (unlikely(global_config.debug_log)) {
            // 打印环状缓冲区数据
            printf("[ring recv], port_index: %d, len: %d, data: ", port_index, send_len);
            for (int j = 0; j < send_len; j++) {
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
    if (unlikely(nb_tx < mbuf_send_size)) {
        // 把没发送成功的mbuf释放掉
        for (int i = nb_tx; i < mbuf_send_size; i++) {
            rte_pktmbuf_free(mbuf_send[i]);
        }
    }
    return true;
}

static bool kni_rx() {
    // KNI数据接收
    struct rte_mbuf *mbuf_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_kni_rx_burst(kni, mbuf_recv, BURST_SIZE);
    if (nb_rx == 0) {
        return false;
    }

    // 有KNI接收数据
    for (int i = 0; i < nb_rx; i++) {
        const uint8_t *recv_data = rte_pktmbuf_mtod(mbuf_recv[i], uint8_t *);
        // KNI环状缓冲区数据发送
        const uint16_t recv_len = mbuf_recv[i]->data_len;
        write_packet(kni_ring_buffer.recv_ring_buffer, recv_data, recv_len);
        if (global_config.debug_log) {
            // 打印KNI接收到的原始数据
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
        uint16_t send_len = 0;
        const bool ok = read_packet(kni_ring_buffer.send_ring_buffer, send_data, &send_len);
        if (!ok) {
            rte_pktmbuf_free(mbuf_send[i]);
            break;
        }
        mbuf_send[i]->pkt_len = send_len;
        mbuf_send[i]->data_len = send_len;
        mbuf_send_size++;
        if (global_config.debug_log) {
            // 打印KNI环状缓冲区数据
            printf("[kni ring recv], len: %d, data: ", send_len);
            for (int j = 0; j < send_len; j++) {
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
    if (nb_tx < mbuf_send_size) {
        // 把没发送成功的mbuf释放掉
        for (int i = nb_tx; i < mbuf_send_size; i++) {
            rte_pktmbuf_free(mbuf_send[i]);
        }
    }
    return true;
}

int lcore_rx(void *arg_ptr) {
    const unsigned int lcore_id = rte_lcore_id();
    const struct lcore_arg *arg = arg_ptr;
    const int port_index = arg->port_index;
    const uint16_t port_id = port_id_list[port_index];
    const int queue_id = arg->queue_id;
    RTE_LOG(INFO, APP, "lcore_rx run in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);

    while (atomic_load(&running)) {
        const bool rx_pkt = eth_rx(port_index, port_id, queue_id);
        if (unlikely(!rx_pkt && global_config.idle_sleep)) {
            // 无包时短暂睡眠节省CPU资源
            usleep(1000 * 10);
        }
    }

    RTE_LOG(INFO, APP, "lcore_rx exit in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    return 0;
}

int lcore_tx(void *arg_ptr) {
    const unsigned int lcore_id = rte_lcore_id();
    const struct lcore_arg *arg = arg_ptr;
    const int port_index = arg->port_index;
    const uint16_t port_id = port_id_list[port_index];
    const int queue_id = arg->queue_id;
    RTE_LOG(INFO, APP, "lcore_tx run in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);

    while (atomic_load(&running)) {
        const bool tx_pkt = eth_tx(port_index, port_id, queue_id);
        if (unlikely(!tx_pkt && global_config.idle_sleep)) {
            // 无包时短暂睡眠节省CPU资源
            usleep(1000 * 10);
        }
    }

    RTE_LOG(INFO, APP, "lcore_tx exit in lcore: %u, port: %u, queue: %u\n", lcore_id, port_id, queue_id);
    return 0;
}

int lcore_rx_tx(const bool handle_eth, const bool handle_kni) {
    const unsigned int lcore_id = rte_lcore_id();
    RTE_LOG(INFO, APP, "lcore_rx_tx run in lcore: %u\n", lcore_id);

    while (atomic_load(&running)) {
        bool no_pkt = true;
        if (handle_eth) {
            for (int port_index = 0; port_index < port_count; port_index++) {
                const uint16_t port_id = port_id_list[port_index];
                const bool rx_pkt = eth_rx(port_index, port_id, 0);
                const bool tx_pkt = eth_tx(port_index, port_id, 0);
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
        if (no_pkt && global_config.idle_sleep) {
            // 无包时短暂睡眠节省CPU资源
            usleep(1000 * 10);
        }
    }

    RTE_LOG(INFO, APP, "lcore_rx_tx exit in lcore: %u\n", lcore_id);
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

int cgo_dpdk_main(const struct dpdk_config *config) {
    printf("dpdk start, version: %s\n", VERSION);
    printf("eal args: %s\n", config->eal_args);
    printf("cpu core list: %s\n", config->cpu_core_list);
    printf("port id list: %s\n", config->port_id_list);
    printf("queue num: %d\n", config->queue_num);
    printf("ring buffer size: %d\n", config->ring_buffer_size);
    printf("debug log: %d\n", config->debug_log);
    printf("idle sleep: %d\n", config->idle_sleep);
    printf("single core: %d\n", config->single_core);
    printf("kni enable: %d\n", config->kni_enable);
    printf("\n");
    global_config = *config;

    // 初始化DPDK
    atomic_store(&running, false);
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
    int cpu_core_list[128];
    parse_args(config->cpu_core_list, &cpu_core_count, &cpu_core_value);
    for (int i = 0; i < cpu_core_count; i++) {
        cpu_core_list[i] = (int) strtol(cpu_core_value[i], NULL, 10);
        free(cpu_core_value[i]);
    }
    free(cpu_core_value);

    const uint32_t ring_buffer_size = config->ring_buffer_size + sizeof(ring_buffer_t);

    char **port_id_value = NULL;
    parse_args(config->port_id_list, &port_count, &port_id_value);
    port_id_list = (uint16_t *) malloc(sizeof(uint16_t) * port_count);
    memset(port_id_list, 0x00, sizeof(uint16_t) * port_count);
    for (int i = 0; i < port_count; i++) {
        port_id_list[i] = strtol(port_id_value[i], NULL, 10);
        free(port_id_value[i]);
    }
    free(port_id_value);

    // 申请mbuf内存池
    const int socket_id = (int) rte_socket_id();
    mbuf_pool = rte_pktmbuf_pool_create(
        "mbuf_pool",
        NUM_MBUFS * port_count * config->queue_num,
        MBUF_CACHE_SIZE,
        0,
        RTE_MBUF_DEFAULT_BUF_SIZE,
        socket_id
    );
    if (!mbuf_pool) {
        rte_exit(EXIT_FAILURE, "mbuf pool create failed\n");
    }

    port_ring_buffer = (struct ring_buffer *) malloc(sizeof(struct ring_buffer) * port_count * config->queue_num);
    memset(port_ring_buffer, 0x00, sizeof(struct ring_buffer) * port_count * config->queue_num);
    port_old_stats = (struct rte_eth_stats *) malloc(sizeof(struct rte_eth_stats) * port_count);
    memset(port_old_stats, 0x00, sizeof(struct rte_eth_stats) * port_count);
    for (int port_index = 0; port_index < port_count; port_index++) {
        // 网卡初始化
        const uint16_t port_id = port_id_list[port_index];
        ret = port_init(port_id, config->queue_num);
        if (ret != 0) {
            rte_exit(EXIT_FAILURE, "port init failed, port: %u\n", port_id);
        }
        for (int queue_id = 0; queue_id < config->queue_num; queue_id++) {
            const int i = port_index * config->queue_num + queue_id;
            // 分配环状缓冲区内存
            port_ring_buffer[i].send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
            port_ring_buffer[i].send_ring_buffer = ring_buffer_create(port_ring_buffer[i].send_ring_mem, ring_buffer_size);
            if (!port_ring_buffer[i].send_ring_buffer) {
                rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
            }
            port_ring_buffer[i].recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
            port_ring_buffer[i].recv_ring_buffer = ring_buffer_create(port_ring_buffer[i].recv_ring_mem, ring_buffer_size);
            if (!port_ring_buffer[i].recv_ring_buffer) {
                rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
            }
        }
    }

    printf("\n");

    if (config->kni_enable) {
        // KNI分配环状缓冲区内存
        kni_ring_buffer.send_ring_mem = rte_malloc("send_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        kni_ring_buffer.send_ring_buffer = ring_buffer_create(kni_ring_buffer.send_ring_mem, ring_buffer_size);
        if (!kni_ring_buffer.send_ring_buffer) {
            rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
        }
        kni_ring_buffer.recv_ring_mem = rte_malloc("recv_ring_buffer", ring_buffer_size, CACHE_LINE_SIZE);
        kni_ring_buffer.recv_ring_buffer = ring_buffer_create(kni_ring_buffer.recv_ring_mem, ring_buffer_size);
        if (!kni_ring_buffer.recv_ring_buffer) {
            rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
        }

        kni_init();
    }

    // 线程核心绑定 循环处理数据包
    atomic_store(&running, true);
    if (config->single_core) {
        lcore_rx_tx(true, config->kni_enable);
    } else {
        struct lcore_arg arg_list[128] = {0};
        for (int port_index = 0; port_index < port_count; port_index++) {
            for (int queue_id = 0; queue_id < config->queue_num; queue_id++) {
                const int arg_index = port_index * config->queue_num + queue_id;
                arg_list[arg_index].port_index = port_index;
                arg_list[arg_index].queue_id = queue_id;
                const int cpu_core_index = 1 + port_index * config->queue_num * 2 + queue_id;
                rte_eal_remote_launch(lcore_rx, arg_list + arg_index, cpu_core_list[cpu_core_index + 0]);
                rte_eal_remote_launch(lcore_tx, arg_list + arg_index, cpu_core_list[cpu_core_index + config->queue_num]);
            }
        }
        lcore_rx_tx(false, config->kni_enable);
    }

    return 0;
}
