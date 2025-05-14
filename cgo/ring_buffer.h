#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdatomic.h>
#include <stdbool.h>

#define CACHE_LINE_SIZE 64
#define ALIGNED __attribute__((aligned(CACHE_LINE_SIZE)))

// 环形缓冲区结构体
typedef struct {
    _Atomic uint64_t head ALIGNED; // 写入位置
    _Atomic uint64_t tail ALIGNED; // 读取位置

    uint32_t size; // 缓冲区总大小 必须为2的幂
    uint32_t mask; // 用于快速取模的掩码
    uint8_t *buffer; // 指向实际数据缓冲区的指针
} ring_buffer_t;

// 创建环形缓冲区
ring_buffer_t *ring_buffer_create(void *mem, const uint32_t size) {
    if (!mem) {
        return NULL;
    }
    // 确保size是2的幂
    if ((size & size - 1) != 0) {
        return NULL;
    }

    ring_buffer_t *rb = malloc(sizeof(ring_buffer_t));
    if (!rb) {
        return NULL;
    }

    rb->head = 0;
    rb->tail = 0;
    rb->size = size;
    rb->mask = size - 1;
    rb->buffer = mem;

    return rb;
}

// 销毁环形缓冲区
void ring_buffer_destroy(ring_buffer_t *rb) {
    if (rb) {
        free(rb);
    }
}

// 计算可用空间
uint32_t ring_buffer_free_space(const ring_buffer_t *rb) {
    const uint64_t head = atomic_load(&rb->head);
    const uint64_t tail = atomic_load(&rb->tail);
    return rb->size - (uint32_t) (head - tail);
}

// 计算已用空间
uint32_t ring_buffer_used_space(const ring_buffer_t *rb) {
    const uint64_t head = atomic_load(&rb->head);
    const uint64_t tail = atomic_load(&rb->tail);
    return head - tail;
}

// 写入数据包
bool write_packet(ring_buffer_t *rb, const uint8_t *data, const uint16_t len) {
    if (len == 0 || len > rb->size / 2) {
        return false;
    }

    uint32_t total_size = sizeof(uint16_t) + len;
    // 4字节对齐
    total_size = total_size + 3 & ~3;

    if (ring_buffer_free_space(rb) < total_size) {
        return false;
    }

    const uint64_t head = atomic_load(&rb->head);
    const uint32_t pos = head & rb->mask;

    // 写入长度
    *(uint16_t *) (rb->buffer + pos) = len;

    // 写入数据
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;

    if (space_after >= len) {
        memcpy(rb->buffer + data_pos, data, len);
    } else {
        memcpy(rb->buffer + data_pos, data, space_after);
        memcpy(rb->buffer, data + space_after, len - space_after);
    }

    // 更新head
    atomic_store(&rb->head, head + total_size);

    return true;
}

// 读取数据包
bool read_packet(ring_buffer_t *rb, uint8_t *data, uint16_t *len) {
    if (ring_buffer_used_space(rb) < sizeof(uint16_t)) {
        return false;
    }

    const uint64_t tail = atomic_load(&rb->tail);
    const uint32_t pos = tail & rb->mask;

    // 读取长度
    const uint16_t packet_len = *(uint16_t *) (rb->buffer + pos);

    if (packet_len == 0 || packet_len > rb->size / 2) {
        return false;
    }

    uint32_t total_size = sizeof(uint16_t) + packet_len;
    // 4字节对齐
    total_size = total_size + 3 & ~3;

    if (ring_buffer_used_space(rb) < total_size) {
        return false;
    }

    // 读取数据
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;

    if (space_after >= packet_len) {
        memcpy(data, rb->buffer + data_pos, packet_len);
    } else {
        memcpy(data, rb->buffer + data_pos, space_after);
        memcpy(data + space_after, rb->buffer, packet_len - space_after);
    }

    *len = packet_len;

    // 更新tail
    atomic_store(&rb->tail, tail + total_size);

    return true;
}
