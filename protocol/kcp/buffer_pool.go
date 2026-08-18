package kcp

import "sync"

// packetBufferPool 只复用固定容量的报文缓冲区 避免不同 MTU 污染对象池
type packetBufferPool struct {
	capacity int
	pool     sync.Pool
}

// xmitBuf 在 KCP 分片和待发送 UDP 报文之间复用标准 MTU 缓冲区
var xmitBuf = newPacketBufferPool(mtuLimit)

// newPacketBufferPool 创建固定容量的报文缓冲池
func newPacketBufferPool(capacity int) *packetBufferPool {
	pool := &packetBufferPool{capacity: capacity}
	pool.pool.New = func() any {
		return make([]byte, capacity)
	}
	return pool
}

// Get 获取指定长度的缓冲区 超过池容量时直接分配独立缓冲区
func (p *packetBufferPool) Get(size int) []byte {
	if size <= p.capacity {
		return p.pool.Get().([]byte)[:size]
	}
	return make([]byte, size)
}

// Put 仅回收容量完全匹配的缓冲区
func (p *packetBufferPool) Put(buffer []byte) {
	if cap(buffer) == p.capacity {
		p.pool.Put(buffer[:p.capacity])
	}
}
