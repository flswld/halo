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

_Atomic
bool dpdk_start = false;

int ring_buffer_size = 0;
bool debug = false;
bool idle_sleep = false;
bool single_core = false;

struct ring_buffer {
    uint8_t *mem_send_head;
    uint8_t *mem_send_cur;
    uint8_t *mem_recv_head;
    uint8_t *mem_recv_cur;
    volatile uint8_t *send_pos_pointer;
    volatile uint8_t *recv_pos_pointer;
    uint64_t size;
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

/*
0				8				16			24				32				...
+---------------------------------------------------------------------------+
|	len low		|	len high	|	finish	|	mem align	|	raw data	|
+---------------------------------------------------------------------------+
*/

// 发送缓冲区头部指针位置
void *cgo_mem_send_head_pointer(const int port_index) {
    return port_ring_buffer[port_index].mem_send_head;
}

// 接收缓冲区头部指针位置
void *cgo_mem_recv_head_pointer(const int port_index) {
    return port_ring_buffer[port_index].mem_recv_head;
}

// 发送缓冲区当前已读指针位置
void **cgo_send_pos_pointer_addr(const int port_index) {
    return (void **) &port_ring_buffer[port_index].send_pos_pointer;
}

// 接收缓冲区当前已读指针位置
void **cgo_recv_pos_pointer_addr(const int port_index) {
    return (void **) &port_ring_buffer[port_index].recv_pos_pointer;
}

// kni发送缓冲区头部指针位置
void *cgo_kni_mem_send_head_pointer() {
    return kni_ring_buffer.mem_send_head;
}

// kni接收缓冲区头部指针位置
void *cgo_kni_mem_recv_head_pointer() {
    return kni_ring_buffer.mem_recv_head;
}

// kni发送缓冲区当前已读指针位置
void **cgo_kni_send_pos_pointer_addr() {
    return (void **) &kni_ring_buffer.send_pos_pointer;
}

// kni接收缓冲区当前已读指针位置
void **cgo_kni_recv_pos_pointer_addr() {
    return (void **) &kni_ring_buffer.recv_pos_pointer;
}

// 写入接收缓冲区
void write_recv_mem(struct ring_buffer *buffer, const uint8_t *data, const uint16_t len) {
    if (buffer->mem_recv_cur == NULL) {
        buffer->mem_recv_cur = buffer->mem_recv_head;
    }
    if (len > 1514) {
        return;
    }
    // 4字节头部
    uint8_t head[4] = {(uint8_t) len, (uint8_t) (len >> 8), 0x00, 0x00};
    _Atomic
    uint32_t *head_ptr = (uint32_t *) buffer->mem_recv_cur;
    uint16_t _len = len + 4;
    // 内存对齐
    uint8_t aling_size = _len % 4;
    if (aling_size != 0) {
        aling_size = 4 - aling_size;
    }
    _len += aling_size;
    const int32_t overflow = (int32_t) buffer->mem_recv_cur + (int32_t) _len - ((int32_t) buffer->mem_recv_head + (int32_t) buffer->size);
    if (debug) {
        printf("[write_recv_mem] buffer: %p, overflow: %d, len: %d, mem_recv_cur: %p, mem_recv_head: %p, recv_pos_pointer: %p\n", buffer, overflow, _len,
               buffer->mem_recv_cur, buffer->mem_recv_head, buffer->recv_pos_pointer);
    }
    if (overflow >= 0) {
        // 有溢出
        if (buffer->mem_recv_cur < buffer->recv_pos_pointer) {
            // 已经处于读写指针交叉状态 丢弃数据
            return;
        }
        if (overflow >= buffer->recv_pos_pointer - buffer->mem_recv_head) {
            // 即使进入读写指针交叉状态 剩余内存依然不足 丢弃数据
            return;
        }
        // 写入头部 原子操作
        uint32_t head_u32 = ((uint32_t) head[0] << 0) + ((uint32_t) head[1] << 8) + ((uint32_t) head[2] << 16) + ((uint32_t) head[3] << 24);
        atomic_store(head_ptr, head_u32);
        if (_len - overflow > 4) {
            // 拷贝前半段数据
            memcpy(buffer->mem_recv_cur + 4, data, len - overflow);
        }
        buffer->mem_recv_cur = buffer->mem_recv_head;
        if (overflow > 0) {
            // 拷贝后半段数据
            memcpy(buffer->mem_recv_cur, data + (len - overflow), overflow);
            buffer->mem_recv_cur += overflow;
            if (buffer->mem_recv_cur >= buffer->mem_recv_head + (int32_t) buffer->size) {
                buffer->mem_recv_cur = buffer->mem_recv_head;
            }
        }
    } else {
        // 无溢出
        if (buffer->mem_recv_cur < buffer->recv_pos_pointer && buffer->mem_recv_cur + _len >= buffer->recv_pos_pointer) {
            // 状态下剩余内存不足 丢弃数据
            return;
        }
        // 写入头部 原子操作
        uint32_t head_u32 = ((uint32_t) head[0] << 0) + ((uint32_t) head[1] << 8) + ((uint32_t) head[2] << 16) + ((uint32_t) head[3] << 24);
        atomic_store(head_ptr, head_u32);
        memcpy(buffer->mem_recv_cur + 4, data, len);
        buffer->mem_recv_cur += _len;
        if (buffer->mem_recv_cur >= buffer->mem_recv_head + (int32_t) buffer->size) {
            buffer->mem_recv_cur = buffer->mem_recv_head;
        }
    }
    // 将后面的数据长度与写入完成标识置为0x00
    _Atomic
    uint32_t *next_head_ptr = (uint32_t *) buffer->mem_recv_cur;
    atomic_store(next_head_ptr, 0x00);
    // 修改写入完成标识 原子操作
    head[2] = 0x01;
    uint32_t head_u32 = ((uint32_t) head[0] << 0) + ((uint32_t) head[1] << 8) + ((uint32_t) head[2] << 16) + ((uint32_t) head[3] << 24);
    atomic_store(head_ptr, head_u32);
}

// 读取发送缓冲区
void read_send_mem(struct ring_buffer *buffer, uint8_t *data, uint16_t *len) {
    if (buffer->mem_send_cur == NULL) {
        buffer->mem_send_cur = buffer->mem_send_head;
    }
    *len = 0;
    // 读取头部 原子操作
    _Atomic
    uint32_t *head_ptr = (uint32_t *) buffer->mem_send_cur;
    const uint32_t head_u32 = atomic_load(head_ptr);
    *len = (uint16_t) head_u32;
    if (*len == 0) {
        // 没有新数据
        return;
    }
    const uint8_t finish_flag = head_u32 >> 16;
    if (finish_flag == 0x00) {
        // 数据尚未写入完成
        *len = 0;
        return;
    }
    buffer->mem_send_cur += 4;
    if (buffer->mem_send_cur >= buffer->mem_send_head + (int32_t) buffer->size) {
        buffer->mem_send_cur = buffer->mem_send_head;
    }
    const int32_t overflow = (int32_t) buffer->mem_send_cur + (int32_t) *len - ((int32_t) buffer->mem_send_head + (int32_t) buffer->size);
    if (debug) {
        printf("[read_send_mem] buffer: %p, overflow: %d, len: %d, mem_send_cur: %p, mem_send_head: %p, send_pos_pointer: %p\n", buffer, overflow, *len,
               buffer->mem_send_cur, buffer->mem_send_head, buffer->send_pos_pointer);
    }
    if (overflow >= 0) {
        // 拷贝前半段数据
        memcpy(data, buffer->mem_send_cur, *len - overflow);
        buffer->mem_send_cur = buffer->mem_send_head;
        if (overflow > 0) {
            // 拷贝后半段数据
            memcpy(data + (*len - overflow), buffer->mem_send_cur, overflow);
            // 内存对齐
            uint8_t aling_size = overflow % 4;
            if (aling_size != 0) {
                aling_size = 4 - aling_size;
            }
            buffer->mem_send_cur += overflow + aling_size;
            if (buffer->mem_send_cur >= buffer->mem_send_head + (int32_t) buffer->size) {
                buffer->mem_send_cur = buffer->mem_send_head;
            }
        }
    } else {
        memcpy(data, buffer->mem_send_cur, *len);
        // 内存对齐
        uint8_t aling_size = *len % 4;
        if (aling_size != 0) {
            aling_size = 4 - aling_size;
        }
        buffer->mem_send_cur += *len + aling_size;
        if (buffer->mem_send_cur >= buffer->mem_send_head + (int32_t) buffer->size) {
            buffer->mem_send_cur = buffer->mem_send_head;
        }
    }
    _Atomic
    uint64_t *send_pos_pointer = (uint64_t *) &buffer->send_pos_pointer;
    atomic_store(send_pos_pointer, (uint64_t)buffer->mem_send_cur);
}

static int kni_change_mtu(uint16_t port_id, unsigned int new_mtu) {
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
    uint8_t nb_ports = rte_eth_dev_count();
    if (port_id < 0 || port_id >= nb_ports) {
        RTE_LOG(ERR, APP, "port is not right\n");
        return -1;
    }

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
    struct rte_mbuf *bufs_recv[BURST_SIZE];
    const uint16_t nb_rx = rte_eth_rx_burst(port_id, 0, bufs_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(nb_rx == 0)) {
            return false;
        }
    }

