#ifndef HALO_RING_BUFFER_H
#define HALO_RING_BUFFER_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

#define CACHE_LINE_SIZE 64 // CPU 缓存行字节数
#define ALIGNED __attribute__((aligned(CACHE_LINE_SIZE))) // 按缓存行边界对齐
#define RING_BUFFER_LAYOUT_VERSION 1u // Go/C 共享内存布局版本
#define RING_BUFFER_RECORD_HEADER_SIZE ((uint64_t) sizeof(uint32_t)) // 单条记录的 uint32 长度头字节数
#define RING_BUFFER_RECORD_ALIGN UINT64_C(4) // 单条记录的字节对齐粒度
#define RING_BUFFER_MIN_DATA_SIZE (RING_BUFFER_RECORD_HEADER_SIZE + RING_BUFFER_RECORD_ALIGN) // 环形缓冲区数据区最小字节数
#define RING_BUFFER_MAX_DATA_SIZE (UINT64_C(1) << 62) // 环形缓冲区数据区最大字节数

// ring_buffer_t 表示单生产者单消费者环形报文缓冲区
typedef struct {
    _Atomic uint64_t head ALIGNED; // 写入位置
    _Atomic uint64_t tail ALIGNED; // 读取位置
    uint64_t size; // 数据区字节大小
    uint64_t mask; // 环形索引掩码
    uint8_t *buffer; // 数据区地址
} ring_buffer_t;

// ring_buffer_producer_t 保存单个生产者独占的本地游标
typedef struct ALIGNED {
    ring_buffer_t *rb; // 关联的环形缓冲区
    uint8_t *buffer; // 当前映射中的数据区地址
    uint64_t head; // 生产者本地写入位置
    uint64_t cached_tail; // 缓存的消费者读取位置
    uint8_t reserved[32]; // 独占缓存行填充
} ring_buffer_producer_t;

// ring_buffer_consumer_t 保存单个消费者独占的本地游标
typedef struct ALIGNED {
    ring_buffer_t *rb; // 关联的环形缓冲区
    uint8_t *buffer; // 当前映射中的数据区地址
    uint64_t tail; // 消费者本地读取位置
    uint64_t cached_head; // 缓存的生产者写入位置
    uint8_t reserved[32]; // 独占缓存行填充
} ring_buffer_consumer_t;

_Static_assert(sizeof(ring_buffer_t) == 128, "ring_buffer_t size must match Go RingBuffer");
_Static_assert(offsetof(ring_buffer_t, head) == 0, "ring_buffer_t head offset mismatch");
_Static_assert(offsetof(ring_buffer_t, tail) == 64, "ring_buffer_t tail offset mismatch");
_Static_assert(offsetof(ring_buffer_t, size) == 72, "ring_buffer_t size offset mismatch");
_Static_assert(offsetof(ring_buffer_t, mask) == 80, "ring_buffer_t mask offset mismatch");
_Static_assert(offsetof(ring_buffer_t, buffer) == 88, "ring_buffer_t buffer offset mismatch");
_Static_assert(sizeof(ring_buffer_producer_t) == CACHE_LINE_SIZE, "ring_buffer_producer_t size mismatch");
_Static_assert(sizeof(ring_buffer_consumer_t) == CACHE_LINE_SIZE, "ring_buffer_consumer_t size mismatch");
_Static_assert(_Alignof(ring_buffer_producer_t) == CACHE_LINE_SIZE, "ring_buffer_producer_t alignment mismatch");
_Static_assert(_Alignof(ring_buffer_consumer_t) == CACHE_LINE_SIZE, "ring_buffer_consumer_t alignment mismatch");

// ring_buffer_record_size 返回包含长度头和对齐填充的记录大小
static inline uint64_t ring_buffer_record_size(const uint32_t len) {
    const uint64_t total_size = RING_BUFFER_RECORD_HEADER_SIZE + len;
    return (total_size + RING_BUFFER_RECORD_ALIGN - 1) & ~(RING_BUFFER_RECORD_ALIGN - 1);
}

