#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdatomic.h>
#include <stdbool.h>

#define CACHE_LINE_SIZE 64
#define ALIGNED __attribute__((aligned(CACHE_LINE_SIZE)))

// ring_buffer_t 表示单生产者单消费者环形报文缓冲区
typedef struct {
    _Atomic
    uint64_t head ALIGNED; // 写入位置
    _Atomic
    uint64_t tail ALIGNED; // 读取位置
    uint32_t size; // 数据区字节大小 必须为 2 的幂
    uint32_t mask; // 环形索引掩码
    uint8_t *buffer; // 数据区地址
} ring_buffer_t;

// ring_buffer_create 在指定内存上初始化环形缓冲区
ring_buffer_t *ring_buffer_create(void *memory, uint32_t size) {
    if (!memory) {
        return NULL;
    }
    const uint32_t header_size = sizeof(ring_buffer_t);
    if (size < header_size) {
        return NULL;
    }
    size -= header_size;
    // 数据区必须为 2 的幂才能使用掩码完成环绕
    if ((size & size - 1) != 0) {
        return NULL;
    }

    ring_buffer_t *rb = memory;
    rb->head = 0;
    rb->tail = 0;
    rb->size = size;
    rb->mask = size - 1;
    rb->buffer = memory + sizeof(ring_buffer_t);

    // 对齐填充区写入固定标记供共享内存映射校验
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

// ring_buffer_destroy 清空环形缓冲区元数据
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

// ring_buffer_mapping 映射已有环形缓冲区并计算数据区地址偏移
ring_buffer_t *ring_buffer_mapping(void *memory, int64_t *offset) {
    if (!memory) {
        return NULL;
    }
    ring_buffer_t *rb = memory;

    // 标记不完整说明目标内存尚未初始化或布局不兼容
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

    // 共享内存映射地址可能变化 使用偏移修正创建进程保存的指针
    *offset = memory + sizeof(ring_buffer_t) - (void *) rb->buffer;
    return rb;
}

// write_packet_offset 使用指定数据区地址偏移写入报文
bool write_packet_offset(ring_buffer_t *rb, const int64_t offset, const uint8_t *data, const uint16_t len) {
    if (len == 0 || len > rb->size / 2) {
        return false;
    }
    // SPSC 模型使用单调递增位置计数 掩码只负责访问数据区
    const uint64_t head = atomic_load(&rb->head);
    const uint64_t tail = atomic_load(&rb->tail);
    const uint32_t free_space = rb->size - (uint32_t) (head - tail);
    uint32_t total_size = sizeof(uint16_t) + len;
    // 每条记录由长度和载荷组成并按 4 字节对齐
    total_size = total_size + 3 & ~3;
    if (free_space < total_size) {
        return false;
    }
    const uint32_t pos = head & rb->mask;
    // 写入长度
    *(uint16_t *) (rb->buffer + offset + pos) = len;
    // 载荷跨越数据区末尾时拆成两段复制
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;
    if (space_after >= len) {
        memcpy(rb->buffer + offset + data_pos, data, len);
    } else {
        memcpy(rb->buffer + offset + data_pos, data, space_after);
        memcpy(rb->buffer + offset, data + space_after, len - space_after);
    }
    // 载荷复制完成后再发布 head 防止消费者读取半条记录
    atomic_store(&rb->head, head + total_size);
    return true;
}

// write_packet 向环形缓冲区写入报文
bool write_packet(ring_buffer_t *rb, const uint8_t *data, const uint16_t len) {
    return write_packet_offset(rb, 0, data, len);
}

// read_packet_offset 使用指定数据区地址偏移读取报文
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
    // 与写入端使用相同的 4 字节记录对齐规则
    total_size = total_size + 3 & ~3;
    // 记录尚未完整发布时等待下一次读取
    if (used_space < total_size) {
        return false;
    }
    // 载荷跨越数据区末尾时拆成两段复制
    const uint32_t data_pos = pos + sizeof(uint16_t) & rb->mask;
    const uint32_t space_after = rb->size - data_pos;
    if (space_after >= packet_len) {
        memcpy(data, rb->buffer + offset + data_pos, packet_len);
    } else {
        memcpy(data, rb->buffer + offset + data_pos, space_after);
        memcpy(data + space_after, rb->buffer + offset, packet_len - space_after);
    }
    *len = packet_len;
    // 载荷复制完成后再发布 tail 允许生产者复用空间
    atomic_store(&rb->tail, tail + total_size);
    return true;
}

// read_packet 从环形缓冲区读取报文
bool read_packet(ring_buffer_t *rb, uint8_t *data, uint16_t *len) {
    return read_packet_offset(rb, 0, data, len);
}
