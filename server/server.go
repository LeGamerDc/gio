package server

import (
	"context"
	"sync"
	"time"

	"github.com/legamerdc/gio/poller"
)

type Server[C Cipher] struct {
	cfg  Config[C]
	h    Handler[C]
	lfds []int
	pls  []poller.Poller
	wg   sync.WaitGroup

	conns sync.Map // fd -> *connection[C]

	tw *timerWheel
}

func Start[C Cipher](cfg Config[C], h Handler[C]) (*Server[C], error) {
	s := &Server[C]{cfg: cfg, h: h}
	if cfg.NumPollers <= 0 {
		cfg.NumPollers = 1
	}
	// 创建时间轮
	tick := cfg.TimerWheelTick
	if tick <= 0 {
		tick = time.Millisecond
	}
	s.tw = newTimerWheel(tick)
	// 创建多个监听 + 多个 poller
	for i := 0; i < cfg.NumPollers; i++ {
		lfd, err := openListener(cfg.ListenNetwork, cfg.ListenAddress, cfg.ReusePort)
		if err != nil {
			s.closeAll()
			return nil, err
		}
		p, err := poller.New()
		if err != nil {
			closeFD(lfd)
			s.closeAll()
			return nil, err
		}
		s.lfds = append(s.lfds, lfd)
		s.pls = append(s.pls, p)
		_ = p.Register(lfd, true, false)
		// 启动事件循环
		pl := p
		idx := i
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_ = pl.Run((*srvHandler[C])(&srvShard[C]{Server: s, idx: idx}))
		}()
		// 启动备用 accept 轮询
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			startAcceptorShard(s, idx)
		}()
	}
	// 启动时间轮（目前仅占位）
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.tw.run(func() {
			// 将来触发各连接的 TxAggregator 刷新
		})
	}()
	return s, nil
}

func (s *Server[C]) Stop(ctx context.Context) error {
	for _, p := range s.pls {
		p.Close()
	}
	for _, fd := range s.lfds {
		_ = closeFD(fd)
	}
	if s.tw != nil {
		s.tw.stop()
	}
	s.wg.Wait()
	return nil
}

func (s *Server[C]) closeAll() {
	for _, p := range s.pls {
		p.Close()
	}
	for _, fd := range s.lfds {
		_ = closeFD(fd)
	}
}

// 分片 handler：每个 poller 有自己的监听 fd

type srvShard[C Cipher] struct {
	*Server[C]
	idx int
}

type srvHandler[C Cipher] srvShard[C]

func (s *srvHandler[C]) OnReadable(fd poller.FD) {
	if int(fd) == s.lfds[s.idx] {
		acceptAllShard((*Server[C])(s.Server), s.idx)
		return
	}
	if v, ok := s.conns.Load(int(fd)); ok {
		c := v.(*connection[C])
		c.onReadable()
	}
}

func (s *srvHandler[C]) OnWritable(fd poller.FD) {
	if v, ok := s.conns.Load(int(fd)); ok {
		c := v.(*connection[C])
		c.onWritable()
	}
}

func (s *srvHandler[C]) OnClose(fd poller.FD, err error) {
	if v, ok := s.conns.LoadAndDelete(int(fd)); ok {
		c := v.(*connection[C])
		c.onClose(err)
	}
}
