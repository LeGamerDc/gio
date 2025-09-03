package gio

import "context"

// Server 为服务端实例（平台无关的最小骨架）
// 具体的 poller/epoll 实现在平台相关文件中提供
type Server[C Cipher] struct {
	cfg        Config[C]
	handler    Handler[C]
	nextConnID uint64
}

// NewServer 构造未启动的 Server 实例
func NewServer[C Cipher](cfg Config[C], h Handler[C]) (*Server[C], error) {
	if h == nil {
		return nil, ErrInvalidArgument
	}
	s := &Server[C]{
		cfg:     cfg,
		handler: h,
	}
	return s, nil
}

// Stop 停止服务端（M1 骨架占位）
func (s *Server[C]) Stop(ctx context.Context) error {
	_ = ctx // 未来用于优雅关闭
	return nil
}