// ring_buffer_structure_valid 校验环形缓冲区固定元数据
static inline bool ring_buffer_structure_valid(const ring_buffer_t *rb) {
    if (!rb || !rb->buffer) {
        return false;
    }
    if (rb->size < RING_BUFFER_MIN_DATA_SIZE || rb->size > RING_BUFFER_MAX_DATA_SIZE ||
        (rb->size & (rb->size - 1)) != 0 || rb->mask != rb->size - 1) {
        return false;
    }
    return true;
}

// ring_buffer_local_data 解析当前映射中的数据区地址并校验映射偏移
static inline bool ring_buffer_local_data(ring_buffer_t *rb, const int64_t offset, uint8_t **buffer) {
    if (!rb || !rb->buffer || !buffer) {
        return false;
    }
    uint8_t *local_buffer = (uint8_t *) rb + sizeof(ring_buffer_t);
    const uintptr_t local_address = (uintptr_t) local_buffer;
    const uintptr_t stored_address = (uintptr_t) rb->buffer;
    int64_t expected_offset;
    if (local_address >= stored_address) {
        const uint64_t delta = local_address - stored_address;
        if (delta > INT64_MAX) {
            return false;
        }
        expected_offset = (int64_t) delta;
    } else {
        const uint64_t delta = stored_address - local_address;
        if (delta > INT64_MAX) {
            return false;
        }
        expected_offset = -(int64_t) delta;
    }
    if (offset != expected_offset) {
        return false;
    }
    *buffer = local_buffer;
    return true;
}

// ring_buffer_create 在指定内存上初始化环形缓冲区
static inline ring_buffer_t *ring_buffer_create(void *memory, uint64_t size) {
    if (!memory || (uintptr_t) memory % _Alignof(ring_buffer_t) != 0 ||
        !atomic_is_lock_free(&((ring_buffer_t *) memory)->head)) {
        return NULL;
    }
    const uint64_t header_size = sizeof(ring_buffer_t);
    if (size < header_size + RING_BUFFER_MIN_DATA_SIZE || size > header_size + RING_BUFFER_MAX_DATA_SIZE) {
        return NULL;
    }
    size -= header_size;
    if (size < RING_BUFFER_MIN_DATA_SIZE || (size & (size - 1)) != 0) {
        return NULL;
    }

    ring_buffer_t *rb = memory;
    atomic_store_explicit(&rb->head, 0, memory_order_relaxed);
    atomic_store_explicit(&rb->tail, 0, memory_order_relaxed);
    rb->size = size;
    rb->mask = size - 1;
    rb->buffer = (uint8_t *) memory + sizeof(ring_buffer_t);

    // 对齐填充区写入布局版本和固定标记供共享内存映射校验
    *((uint8_t *) rb + 8) = RING_BUFFER_LAYOUT_VERSION;
    for (int i = 9; i <= 63; i++) {
        *((uint8_t *) rb + i) = 0xAA;
    }
    for (int i = 96; i <= 127; i++) {
        *((uint8_t *) rb + i) = 0xFF;
    }

    return rb;
}

// ring_buffer_destroy 清空环形缓冲区元数据
static inline void ring_buffer_destroy(ring_buffer_t *rb) {
    if (!rb) {
        return;
    }
    atomic_store_explicit(&rb->head, 0, memory_order_relaxed);
    atomic_store_explicit(&rb->tail, 0, memory_order_relaxed);
    rb->size = 0;
    rb->mask = 0;
    rb->buffer = NULL;

    for (int i = 8; i <= 63; i++) {
        *((uint8_t *) rb + i) = 0x00;
    }
    for (int i = 96; i <= 127; i++) {
        *((uint8_t *) rb + i) = 0x00;
    }
}

