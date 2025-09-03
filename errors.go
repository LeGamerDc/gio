package gio

import "errors"

var (
	// ErrPlatformNotSupported 非 Linux 平台的占位错误（需要 epoll）
	ErrPlatformNotSupported = errors.New("gio: platform not supported (requires Linux/epoll)")

	// ErrNotImplemented 功能尚未落地
	ErrNotImplemented = errors.New("gio: not implemented")

	// ErrInvalidArgument 参数非法
	ErrInvalidArgument = errors.New("gio: invalid argument")
)
