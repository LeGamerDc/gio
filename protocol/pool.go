package protocol

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

var (
	encoderPool = sync.Pool{New: func() any {
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
		return enc
	}}
	decoderPool = sync.Pool{New: func() any {
		dec, _ := zstd.NewReader(nil)
		return dec
	}}
)

func getEncoder() *zstd.Encoder  { return encoderPool.Get().(*zstd.Encoder) }
func putEncoder(e *zstd.Encoder) { encoderPool.Put(e) }
func getDecoder() *zstd.Decoder  { return decoderPool.Get().(*zstd.Decoder) }
func putDecoder(d *zstd.Decoder) { decoderPool.Put(d) }