// ring_buffer_mapping 映射已有环形缓冲区并计算数据区地址偏移
static inline ring_buffer_t *ring_buffer_mapping(void *memory, int64_t *offset) {
    if (!memory || !offset) {
        return NULL;
    }
    ring_buffer_t *rb = memory;

    // 版本不匹配说明目标内存仍使用旧长度头或其他不兼容布局
    if (*((uint8_t *) rb + 8) != RING_BUFFER_LAYOUT_VERSION) {
        return NULL;
    }
    for (int i = 9; i <= 63; i++) {
        if (*((uint8_t *) rb + i) != 0xAA) {
            return NULL;
        }
    }
    for (int i = 96; i <= 127; i++) {
        if (*((uint8_t *) rb + i) != 0xFF) {
            return NULL;
        }
    }
    if (!ring_buffer_structure_valid(rb)) {
        return NULL;
    }
    const uint64_t tail = atomic_load_explicit(&rb->tail, memory_order_acquire);
    const uint64_t head = atomic_load_explicit(&rb->head, memory_order_acquire);
    if (head - tail > rb->size) {
        return NULL;
    }

    // 共享内存映射地址可能变化 使用偏移校验创建进程保存的指针
    const uintptr_t local_address = (uintptr_t) ((uint8_t *) memory + sizeof(ring_buffer_t));
    const uintptr_t stored_address = (uintptr_t) rb->buffer;
    if (local_address >= stored_address) {
        const uint64_t delta = local_address - stored_address;
        if (delta > INT64_MAX) {
            return NULL;
        }
        *offset = (int64_t) delta;
    } else {
        const uint64_t delta = stored_address - local_address;
        if (delta > INT64_MAX) {
            return NULL;
        }
        *offset = -(int64_t) delta;
    }
    return rb;
}

// ring_buffer_producer_init 初始化独占指定写入端的生产者上下文
static inline bool ring_buffer_producer_init(ring_buffer_producer_t *producer, ring_buffer_t *rb, const int64_t offset) {
    if (!producer || !ring_buffer_structure_valid(rb)) {
        return false;
    }
    uint8_t *buffer = NULL;
    if (!ring_buffer_local_data(rb, offset, &buffer)) {
        return false;
    }
    const uint64_t tail = atomic_load_explicit(&rb->tail, memory_order_acquire);
    const uint64_t head = atomic_load_explicit(&rb->head, memory_order_relaxed);
    if (head - tail > rb->size) {
        return false;
    }
    producer->rb = rb;
    producer->buffer = buffer;
    producer->head = head;
    producer->cached_tail = tail;
    return true;
}

// ring_buffer_consumer_init 初始化独占指定读取端的消费者上下文
static inline bool ring_buffer_consumer_init(ring_buffer_consumer_t *consumer, ring_buffer_t *rb, const int64_t offset) {
    if (!consumer || !ring_buffer_structure_valid(rb)) {
        return false;
    }
    uint8_t *buffer = NULL;
    if (!ring_buffer_local_data(rb, offset, &buffer)) {
        return false;
    }
    const uint64_t tail = atomic_load_explicit(&rb->tail, memory_order_relaxed);
    const uint64_t head = atomic_load_explicit(&rb->head, memory_order_acquire);
    if (head - tail > rb->size) {
        return false;
    }
    consumer->rb = rb;
    consumer->buffer = buffer;
    consumer->tail = tail;
    consumer->cached_head = head;
    return true;
}

