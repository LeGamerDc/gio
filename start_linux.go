//go:build linux

package gio

import (
	"context"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// Start 在 Linux 上启动服务：创建 listener 并进入单 poller 事件循环（骨架）
func Start[C Cipher](cfg Config[C], h Handler[C]) error {
	s, err := NewServer(cfg, h)
	if err != nil {
		return err
	}
	ctx := context.Background()
	return s.Start(ctx)
}

// Start 启动 Server（M1：单 poller 骨架）
func (s *Server[C]) Start(ctx context.Context) error {
	if s.cfg.Address == "" {
		s.cfg.Address = ":0"
	}
	l, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return err
	}
	defer l.Close()

	// 将 net.Listener 断言为 *net.TCPListener
	tl, ok := l.(*net.TCPListener)
	if !ok {
		return ErrInvalidArgument
	}

	// 通过 SyscallConn 设置 O_NONBLOCK
	if rc, err := tl.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			_ = unix.SetNonblock(int(fd), true)
		})
	}

	// 单 poller 事件循环（占位调用）
	p, err := newPoller()
	if err != nil {
		return err
	}
	defer p.close()

	// 将 listener 文件描述符注册到 epoll（边缘触发）
	if err := p.attachListener(tl); err != nil {
		return err
	}

	// 运行事件循环（阻塞，直到 ctx 取消或错误）
	return run(p, ctx, s, tl)
}

// 保证引用 os 包，避免未使用错误（在不同构建路径下）
var _ = os.ErrClosed
