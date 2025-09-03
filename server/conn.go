package server

import (
	"context"
	"log"

	"github.com/legamerdc/gio/protocol"
)

type Conn[C Cipher] struct {
	ID   uint64
	Data C

	// 运行时注入
	runtime *connection[C]
	enc     *protocol.Encoder
}

func (c *Conn[C]) Context() *C { return &c.Data }

func (c *Conn[C]) Write(msg []byte, api uint16, opts ...any) error {
	if c.enc == nil || c.runtime == nil {
		log.Printf("server: Conn.Write skip, enc=%v runtime=%v", c.enc != nil, c.runtime != nil)
		return nil
	}
	frame, err := c.enc.EncodeSingle(api, msg, false)
	if err != nil {
		log.Printf("server: Conn.Write encode error: %v", err)
		return err
	}
	err = c.runtime.enqueueWrite(frame)
	if err != nil {
		log.Printf("server: Conn.Write enqueue error: %v", err)
	}
	return err
}

func (c *Conn[C]) Go(task func(ctx context.Context) error) {}

func (c *Conn[C]) Flush() error { return nil }

func (c *Conn[C]) Close() error { return nil }
