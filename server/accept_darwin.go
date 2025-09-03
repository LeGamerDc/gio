//go:build darwin

package server

import (
	"time"

	"golang.org/x/sys/unix"
)

func acceptAll[C Cipher](s *Server[C]) { acceptAllShard(s, 0) }

func startAcceptorShard[C Cipher](s *Server[C], idx int) {
	// 简单轮询接受，增强兼容性
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			acceptAllShard(s, idx)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func acceptAllShard[C Cipher](s *Server[C], idx int) {
	lfd := s.lfds[idx]
	p := s.pls[idx]
	for {
		fd, _, err := unix.Accept(lfd)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return
			}
			return
		}
		_ = unix.SetNonblock(fd, true)
		_ = p.Register(fd, true, false)
		ic := newConnectionShard[C](fd, s, idx)
		s.conns.Store(fd, ic)
		go s.h.OnOpen(&ic.api)
	}
}
