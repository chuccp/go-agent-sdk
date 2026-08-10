package chat

// -------- StreamWriter / EventReceiver --------

// EventReceiver 事件接收方：流式过程中产生的客户端推送事件（文本/思考链增量等）
// 通过它向外发送，典型实现是 SessionContext（传入其 AddEvent 接收）。
type EventReceiver interface {
	AddEvent(event *ClientEvent)
}

// StreamWriter 是流式内容的生产方接口（类似 http.ResponseWriter）。
// 生产方（LLM provider / 工具）通过它写入协议事件或内容块，
// Block 的拼接与组合由 BlockStream 内部完成，写入方无需关心。
type StreamWriter interface {
	Write(event Event) error
	WriteBlock(block Block) error
	WriteError(err error)
	Close()
}
