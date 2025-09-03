package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// BatchItem 用于批前镜像编码。
type BatchItem struct {
	Api     uint16
	Payload []byte
}

// Encoder 提供单帧/批量帧编码。
// 注：批量帧总是压缩（Batched => Compressed）。

type Encoder struct{}

func NewEncoder() (*Encoder, error) { return &Encoder{}, nil }

func (e *Encoder) Close() error { return nil }

// EncodeSingle 返回：头部+可选 api + payload（压缩可选）。
func (e *Encoder) EncodeSingle(api uint16, payload []byte, compressed bool) (frame []byte, _ error) {
	var body []byte
	if compressed {
		zw := getEncoder()
		body = zw.EncodeAll(payload, nil)
		putEncoder(zw)
	} else {
		body = payload
	}
	hdr, _, err := EncodeLenFlags(len(body), compressed, false)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(hdr)+2+len(body))
	out = append(out, hdr...)
	out = AppendApi(out, api)
	out = append(out, body...)
	return out, nil
}

// EncodeBatch 将一批消息编码为批前镜像并压缩，返回单帧（Batched=1，隐含 Compressed=1，无 Api 字段）。
func (e *Encoder) EncodeBatch(items []BatchItem) ([]byte, error) {
	var pre bytes.Buffer
	// numMessages
	var uvBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(uvBuf[:], uint64(len(items)))
	pre.Write(uvBuf[:n])
	for _, it := range items {
		// api
		var a [2]byte
		binary.BigEndian.PutUint16(a[:], it.Api)
		pre.Write(a[:])
		// len
		n = binary.PutUvarint(uvBuf[:], uint64(len(it.Payload)))
		pre.Write(uvBuf[:n])
		// payload
		pre.Write(it.Payload)
	}
	// 压缩 pre-image
	zw := getEncoder()
	body := zw.EncodeAll(pre.Bytes(), nil)
	putEncoder(zw)
	hdr, _, err := EncodeLenFlags(len(body), true, true)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(hdr)+len(body))
	out = append(out, hdr...)
	out = append(out, body...)
	return out, nil
}

// Parser 按帧解析；对批量帧进行解压并回调每条消息。
// 回调可选择消费错误终止解析。

type Parser struct{}

func NewParser() (*Parser, error) { return &Parser{}, nil }

func (p *Parser) Close() error { return nil }

var ErrIncomplete = errors.New("protocol: incomplete frame")

// Parse 尝试从 buf 解析尽可能多的帧；返回已消费字节数。
// onMessage(api, payload) 在非批量时直接回调；在批量时对每条批内消息回调。
func (p *Parser) Parse(buf []byte, onMessage func(api uint16, payload []byte) error) (consumed int, _ error) {
	i := 0
	for {
		if len(buf[i:]) < 2 {
			return i, nil // 不足以判断头
		}
		c, length, compressed, batched, err := DecodeLenFlags(buf[i:])
		if err != nil {
			return i, err
		}
		if !batched {
			// 非批量：长度不包含 Api，需要额外读取 2 字节的 Api
			if len(buf[i+c:]) < 2+length {
				return i, nil // 不完整帧
			}
			api := binary.BigEndian.Uint16(buf[i+c : i+c+2])
			msg := buf[i+c+2 : i+c+2+length]
			if compressed {
				dz := getDecoder()
				out, derr := dz.DecodeAll(msg, nil)
				putDecoder(dz)
				if derr != nil {
					return i, derr
				}
				msg = out
			}
			if err := onMessage(api, msg); err != nil {
				return i, err
			}
			i += c + 2 + length
			continue
		}
		// 批量：payload 为压缩后的 pre-image，长度即为压缩体长度
		if len(buf[i+c:]) < length {
			return i, nil // 不完整帧
		}
		payload := buf[i+c : i+c+length]
		i += c + length
		dz := getDecoder()
		out, derr := dz.DecodeAll(payload, nil)
		putDecoder(dz)
		if derr != nil {
			return i, derr
		}
		// 解析 pre-image
		r := bytes.NewReader(out)
		num, err := binary.ReadUvarint(r)
		if err != nil {
			return i, err
		}
		for j := uint64(0); j < num; j++ {
			var ab [2]byte
			if _, err := io.ReadFull(r, ab[:]); err != nil {
				return i, err
			}
			api := binary.BigEndian.Uint16(ab[:])
			ln, err := binary.ReadUvarint(r)
			if err != nil {
				return i, err
			}
			if ln == 0 {
				if err := onMessage(api, nil); err != nil {
					return i, err
				}
				continue
			}
			msg := make([]byte, ln)
			if _, err := io.ReadFull(r, msg); err != nil {
				return i, err
			}
			if err := onMessage(api, msg); err != nil {
				return i, err
			}
		}
	}
}