    // 有网卡接收数据
    if (likely(nb_rx != 0)) {
        for (int i = 0; i < nb_rx; i++) {
            const uint8_t *pktbuf = rte_pktmbuf_mtod(bufs_recv[i], uint8_t *);
            if (debug) {
                // 打印网卡接收到的原始数据
                printf("[nic recv], port_index: %d, len: %d, data: ", port_index, bufs_recv[i]->data_len);
                for (int j = 0; j < bufs_recv[i]->data_len; j++) {
                    printf("%02x", pktbuf[j]);
                }
                printf("\n\n");
            }
            // 环状缓冲区数据发送
            const uint16_t ring_send_len = bufs_recv[i]->data_len;
            write_recv_mem(&port_ring_buffer[port_index], pktbuf, ring_send_len);
            rte_pktmbuf_free(bufs_recv[i]);
        }
    }
    return true;
}

bool kni_rx() {
    // KNI数据接收
    struct rte_mbuf *bufs_kni_recv[BURST_SIZE];
    const uint16_t nb_kni_rx = rte_kni_rx_burst(kni, bufs_kni_recv, BURST_SIZE);

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(nb_kni_rx == 0)) {
            return false;
        }
    }

    // 有KNI接收数据
    if (likely(nb_kni_rx != 0)) {
        for (int i = 0; i < nb_kni_rx; i++) {
            const uint8_t *pktbuf = rte_pktmbuf_mtod(bufs_kni_recv[i], uint8_t *);
            if (debug) {
                // 打印KNI接收到的原始数据
                printf("[kni recv], len: %d, data: ", bufs_kni_recv[i]->data_len);
                for (int j = 0; j < bufs_kni_recv[i]->data_len; j++) {
                    printf("%02x", pktbuf[j]);
                }
                printf("\n\n");
            }
            // KNI环状缓冲区数据发送
            const uint16_t ring_send_len = bufs_kni_recv[i]->data_len;
            write_recv_mem(&kni_ring_buffer, pktbuf, ring_send_len);
            rte_pktmbuf_free(bufs_kni_recv[i]);
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
            usleep(1000 * 1);
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
    uint8_t ring_recv_buf[BURST_SIZE][1514];
    uint16_t ring_recv_buf_len[BURST_SIZE];
    int ring_recv_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        read_send_mem(&port_ring_buffer[port_index], ring_recv_buf[i], &ring_recv_buf_len[i]);
        if (unlikely(ring_recv_buf_len[i] == 0)) {
            break;
        }
        ring_recv_size++;
    }

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(ring_recv_size == 0)) {
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
                    printf("%02x", ring_recv_buf[i][j]);
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
        const uint16_t nb_tx = rte_eth_tx_burst(port_id, 0, bufs_send, ring_recv_size);

        if (unlikely(nb_tx < ring_recv_size)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_tx; i < ring_recv_size; i++) {
                rte_pktmbuf_free(bufs_send[i]);
            }
        }
    }

    return true;
}

