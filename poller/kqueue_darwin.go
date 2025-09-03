//go:build darwin

package poller

import (
	"errors"
	"log"
	"runtime"

	"golang.org/x/sys/unix"
)

type kqueuePoller struct {
	kq    int
	wfd   int // 写端，用于唤醒
	rfd   int // 读端，注册到 kqueue
	close bool
}

func New() (Poller, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	// 使用管道作为唤醒
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		unix.Close(kq)
		return nil, err
	}
	rfd, wfd := p[0], p[1]
	_ = unix.SetNonblock(rfd, true)
	_ = unix.SetNonblock(wfd, true)
	// 注册读事件
	kev := unix.Kevent_t{
		Ident:  uint64(rfd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_CLEAR,
	}
	_, err = unix.Kevent(kq, []unix.Kevent_t{kev}, nil, nil)
	if err != nil {
		unix.Close(rfd)
		unix.Close(wfd)
		unix.Close(kq)
		return nil, err
	}
	return &kqueuePoller{kq: kq, wfd: wfd, rfd: rfd}, nil
}

func (p *kqueuePoller) Register(fd FD, readable, writable bool) error {
	var changes []unix.Kevent_t
	if readable {
		changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_CLEAR})
	}
	if writable {
		changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_ADD | unix.EV_CLEAR})
	}
	if len(changes) == 0 {
		return nil
	}
	_, err := unix.Kevent(p.kq, changes, nil, nil)
	return err
}

func (p *kqueuePoller) Mod(fd FD, readable, writable bool) error {
	// 在 kqueue 中，Mod 等价为删除不需要的再添加
	var changes []unix.Kevent_t
	changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_DELETE})
	changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_DELETE})
	if readable {
		changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_ADD | unix.EV_CLEAR})
	}
	if writable {
		changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_ADD | unix.EV_CLEAR})
	}
	_, err := unix.Kevent(p.kq, changes, nil, nil)
	return err
}

func (p *kqueuePoller) Unregister(fd FD) error {
	changes := []unix.Kevent_t{
		{Ident: uint64(fd), Filter: unix.EVFILT_READ, Flags: unix.EV_DELETE},
		{Ident: uint64(fd), Filter: unix.EVFILT_WRITE, Flags: unix.EV_DELETE},
	}
	_, err := unix.Kevent(p.kq, changes, nil, nil)
	return err
}

func (p *kqueuePoller) Wake() error {
	var b [1]byte
	b[0] = 1
	_, err := unix.Write(p.wfd, b[:])
	if err == unix.EAGAIN {
		return nil
	}
	return err
}

func (p *kqueuePoller) Close() error {
	p.close = true
	unix.Close(p.rfd)
	unix.Close(p.wfd)
	return unix.Close(p.kq)
}

func (p *kqueuePoller) Run(h Handler) error {
	defer runtime.KeepAlive(p)
	events := make([]unix.Kevent_t, 1024)
	buf := make([]byte, 16)
	for !p.close {
		n, err := unix.Kevent(p.kq, nil, events, nil)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		for i := 0; i < n; i++ {
			ev := events[i]
			fd := int(ev.Ident)
			if fd == p.rfd {
				for {
					_, rerr := unix.Read(p.rfd, buf)
					if rerr == unix.EAGAIN {
						break
					}
					if rerr != nil {
						return rerr
					}
				}
				continue
			}
			log.Printf("kqueue: event fd=%d filter=%d flags=0x%x fflags=0x%x data=%d", fd, ev.Filter, ev.Flags, ev.Fflags, ev.Data)
			// 优先处理可读/可写
			if ev.Filter == unix.EVFILT_READ {
				h.OnReadable(fd)
				// 读完后若标记 EOF，再进行关闭回调
				if (ev.Flags & unix.EV_EOF) != 0 {
					h.OnClose(fd, errors.New("kqueue: eof"))
				}
				continue
			}
			if ev.Filter == unix.EVFILT_WRITE {
				h.OnWritable(fd)
				continue
			}
			// 兜底：无特定过滤器但带 EOF 的情况
			if (ev.Flags & unix.EV_EOF) != 0 {
				h.OnClose(fd, errors.New("kqueue: eof"))
			}
		}
	}
	return nil
}
