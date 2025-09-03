//go:build !linux

package gio

import "context"

// Start 在非 Linux 平台返回占位错误，保证编译通过
func Start[C Cipher](cfg Config[C], h Handler[C]) error {
	_, err := NewServer(cfg, h)
	if err != nil {
		return err
	}
	return ErrPlatformNotSupported
}

// StartServer 提供与 NewServer + Serve 等价的统一入口（占位）
func (s *Server[C]) Start(ctx context.Context) error {
	_ = ctx
	return ErrPlatformNotSupported
}
