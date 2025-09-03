package gio

import (
	"encoding/binary"
	"fmt"
)

// 按 design.md：LenFlags 可变 2B/4B；非批量帧包含 Api(uint16)
// 本文件仅提供解析与编码的最小工具函数（无压缩路径），用于 M1

const (
	lenflagCompressedBit = 1 << 15
	lenflagBatchedBit    = 1 << 14
	lenflagExtBit        = 1 << 13 // 短头中 Ext=0；若读取到1则需扩展为长头
)

// parseLenFlags 解析可变长度 LenFlags，返回（isCompressed, isBatched, headerBytesUsed, payloadLen）
func parseLenFlags(b []byte) (compressed bool, batched bool, used int, payloadLen int, err error) {
	if len(b) < 2 {
		return false, false, 0, 0, fmt.Errorf("gio: short header")
	}
	first := binary.BigEndian.Uint16(b[:2])
	compressed = (first & lenflagCompressedBit) != 0
	batched = (first & lenflagBatchedBit) != 0
	ext := (first & lenflagExtBit) != 0
	if !ext {
		// 短头：低 13 位为长度
		used = 2
		payloadLen = int(first & 0x1FFF)
		return
	}
	// 长头需要 4 字节
	if len(b) < 4 {
		return false, false, 0, 0, fmt.Errorf("gio: need long header")
	}
	hi := uint16(first & 0x1FFF)
	lo := binary.BigEndian.Uint16(b[2:4])
	used = 4
	payloadLen = int((uint32(hi) << 16) | uint32(lo))
	return
}

// encodeLenFlags 编码 LenFlags（根据长度选择 2B/4B）
func encodeLenFlags(dst []byte, compressed bool, batched bool, payloadLen int) (used int, err error) {
	if payloadLen < 0 {
		return 0, fmt.Errorf("gio: invalid length")
	}
	var flags uint32
	if compressed {
		flags |= 1 << 31
	}
	if batched {
		flags |= 1 << 30
	}
	if payloadLen <= 0x1FFF { // 13 bits
		// 短头：Ext=0
		v := uint16((flags>>16)&0xE000) | uint16(payloadLen)
		if len(dst) < 2 {
			return 0, fmt.Errorf("gio: dst too small")
		}
		binary.BigEndian.PutUint16(dst[:2], v)
		return 2, nil
	}
	// 长头：Ext=1 + 29bit len
	flags |= 1 << 29 // Ext
	length := uint32(payloadLen)
	hi := uint16((length >> 16) & 0x1FFF)
	lo := uint16(length & 0xFFFF)
	if len(dst) < 4 {
		return 0, fmt.Errorf("gio: dst too small")
	}
	first := uint16((flags>>16)&0xE000) | hi
	binary.BigEndian.PutUint16(dst[:2], first)
	binary.BigEndian.PutUint16(dst[2:4], lo)
	return 4, nil
}
