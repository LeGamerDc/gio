package gio

import "time"

// Cipher 定义了就地加解密的钩子接口
// 要求实现不改变 payload 长度
type Cipher interface {
	EncryptInPlace(p []byte)
	DecryptInPlace(p []byte)
}

// Config 为服务端配置（简化版骨架，后续逐步充实）
// 注意：M1 阶段部分字段不会立即生效，仅用于占位与 API 稳定
type Config[C Cipher] struct {
	Address            string        // 监听地址，如 ":8080"
	NumPollers         int           // poller 数量（M1 可固定为 1）
	RxRingSize         int           // 每连接 RX 环大小（字节）
	TxRingSize         int           // 每连接 TX 环大小（字节）
	TxBatchWindow      time.Duration // 延迟聚合窗口（M3）
	TxBatchBytes       int           // 批量阈值字节（M3）
	TxBatchMsgs        int           // 批量阈值条目（M3）
	TimerWheelTick     time.Duration // 时间轮精度（M3）
	MaxPayload         int           // 单帧最大负载
	CompressionDefault string        // immediate / zstd 等（M3）
	NewCipher          func() C      // 连接上下文的 Cipher 构造
}

// DefaultConfig 提供一组可工作的默认值（M1 强化 Address/NumPollers）
func DefaultConfig[C Cipher]() Config[C] {
	return Config[C]{
		Address:            ":0",
		NumPollers:         1,
		RxRingSize:         1 << 20, // 1 MiB
		TxRingSize:         1 << 20, // 1 MiB
		TxBatchWindow:      0,
		TxBatchBytes:       32 << 10, // 32 KiB
		TxBatchMsgs:        16,
		TimerWheelTick:     0,
		MaxPayload:         16 << 20, // 16 MiB
		CompressionDefault: "immediate",
	}
}
