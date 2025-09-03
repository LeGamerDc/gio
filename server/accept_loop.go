package server

import "time"

func (s *Server[C]) startAcceptor() {
	// 简单定时轮询接受连接，优先保证可用性
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			acceptAll(s)
		default:
			// 让出
			time.Sleep(1 * time.Millisecond)
		}
	}
}
