//go:build linux

package server

import (
	"golang.org/x/sys/unix"
)

func acceptAll[C Cipher](s *Server[C]) { acceptAllShard(s, 0) }

func startAcceptorShard[C Cipher](s *Server[C], idx int) { /* 预留，linux 下依赖事件触发为主 */
}

func acceptAllShard[C Cipher](s *Server[C], idx int) {
	lfd := s.lfds[idx]
	p := s.pls[idx]
	for {
		fd, _, err := unix.Accept4(lfd, unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return
			}
			return
		}
		_ = p.Register(fd, true, false)
		ic := newConnectionShard[C](fd, s, idx)
		s.conns.Store(fd, ic)
		go s.h.OnOpen(&ic.api)
	}
}
