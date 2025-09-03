//go:build linux

package poller

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

type epollPoller struct {
	efd   int
	wfd   int // eventfd for wakeup
	close bool
}

func New() (Poller, error) {
	efd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	wfd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		unix.Close(efd)
		return nil, err
	}
	p := &epollPoller{efd: efd, wfd: wfd}
	// 注册 wakeup fd
	ev := &unix.EpollEvent{Events: unix.EPOLLIN | unix.EPOLLET, Fd: int32(wfd)}
	if err := unix.EpollCtl(efd, unix.EPOLL_CTL_ADD, wfd, ev); err != nil {
		unix.Close(wfd)
		unix.Close(efd)
		return nil, err
	}
	return p, nil
}

func (p *epollPoller) Register(fd FD, readable, writable bool) error {
	var flag uint32 = unix.EPOLLET
	if readable {
		flag |= unix.EPOLLIN
	}
	if writable {
		flag |= unix.EPOLLOUT
	}
	ev := &unix.EpollEvent{Events: flag, Fd: int32(fd)}
	return unix.EpollCtl(p.efd, unix.EPOLL_CTL_ADD, fd, ev)
}

func (p *epollPoller) Mod(fd FD, readable, writable bool) error {
	var flag uint32 = unix.EPOLLET
	if readable {
		flag |= unix.EPOLLIN
	}
	if writable {
		flag |= unix.EPOLLOUT
	}
	ev := &unix.EpollEvent{Events: flag, Fd: int32(fd)}
	return unix.EpollCtl(p.efd, unix.EPOLL_CTL_MOD, fd, ev)
}

func (p *epollPoller) Unregister(fd FD) error {
	return unix.EpollCtl(p.efd, unix.EPOLL_CTL_DEL, fd, nil)
}

func (p *epollPoller) Wake() error {
	var buf [8]byte
	buf[0] = 1
	_, err := unix.Write(p.wfd, buf[:])
	if err == unix.EAGAIN {
		return nil
	}
	return err
}

func (p *epollPoller) Close() error {
	p.close = true
	unix.Close(p.wfd)
	return unix.Close(p.efd)
}

func (p *epollPoller) Run(h Handler) error {
	defer runtime.KeepAlive(p)
	events := make([]unix.EpollEvent, 1024)
	var efdBuf [8]byte
	for !p.close {
		n, err := unix.EpollWait(p.efd, events, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		for i := 0; i < n; i++ {
			ev := events[i]
			fd := int(ev.Fd)
			if fd == p.wfd {
				// 清空 eventfd
				for {
					_, rerr := unix.Read(p.wfd, efdBuf[:])
					if rerr == unix.EAGAIN {
						break
					}
					if rerr != nil {
						return rerr
					}
				}
				continue
			}
			if (ev.Events & (unix.EPOLLERR | unix.EPOLLHUP)) != 0 {
				h.OnClose(fd, errors.New("epoll: err|hup"))
				continue
			}
			if (ev.Events & unix.EPOLLIN) != 0 {
				h.OnReadable(fd)
			}
			if (ev.Events & unix.EPOLLOUT) != 0 {
				h.OnWritable(fd)
			}
		}
	}
	return nil
}