bool kni_tx() {
    // KNI环状缓冲区数据接收
    uint8_t kni_ring_recv_buf[BURST_SIZE][1514];
    uint16_t kni_ring_recv_buf_len[BURST_SIZE];
    int kni_ring_recv_size = 0;
    for (int i = 0; i < BURST_SIZE; i++) {
        read_send_mem(&kni_ring_buffer, kni_ring_recv_buf[i], &kni_ring_recv_buf_len[i]);
        if (unlikely(kni_ring_recv_buf_len[i] == 0)) {
            break;
        }
        kni_ring_recv_size++;
    }

    if (idle_sleep) {
        // 无包时短暂睡眠节省CPU资源
        if (unlikely(kni_ring_recv_size == 0)) {
            return false;
        }
    }

    // 有KNI环状缓冲区数据
    if (likely(kni_ring_recv_size > 0)) {
        if (debug) {
            // 打印KNI环状缓冲区数据
            for (int i = 0; i < kni_ring_recv_size; i++) {
                printf("[kni ring recv], len: %d, data: ", kni_ring_recv_buf_len[i]);
                for (int j = 0; j < kni_ring_recv_buf_len[i]; j++) {
                    printf("%02x", kni_ring_recv_buf[i][j]);
                }
                printf("\n\n");
            }
        }

        struct rte_mbuf *bufs_send[BURST_SIZE];

        // 数据拷贝
        for (int i = 0; i < kni_ring_recv_size; i++) {
            bufs_send[i] = rte_pktmbuf_alloc(mbuf_pool);
            uint8_t *send_data = (uint8_t *) rte_pktmbuf_append(bufs_send[i], kni_ring_recv_buf_len[i] * sizeof(uint8_t));
            memcpy(send_data, kni_ring_recv_buf[i], kni_ring_recv_buf_len[i]);
        }

        // KNI数据发送
        uint16_t nb_kni_tx = rte_kni_tx_burst(kni, bufs_send, kni_ring_recv_size);

        if (unlikely(nb_kni_tx < kni_ring_recv_size)) {
            // 把没发送成功的mbuf释放掉
            for (int i = nb_kni_tx; i < kni_ring_recv_size; i++) {
                rte_pktmbuf_free(bufs_send[i]);
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
            usleep(1000 * 1);
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
            usleep(1000 * 1);
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

    const uint8_t nb_ports = rte_eth_dev_count();
    for (int i = 0; i < nb_ports; i++) {
        char dev_name[RTE_DEV_NAME_MAX_LEN];
        rte_eth_dev_get_name_by_port(i, dev_name);
        printf("port number: %d, port pci: %s, ", i, dev_name);
        struct ether_addr dev_eth_addr = {0};
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
        port_ring_buffer[i].size = ring_buffer_size;
    }
    // kni分配环状缓冲区内存
    kni_ring_buffer.mem_send_head = rte_malloc("send_ring_buffer", ring_buffer_size, 0);
    if (kni_ring_buffer.mem_send_head == NULL) {
        rte_exit(EXIT_FAILURE, "send ring buffer create failed\n");
    }
    kni_ring_buffer.mem_send_cur = NULL;
    kni_ring_buffer.mem_recv_head = rte_malloc("recv_ring_buffer", ring_buffer_size, 0);
    if (kni_ring_buffer.mem_recv_head == NULL) {
        rte_exit(EXIT_FAILURE, "recv ring buffer create failed\n");
    }
    kni_ring_buffer.mem_recv_cur = NULL;
    kni_ring_buffer.send_pos_pointer = kni_ring_buffer.mem_send_head;
    kni_ring_buffer.recv_pos_pointer = kni_ring_buffer.mem_recv_head;
    kni_ring_buffer.size = ring_buffer_size;
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
        rte_free(port_ring_buffer[i].mem_send_head);
        rte_free(port_ring_buffer[i].mem_recv_head);
    }
    free(port_id_list);
    free(port_ring_buffer);
    free(port_old_stats);

    return 0;
}
