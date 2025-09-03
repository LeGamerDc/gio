//go:build linux || darwin

package server

import (
	"net"
	"strings"

	"golang.org/x/sys/unix"
)

func openListener(network, address string, reusePort bool) (int, error) {
	// 仅支持 tcp 与 tcp4/tcp6
	fam := unix.AF_INET
	if strings.HasSuffix(network, "6") {
		fam = unix.AF_INET6
	}
	fd, err := unix.Socket(fam, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, err
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	if reusePort {
		_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}
	_ = unix.SetNonblock(fd, true)
	// 绑定
	var sa unix.Sockaddr
	if fam == unix.AF_INET6 {
		addr, err := net.ResolveTCPAddr("tcp6", address)
		if err != nil {
			unix.Close(fd)
			return -1, err
		}
		var sa6 unix.SockaddrInet6
		if addr.IP != nil {
			copy(sa6.Addr[:], addr.IP.To16())
		}
		sa6.Port = addr.Port
		sa = &sa6
	} else {
		addr, err := net.ResolveTCPAddr("tcp4", address)
		if err != nil {
			unix.Close(fd)
			return -1, err
		}
		var sa4 unix.SockaddrInet4
		if addr.IP != nil {
			copy(sa4.Addr[:], addr.IP.To4())
		}
		sa4.Port = addr.Port
		sa = &sa4
	}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if err := unix.Listen(fd, 1024); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func closeFD(fd int) error { return unix.Close(fd) }
