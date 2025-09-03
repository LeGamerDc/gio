package poller

import "net"

// FD 表示文件描述符。
type FD = int

// Handler 是 poller 的事件回调接口。
// 在对应的 poller goroutine 中调用，要求无阻塞返回。

type Handler interface {
	OnReadable(fd FD)
	OnWritable(fd FD)
	OnClose(fd FD, err error)
}

// Poller 提供注册/事件循环。

type Poller interface {
	Register(fd FD, readable, writable bool) error
	Mod(fd FD, readable, writable bool) error
	Unregister(fd FD) error
	Run(h Handler) error
	Wake() error
	Close() error
}

// ListenerFactory 用于创建支持 SO_REUSEPORT 的多监听。

type ListenerFactory interface {
	Listen(network, address string, reusePort bool) (net.Listener, error)
}
