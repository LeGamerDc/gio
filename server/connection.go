package server

import (
	"log"
	"sync"

	"github.com/legamerdc/gio/poller"
	"github.com/legamerdc/gio/protocol"
	"golang.org/x/sys/unix"
)

type connection[C Cipher] struct {
	fd      int
	srv     *Server[C]
	mu      sync.Mutex
	api     Conn[C]
	enc     *protocol.Encoder
	prs     *protocol.Parser
	readBuf [64 << 10]byte
	// 发送队列（简单版）
	wq   [][]byte
	wpos int
	// 归属 poller
	pl poller.Poller
}

func newConnection[C Cipher](fd int, s *Server[C]) *connection[C] {
	enc, _ := protocol.NewEncoder()
	prs, _ := protocol.NewParser()
	c := &connection[C]{fd: fd, srv: s, enc: enc, prs: prs}
	c.api = Conn[C]{ID: uint64(fd), runtime: c, enc: enc}
	return c
}

func newConnectionShard[C Cipher](fd int, s *Server[C], idx int) *connection[C] {
	enc, _ := protocol.NewEncoder()
	prs, _ := protocol.NewParser()
	c := &connection[C]{fd: fd, srv: s, enc: enc, prs: prs, pl: s.pls[idx]}
	c.api = Conn[C]{ID: uint64(fd), runtime: c, enc: enc}
	return c
}

func (c *connection[C]) onReadable() {
	for {
		n, err := unix.Read(c.fd, c.readBuf[:])
		log.Printf("server: read fd=%d n=%d err=%v", c.fd, n, err)
		if n > 0 {
			buf := c.readBuf[:n]
			_, _ = c.prs.Parse(buf, func(api uint16, payload []byte) error {
				async := c.srv.h.OnMessage(&c.api, api, payload)
				_ = async
				return nil
			})
		}
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
				return
			}
			c.onClose(err)
			return
		}
		if n == 0 {
			c.onClose(nil)
			return
		}
	}
}

func (c *connection[C]) onWritable() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.wpos < len(c.wq) {
		b := c.wq[c.wpos]
		n, err := unix.Write(c.fd, b)
		log.Printf("server: write fd=%d n=%d err=%v", c.fd, n, err)
		if n > 0 {
			if n == len(b) {
				c.wpos++
				continue
			}
			c.wq[c.wpos] = b[n:]
			return
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			return
		}
		c.onClose(err)
		return
	}
	// 全部写完，关闭 EPOLLOUT
	if c.pl != nil {
		_ = c.pl.Mod(c.fd, true, false)
	}
	// 压缩队列
	if c.wpos > 0 {
		c.wq = c.wq[c.wpos:]
		c.wpos = 0
	}
}

func (c *connection[C]) enqueueWrite(frame []byte) error {
	c.mu.Lock()
	c.wq = append(c.wq, frame)
	needOut := len(c.wq) == 1
	c.mu.Unlock()
	if needOut {
		// 尝试立即写
		c.onWritable()
		// 若未清空则打开 EPOLLOUT
		c.mu.Lock()
		pend := c.wpos < len(c.wq)
		c.mu.Unlock()
		if pend && c.pl != nil {
			_ = c.pl.Mod(c.fd, true, true)
		}
	}
	return nil
}

func (c *connection[C]) onClose(err error) {
	unix.Close(c.fd)
	c.prs.Close()
	c.enc.Close()
	c.srv.h.OnClose(&c.api, err)
}
