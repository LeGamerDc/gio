package gio

// Handler 为用户回调接口
// OnMessage 返回 true 表示业务选择异步分流（后续版本接入 Conn.Go）
type Handler[C Cipher] interface {
	OnOpen(c *Conn[C])
	OnMessage(c *Conn[C], api uint16, msg []byte) (async bool)
	OnClose(c *Conn[C], err error)
}
