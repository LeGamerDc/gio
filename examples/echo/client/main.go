package main

import (
	"log"
	"time"

	"github.com/legamerdc/gio/client"
)

const apiEcho uint16 = 1

type handler struct{}

func (handler) OnOpen(c *client.Client) {
	log.Println("client: connected")
	_ = c.Write(apiEcho, []byte("hello"))
}

func (handler) OnMessage(c *client.Client, api uint16, msg []byte) {
	log.Printf("client: recv api=%d msg=%q\n", api, string(msg))
}

func (handler) OnClose(c *client.Client, err error) {
	log.Println("client: closed", err)
}

func main() {
	c, err := client.Dial("tcp", "127.0.0.1:18888", handler{})
	if err != nil {
		log.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = c.Close()
}
