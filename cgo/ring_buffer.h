#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdatomic.h>
#include <stdbool.h>

#define CACHE_LINE_SIZE 64
#define ALIGNED __attribute__((aligned(CACHE_LINE_SIZE)))

// 环形缓冲区结构体
typedef struct {
    _Atomic
    uint64_t head ALIGNED; // 写入位置
    _Atomic
    uint64_t tail ALIGNED; // 读取位置
    uint32_t size; // 缓冲区总大小 必须为2的幂
    uint32_t mask; // 用于快速取模的掩码
    uint8_t *buffer; // 指向实际数据缓冲区的指针
} ring_buffer_t;

// 创建环形缓冲区
ring_buffer_t *ring_buffer_create(void *memory, uint32_t size) {
    if (!memory) {
        return NULL;
    }
    const uint32_t header_size = sizeof(ring_buffer_t);
    if (size < header_size) {
        return NULL;
    }
    size -= header_size;
    // 确保size是2的幂
    if ((size & size - 1) != 0) {
        return NULL;
    }

    ring_buffer_t *rb = memory;
    rb->head = 0;
    rb->tail = 0;
    rb->size = size;
    rb->mask = size - 1;
    rb->buffer = memory + sizeof(ring_buffer_t);

    for (int i = 8; i <= 63; i++) {
        uint8_t *v = (uint8_t *) rb + i;
        *v = 0xAA;
    }
    for (int i = 88; i <= 127; i++) {
        uint8_t *v = (uint8_t *) rb + i;
        *v = 0xFF;
    }

    return rb;
}

// 销毁环形缓冲区
void ring_buffer_destroy(ring_buffer_t *rb) {
    if (rb) {
        rb->head = 0;
        rb->tail = 0;
        rb->size = 0;
        rb->mask = 0;
        rb->buffer = NULL;

        for (int i = 8; i <= 63; i++) {
            uint8_t *v = (uint8_t *) rb + i;
            *v = 0x00;
        }
        for (int i = 88; i <= 127; i++) {
            uint8_t *v = (uint8_t *) rb + i;
            *v = 0x00;
        }
    }
}

// 映射环形缓冲区
ring_buffer_t *ring_buffer_mapping(void *memory, int64_t *offset) {
    if (!memory) {
        return NULL;
    }
    ring_buffer_t *rb = memory;

    for (int i = 8; i <= 63; i++) {
        const uint8_t *v = (uint8_t *) rb + i;
        if (*v != 0xAA) {
            return NULL;
        }
    }
    for (int i = 88; i <= 127; i++) {
        const uint8_t *v = (uint8_t *) rb + i;
        if (*v != 0xFF) {
            return NULL;
        }
    }

    *offset = memory + sizeof(ring_buffer_t) - (void *) rb->buffer;
    return rb;
}

// 偏移写入数据包
bool write_packet_offset(ring_buffer_t *rb, const int64_t offset, const uint8_t *data, const uint16_t len) {
    if (len == 0 || len > rb->size / 2) {
        return false;
    }
    const uint64_t head = atomic_load(&rb->head);
    const uint64_t tail = atomic_load(&rb->tail);
    const uint32_t free_space = rb->size - (uint32_t) (head - tail);
    uint32_t total_size = sizeof(uint16_t) + len;
    // 4字节对齐
    total_size = total_size + 3 & ~3;
    if (free_space < total_size) {
        return false;
    }
    const uint32_t pos = head & rb->mask;
    // 写入长度
    *(uint16_t *) (rb->buffer + offset + pos) = len;
    // 写入数据
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;
    if (space_after >= len) {
        memcpy(rb->buffer + offset + data_pos, data, len);
    } else {
        memcpy(rb->buffer + offset + data_pos, data, space_after);
        memcpy(rb->buffer + offset, data + space_after, len - space_after);
    }
    // 更新head
    atomic_store(&rb->head, head + total_size);
    return true;
}

// 写入数据包
bool write_packet(ring_buffer_t *rb, const uint8_t *data, const uint16_t len) {
    return write_packet_offset(rb, 0, data, len);
}

// 偏移读取数据包
bool read_packet_offset(ring_buffer_t *rb, const int64_t offset, uint8_t *data, uint16_t *len) {
    *len = 0;
    const uint64_t head = atomic_load(&rb->head);
    const uint64_t tail = atomic_load(&rb->tail);
    const uint32_t used_space = head - tail;
    if (used_space < sizeof(uint16_t)) {
        return false;
    }
    const uint32_t pos = tail & rb->mask;
    // 读取长度
    const uint16_t packet_len = *(uint16_t *) (rb->buffer + offset + pos);
    if (packet_len == 0 || packet_len > rb->size / 2) {
        return false;
    }
    uint32_t total_size = sizeof(uint16_t) + packet_len;
    // 4字节对齐
    total_size = total_size + 3 & ~3;
    if (used_space < total_size) {
        return false;
    }
    // 读取数据
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;
    if (space_after >= packet_len) {
        memcpy(data, rb->buffer + offset + data_pos, packet_len);
    } else {
        memcpy(data, rb->buffer + offset + data_pos, space_after);
        memcpy(data + space_after, rb->buffer + offset, packet_len - space_after);
    }
    *len = packet_len;
    // 更新tail
    atomic_store(&rb->tail, tail + total_size);
    return true;
}

// 读取数据包
bool read_packet(ring_buffer_t *rb, uint8_t *data, uint16_t *len) {
    return read_packet_offset(rb, 0, data, len);
}
