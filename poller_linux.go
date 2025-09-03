//go:build linux

package gio

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

type poller struct {
	epfd     int
	eventfd  int
	stopping atomic.Bool
}

func newPoller() (*poller, error) {
	epfd, err := unix.EpollCreate1(unix.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	// eventfd 用于跨 goroutine 唤醒 epoll
	eventfd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(epfd)
		return nil, err
	}
	p := &poller{epfd: epfd, eventfd: eventfd}
	// 将 eventfd 加入 epoll（读事件，边缘触发）
	ev := &unix.EpollEvent{Events: uint32(unix.EPOLLIN | unix.EPOLLET), Fd: int32(eventfd)}
	if err := unix.EpollCtl(epfd, unix.EPOLL_CTL_ADD, eventfd, ev); err != nil {
		unix.Close(eventfd)
		unix.Close(epfd)
		return nil, err
	}
	return p, nil
}

func (p *poller) close() {
	unix.Close(p.eventfd)
	unix.Close(p.epfd)
}

func (p *poller) wake() error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], 1)
	_, err := unix.Write(p.eventfd, buf[:])
	return err
}

func (p *poller) attachListener(tl *net.TCPListener) error {
	rc, err := tl.SyscallConn()
	if err != nil {
		return err
	}
	var ctlErr error
	err = rc.Control(func(fd uintptr) {
		ev := &unix.EpollEvent{Events: uint32(unix.EPOLLIN | unix.EPOLLET), Fd: int32(fd)}
		if e := unix.EpollCtl(p.epfd, unix.EPOLL_CTL_ADD, int(fd), ev); e != nil {
			ctlErr = e
			return
		}
	})
	if err != nil {
		return err
	}
	return ctlErr
}

// run 为泛型自由函数，避免在方法上使用类型参数
func run[C Cipher](p *poller, ctx context.Context, s *Server[C], l *net.TCPListener) error {
	// 获取 listener fd
	var lfd int
	if rc, err := l.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) { lfd = int(fd) })
	}

	type connState struct {
		fd   int
		conn *Conn[C]
		buf  []byte
	}
	conns := make(map[int]int)            // fd -> index in states slice
	states := make([]*connState, 0, 1024) // dense storage

	lookup := func(fd int) *connState {
		if idx, ok := conns[fd]; ok {
			return states[idx]
		}
		return nil
	}
	addConn := func(fd int, c *Conn[C]) {
		st := &connState{fd: fd, conn: c, buf: make([]byte, 0, 64*1024)}
		conns[fd] = len(states)
		states = append(states, st)
	}
	delConn := func(fd int, err error) {
		if idx, ok := conns[fd]; ok {
			st := states[idx]
			// 移除 epoll 并关闭 fd
			_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
			unix.Close(fd)
			// handler 通知关闭
			s.handler.OnClose(st.conn, err)
			// 从 dense 列表删除
			last := len(states) - 1
			states[idx] = states[last]
			states = states[:last]
			if idx < len(states) {
				conns[states[idx].fd] = idx
			}
			delete(conns, fd)
		} else {
			// 未注册但仍尝试关闭
			_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
			unix.Close(fd)
		}
	}

	events := make([]unix.EpollEvent, 1024)
	var tmp [64 * 1024]byte

	for !p.stopping.Load() {
		n, err := unix.EpollWait(p.epfd, events, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		for i := 0; i < n; i++ {
			ev := events[i]
			fd := int(ev.Fd)
			if fd == p.eventfd {
				var b [8]byte
				_, _ = unix.Read(p.eventfd, b[:])
				continue
			}
			if fd == lfd {
				// accept 循环直到 EAGAIN
				for {
					nc, err := l.Accept()
					if err != nil {
						if ne, ok := err.(net.Error); ok && ne.Temporary() {
							continue
						}
						if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
							break
						}
						return err
					}
					// 获取 fd 并设置选项
					var cfd int
					if rc, err := nc.(*net.TCPConn).SyscallConn(); err == nil {
						_ = rc.Control(func(x uintptr) { cfd = int(x) })
					}
					_ = unix.SetNonblock(cfd, true)
					_ = unix.SetsockoptInt(cfd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1)
					// 注册到 epoll
					cev := &unix.EpollEvent{Events: uint32(unix.EPOLLIN | unix.EPOLLET | unix.EPOLLRDHUP), Fd: int32(cfd)}
					if err := unix.EpollCtl(p.epfd, unix.EPOLL_CTL_ADD, cfd, cev); err != nil {
						nc.Close()
						continue
					}
					// 构造连接对象与回调 OnOpen
					s.nextConnID++
					var data C
					if s.cfg.NewCipher != nil {
						data = s.cfg.NewCipher()
					}
					c := &Conn[C]{ID: s.nextConnID, Data: data}
					addConn(cfd, c)
					s.handler.OnOpen(c)
				}
				continue
			}

			// 连接事件
			if (ev.Events & (unix.EPOLLHUP | unix.EPOLLERR | unix.EPOLLRDHUP)) != 0 {
				delConn(fd, nil)
				continue
			}
			if (ev.Events & unix.EPOLLIN) != 0 {
				st := lookup(fd)
				if st == nil {
					// 未知 fd
					_ = unix.EpollCtl(p.epfd, unix.EPOLL_CTL_DEL, fd, nil)
					unix.Close(fd)
					continue
				}
				for {
					nread, rerr := unix.Read(fd, tmp[:])
					if nread > 0 {
						st.buf = append(st.buf, tmp[:nread]...)
					}
					if rerr != nil {
						if rerr == unix.EAGAIN || rerr == unix.EWOULDBLOCK {
							break
						}
						delConn(fd, rerr)
						break
					}
					if nread == 0 {
						// 对端关闭
						delConn(fd, nil)
						break
					}
				}
				// 解析缓冲区中的所有完整非批量帧
				b := st.buf
				off := 0
				for {
					if len(b)-off < 2 {
						break
					}
					compressed, batched, used, payloadLen, perr := parseLenFlags(b[off:])
					if perr != nil {
						break
					}
					if batched || compressed {
						// M1 不处理压缩或批量，直接关闭
						delConn(fd, errors.New("gio: unsupported frame in M1"))
						break
					}
					// 非批量帧应包含 Api(uint16)
					hdr := used + 2
					if len(b)-off < hdr {
						break
					}
					api := binary.BigEndian.Uint16(b[off+used : off+used+2])
					total := hdr + payloadLen
					if payloadLen < 0 || payloadLen > s.cfg.MaxPayload {
						delConn(fd, errors.New("gio: payload too large"))
						break
					}
					if len(b)-off < total {
						break
					}
					msg := b[off+hdr : off+total]
					// 同连接串行：直接在此 goroutine 调用
					_ = s.handler.OnMessage(st.conn, api, msg)
					off += total
				}
				if off > 0 {
					// 滑动窗口，保留未完成部分
					st.buf = append(st.buf[:0], st.buf[off:]...)
				}
			}
		}
	}
	return nil
}
