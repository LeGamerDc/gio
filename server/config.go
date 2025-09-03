package server

import (
	"time"
)

type Cipher interface {
	EncryptInPlace(p []byte)
	DecryptInPlace(p []byte)
}

type Handler[C Cipher] interface {
	OnOpen(c *Conn[C])
	OnMessage(c *Conn[C], api uint16, msg []byte) (async bool)
	OnClose(c *Conn[C], err error)
}

type Config[C Cipher] struct {
	NumPollers      int
	RxRingSize      int
	TxRingSize      int
	TxBatchWindow   time.Duration
	TxBatchBytes    int
	MaxPayload      int
	TimerWheelTick  time.Duration
	CompressionAlgo string
	NewCipher       func() C
	ListenNetwork   string
	ListenAddress   string
	ReusePort       bool
}