// ring_buffer_producer_write_packet 使用生产者独占上下文写入报文
static inline bool ring_buffer_producer_write_packet(ring_buffer_producer_t *producer, const uint8_t *data, const uint32_t len) {
    if (!producer || !producer->rb || !producer->buffer || !data || len == 0 || len > producer->rb->size / 2) {
        return false;
    }

    const uint64_t head = producer->head;
    uint64_t used_space = head - producer->cached_tail;
    if (used_space > producer->rb->size) {
        producer->cached_tail = atomic_load_explicit(&producer->rb->tail, memory_order_acquire);
        used_space = head - producer->cached_tail;
        if (used_space > producer->rb->size) {
            return false;
        }
    }

    const uint64_t total_size = ring_buffer_record_size(len);
    uint64_t free_space = producer->rb->size - used_space;
    if (free_space < total_size) {
        // 缓存空间不足时才读取对端缓存线
        producer->cached_tail = atomic_load_explicit(&producer->rb->tail, memory_order_acquire);
        used_space = head - producer->cached_tail;
        if (used_space > producer->rb->size || producer->rb->size - used_space < total_size) {
            return false;
        }
    }

    const uint64_t pos = head & producer->rb->mask;
    *(uint32_t *) (producer->buffer + pos) = len;
    const uint64_t data_pos = (pos + RING_BUFFER_RECORD_HEADER_SIZE) & producer->rb->mask;
    const uint64_t space_after = producer->rb->size - data_pos;
    if (space_after >= len) {
        memcpy(producer->buffer + data_pos, data, len);
    } else {
        memcpy(producer->buffer + data_pos, data, space_after);
        memcpy(producer->buffer, data + space_after, len - space_after);
    }

    // 先完成载荷复制再以 release 语义发布 head
    const uint64_t next_head = head + total_size;
    producer->head = next_head;
    atomic_store_explicit(&producer->rb->head, next_head, memory_order_release);
    return true;
}

// ring_buffer_consumer_read_packet 使用消费者独占上下文读取报文
static inline bool ring_buffer_consumer_read_packet(ring_buffer_consumer_t *consumer, uint8_t *data, const uint32_t capacity, uint32_t *len) {
    if (!len) {
        return false;
    }
    *len = 0;
    if (!consumer || !consumer->rb || !consumer->buffer) {
        return false;
    }

    const uint64_t tail = consumer->tail;
    uint64_t used_space = consumer->cached_head - tail;
    if (used_space > consumer->rb->size || used_space < RING_BUFFER_RECORD_HEADER_SIZE) {
        // 缓存无数据或上下文刚挂接运行中的环时读取最新 head
        consumer->cached_head = atomic_load_explicit(&consumer->rb->head, memory_order_acquire);
        used_space = consumer->cached_head - tail;
        if (used_space > consumer->rb->size || used_space < RING_BUFFER_RECORD_HEADER_SIZE) {
            return false;
        }
    }

    const uint64_t pos = tail & consumer->rb->mask;
    const uint32_t packet_len = *(uint32_t *) (consumer->buffer + pos);
    if (packet_len == 0 || packet_len > consumer->rb->size / 2) {
        return false;
    }
    const uint64_t total_size = ring_buffer_record_size(packet_len);
    if (used_space < total_size) {
        consumer->cached_head = atomic_load_explicit(&consumer->rb->head, memory_order_acquire);
        used_space = consumer->cached_head - tail;
        if (used_space > consumer->rb->size || used_space < total_size) {
            return false;
        }
    }
    if (!data || capacity < packet_len) {
        // 返回所需容量并保持 tail 不变 调用方扩容后可以重试
        *len = packet_len;
        return false;
    }

    const uint64_t data_pos = (pos + RING_BUFFER_RECORD_HEADER_SIZE) & consumer->rb->mask;
    const uint64_t space_after = consumer->rb->size - data_pos;
    if (space_after >= packet_len) {
        memcpy(data, consumer->buffer + data_pos, packet_len);
    } else {
        memcpy(data, consumer->buffer + data_pos, space_after);
        memcpy(data + space_after, consumer->buffer, packet_len - space_after);
    }
    *len = packet_len;

    // 数据复制完成后再以 release 语义发布 tail
    const uint64_t next_tail = tail + total_size;
    consumer->tail = next_tail;
    atomic_store_explicit(&consumer->rb->tail, next_tail, memory_order_release);
    return true;
}

#endif
