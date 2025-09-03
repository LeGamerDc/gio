package protocol

import (
	"encoding/binary"
	"errors"
)

// LenFlags 头部编码：
// 短头（2B，BE）：
//   bit15: Compressed
//   bit14: Batched (隐含 Compressed=1)
//   bit13: Ext=0 (短头)
//   bit12..0: Len13 (0..8191)
// 长头（4B，BE）：
//   bit31: Compressed
//   bit30: Batched (隐含 Compressed=1)
//   bit29: Ext=1 (长头)
//   bit28..0: Len29 (0..(1<<29)-1)

const (
	shortHeadMaxLen = (1 << 13) - 1 // 8191
	longHeadMaxLen  = (1 << 29) - 1
)

var (
	errHeaderTooShort   = errors.New("protocol: header too short")
	errLengthOutOfRange = errors.New("protocol: length out of range")
)

// EncodeLenFlags 返回写入的头部字节（2 或 4 字节）和是否为长头。
func EncodeLenFlags(length int, compressed, batched bool) (hdr []byte, isLong bool, _ error) {
	if length < 0 || length > longHeadMaxLen {
		return nil, false, errLengthOutOfRange
	}
	if batched {
		compressed = true // 规则：Batched 隐含 Compressed
	}
	if length <= shortHeadMaxLen {
		// 短头：2 字节
		var v uint16
		if compressed {
			v |= 1 << 15
		}
		if batched {
			v |= 1 << 14
		}
		// bit13=0 表示短头
		v |= uint16(length) & 0x1FFF
		buf := make([]byte, 2)
		binary.BigEndian.PutUint16(buf, v)
		return buf, false, nil
	}
	// 长头：4 字节
	var v uint32 = 1 << 29 // Ext=1
	if compressed {
		v |= 1 << 31
	}
	if batched {
		v |= 1 << 30
	}
	v |= uint32(length) & 0x1FFFFFFF
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, v)
	return buf, true, nil
}

// DecodeLenFlags 解码头部，返回：已消费字节数、长度、compressed、batched。
func DecodeLenFlags(b []byte) (consumed int, length int, compressed, batched bool, _ error) {
	if len(b) < 2 {
		return 0, 0, false, false, errHeaderTooShort
	}
	v16 := binary.BigEndian.Uint16(b[:2])
	ext := (v16>>13)&0x1 == 1 // 对短头，Ext=0；若为 1 则表示长头
	if !ext {
		compressed = (v16>>15)&0x1 == 1
		batched = (v16>>14)&0x1 == 1
		length = int(v16 & 0x1FFF)
		return 2, length, compressed, batched, nil
	}
	// 长头
	if len(b) < 4 {
		return 0, 0, false, false, errHeaderTooShort
	}
	v32 := binary.BigEndian.Uint32(b[:4])
	compressed = (v32>>31)&0x1 == 1
	batched = (v32>>30)&0x1 == 1
	length = int(v32 & 0x1FFFFFFF)
	return 4, length, compressed, batched, nil
}

// AppendApi 将 api(uint16, BE) 追加到切片末尾。
func AppendApi(dst []byte, api uint16) []byte {
	var a [2]byte
	binary.BigEndian.PutUint16(a[:], api)
	return append(dst, a[:]...)
}

// ReadApi 从 b 前两个字节解析 api。
func ReadApi(b []byte) (api uint16, consumed int, _ error) {
	if len(b) < 2 {
		return 0, 0, errHeaderTooShort
	}
	api = binary.BigEndian.Uint16(b[:2])
	return api, 2, nil
}
