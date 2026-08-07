package agent

import (
	"github.com/chuccp/go-agent-sdk/chat"
)

// streamResponse 消费流式响应，返回 BlockStream 已组装完成的所有 content block 和 stop_reason。
// Block 的拼接与组合由 BlockStream 内部完成；文本/思考链增量已在写入时
// 通过 EventSink（AddEvent）向外广播，此处只负责收集结果。
func (c *SessionContext) streamResponse(stream *chat.BlockStream) (blocks chat.Blocks, stopReason chat.StopReason, err error) {
	for b := stream.ReadBlock(); b != nil; b = stream.ReadBlock() {
		blocks = append(blocks, b)
	}
	return blocks, stream.StopReason(), stream.Err()
}

// withoutThinking 从 blocks 中剥离 thinking block。
// 发送给 LLM 的历史不需要思考链：Anthropic 要求历史中的 thinking block 必须携带
// signature 字段原样传回，否则报 400；且空 thinking 会因 omitempty 序列化为
// {"type":"thinking"} 缺少 thinking 字段。思考链仍保留在 history/DB 中供展示，
// 仅不回传给模型。
func withoutThinking(blocks chat.Blocks) chat.Blocks {
	result := make(chat.Blocks, 0, len(blocks))
	for _, b := range blocks {
		if _, ok := b.(*chat.ThinkingBlock); ok {
			continue
		}
		result = append(result, b)
	}
	return result
}
