package ring

import (
	"errors"
)

var ErrTooLarge = errors.New("ring: write too large")

// Buffer 是无锁的单生产者单消费者环形字节缓冲。
// 为简化，本实现以调用方控制并发；在 poller 线程中使用。

type Buffer struct {
	buf      []byte
	mask     int
	readPos  int
	writePos int
}

// New 返回容量为 2 的幂次的环形缓冲。若 cap 非 2 的幂则向上取整。
func New(capacity int) *Buffer {
	capPow2 := 1
	for capPow2 < capacity {
		capPow2 <<= 1
	}
	return &Buffer{buf: make([]byte, capPow2), mask: capPow2 - 1}
}

func (b *Buffer) Cap() int { return len(b.buf) }

func (b *Buffer) Len() int { return b.writePos - b.readPos }

func (b *Buffer) Free() int { return b.Cap() - b.Len() }

// Write 将数据写入环形缓冲；当数据长度超过剩余空间时返回错误。
func (b *Buffer) Write(p []byte) (int, error) {
	if len(p) > b.Free() {
		return 0, ErrTooLarge
	}
	n := len(p)
	start := b.writePos & b.mask
	end := start + n
	if end <= len(b.buf) {
		copy(b.buf[start:end], p)
	} else {
		l := len(b.buf) - start
		copy(b.buf[start:], p[:l])
		copy(b.buf[:end-l], p[l:])
	}
	b.writePos += n
	return n, nil
}

// Peek 读取最多 n 字节但不前进读指针。
func (b *Buffer) Peek(n int) []byte {
	if n <= 0 {
		return nil
	}
	ln := b.Len()
	if n > ln {
		n = ln
	}
	start := b.readPos & b.mask
	end := start + n
	if end <= len(b.buf) {
		return b.buf[start:end]
	}
	// 分段视图需要拷贝为连续切片
	buf := make([]byte, n)
	l := len(b.buf) - start
	copy(buf[:l], b.buf[start:])
	copy(buf[l:], b.buf[:end-l])
	return buf
}

// Discard 前进读指针。
func (b *Buffer) Discard(n int) int {
	ln := b.Len()
	if n > ln {
		n = ln
	}
	b.readPos += n
	return n
}
