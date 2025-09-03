package main

import (
	"context"
	"log"
	"time"

	"github.com/legamerdc/gio/server"
)

type nopCipher struct{}

func (nopCipher) EncryptInPlace(p []byte) {}
func (nopCipher) DecryptInPlace(p []byte) {}

const apiEcho uint16 = 1

type echoHandler struct{}

func (echoHandler) OnOpen(c *server.Conn[nopCipher]) {
	log.Println("server: conn open", c.ID)
	_ = c.Write([]byte("welcome"), apiEcho)
}

func (echoHandler) OnMessage(c *server.Conn[nopCipher], api uint16, msg []byte) (async bool) {
	if api == apiEcho {
		_ = c.Write(msg, apiEcho)
	}
	return false
}

func (echoHandler) OnClose(c *server.Conn[nopCipher], err error) {
	log.Println("server: conn close", c.ID, err)
}

func main() {
	cfg := server.Config[nopCipher]{
		NumPollers:     2,
		ListenNetwork:  "tcp",
		ListenAddress:  ":18888",
		ReusePort:      true,
		TxBatchWindow:  10 * time.Millisecond,
		TimerWheelTick: time.Millisecond,
	}
	_, err := server.Start[nopCipher](cfg, echoHandler{})
	if err != nil {
		log.Fatal(err)
	}
	<-context.Background().Done()
}
