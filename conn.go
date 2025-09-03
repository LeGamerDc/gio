package gio

import "context"

// Conn 表示一条连接（M1 骨架）
// 后续将由 poller 驱动其读写与状态机
type Conn[C Cipher] struct {
	ID   uint64
	Data C
}

// Context 返回连接关联的 Cipher 上下文指针
func (c *Conn[C]) Context() *C { return &c.Data }

// 写入选项（M1 占位）
type writeOptions struct{}
type WriteOption func(*writeOptions)

// Write 发送一条消息（M1 占位实现）
func (c *Conn[C]) Write(msg []byte, api uint16, opts ...WriteOption) error {
	_ = msg
	_ = api
	_ = opts
	return ErrNotImplemented
}

// Go 将当前消息分流至异步处理（M1 占位）
func (c *Conn[C]) Go(task func(ctx context.Context) error) {
	_ = task
}

// Flush 刷新延迟发送（M1 占位）
func (c *Conn[C]) Flush() error { return ErrNotImplemented }

// Close 关闭连接（M1 占位）
func (c *Conn[C]) Close() error { return ErrNotImplemented }
