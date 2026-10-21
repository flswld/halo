package tcp

type byteBuffer struct {
	data []byte
	head int
	size int
}

func (b *byteBuffer) write(p []byte) int {
	n := min(len(p), len(b.data)-b.size)
	tail := (b.head + b.size) % len(b.data)
	first := min(n, len(b.data)-tail)
	copy(b.data[tail:], p[:first])
	copy(b.data, p[first:n])
	b.size += n
	return n
}

func (b *byteBuffer) peek(n int) []byte {
	n = min(n, b.size)
	p := make([]byte, n)
	first := min(n, len(b.data)-b.head)
	copy(p, b.data[b.head:b.head+first])
	copy(p[first:], b.data[:n-first])
	return p
}

func (b *byteBuffer) discard(n int) {
	b.head = (b.head + n) % len(b.data)
	b.size -= n
}

func (b *byteBuffer) read(p []byte) int {
	n := min(len(p), b.size)
	first := min(n, len(b.data)-b.head)
	copy(p, b.data[b.head:b.head+first])
	copy(p[first:n], b.data[:n-first])
	b.discard(n)
	return n
}
