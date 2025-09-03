package client

import (
	"log"
	"net"
	"sync"

	"github.com/legamerdc/gio/protocol"
)

type Handler interface {
	OnOpen(c *Client)
	OnMessage(c *Client, api uint16, msg []byte)
	OnClose(c *Client, err error)
}

type Client struct {
	conn net.Conn
	enc  *protocol.Encoder
	prs  *protocol.Parser
	mu   sync.Mutex
	// 接收缓冲，跨多次 Read 累积，避免半包丢失
	rb []byte
}

func Dial(network, address string, h Handler) (*Client, error) {
	nc, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	enc, _ := protocol.NewEncoder()
	prs, _ := protocol.NewParser()
	c := &Client{conn: nc, enc: enc, prs: prs}
	go h.OnOpen(c)
	go c.readLoop(h)
	return c, nil
}

func (c *Client) readLoop(h Handler) {
	buf := make([]byte, 64<<10)
	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			c.rb = append(c.rb, buf[:n]...)
			for {
				consumed, perr := c.prs.Parse(c.rb, func(api uint16, payload []byte) error {
					h.OnMessage(c, api, payload)
					return nil
				})
				if perr != nil {
					log.Printf("client: parse error: %v", perr)
				}
				if consumed == 0 {
					break
				}
				// 滑动缓冲：保留未消费部分
				c.rb = c.rb[consumed:]
			}
		}
		if err != nil {
			c.prs.Close()
			c.enc.Close()
			h.OnClose(c, err)
			return
		}
	}
}

func (c *Client) Write(api uint16, msg []byte) error {
	frame, err := c.enc.EncodeSingle(api, msg, false)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.conn.Write(frame)
	return err
}

func (c *Client) Close() error { return c.conn.Close() }
