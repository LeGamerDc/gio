package netutil

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func SetNonblock(fd int, nonblock bool) error {
	return unix.SetNonblock(fd, nonblock)
}

func SetReusePort(fd int, enable bool) error {
	v := 0
	if enable {
		v = 1
	}
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, v)
}

func SetReuseAddr(fd int, enable bool) error {
	v := 0
	if enable {
		v = 1
	}
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, v)
}

func SetNoDelay(fd int, enable bool) error {
	v := 0
	if enable {
		v = 1
	}
	return unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY, v)
}

func SetRecvBuf(fd int, n int) error {
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, n)
}
func SetSendBuf(fd int, n int) error {
	return unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, n)
}

// GetFD 从 net.Listener 或 net.Conn 中抽取 fd。
func GetFDFromConn(c net.Conn) (int, error) {
	// 依赖 net.TCPConn 的 SyscallConn
	if sc, ok := c.(interface{ SyscallConn() syscall.RawConn }); ok {
		var fd int
		var err error
		e := sc.SyscallConn()
		e.Control(func(rawfd uintptr) {
			fd = int(rawfd)
		})
		return fd, err
	}
	return -1, syscall.EINVAL
}
