package chat

import "github.com/chuccp/go-agent-sdk/util"

// StopReason 是模型停止生成的原因
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"      // 自然结束
	StopReasonMaxTokens StopReason = "max_tokens"    // 达到 max_tokens 上限
	StopReasonToolUse   StopReason = "tool_use"      // 需要调用工具
	StopReasonStopSeq   StopReason = "stop_sequence" // 命中停止序列
)

// Usage 记录本次请求的 token 消耗。
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// -------- StreamWriter --------

// StreamWriter 是流式事件的生产方接口（类似 http.ResponseWriter）。
// ChatService 实现方通过此接口写入事件，无需关心底层队列细节。
type StreamWriter interface {
	Write(event Event) error
	WriteError(err error)
	Close()
}

// -------- Response --------

// Response 是 ChatWithStream 返回的流式响应体。
// 调用方通过循环调用 ReadEvent() 消费事件，nil 表示流结束。
type Response struct {
	events *util.Queue[Event]
	closed bool
}

// NewResponse 创建一个流式 Response。生产方通过 Write 写入事件，完成后调用 Close。
func NewResponse() *Response {
	return &Response{events: util.NewQueue[Event]()}
}

func (r *Response) Write(event Event) error {
	return r.events.Offer(event)
}

func (r *Response) WriteError(err error) {
	r.events.Offer(&ErrorEvent{Err: err})
	r.events.Close()
}

// Close 关闭事件队列，ReadEvent 将在消费完剩余事件后返回 nil。
func (r *Response) Close() {
	r.events.Close()
}

func (r *Response) ReadEvent() Event {
	if r.closed || r.events == nil {
		return nil
	}
	evt, ok := r.events.Dequeue()
	if !ok {
		r.closed = true
		return nil
	}
	return evt
}

// Collect 消费所有事件并聚合成文本和工具调用结果。
// 这是一个便捷方法，适用于不需要逐事件处理的简单场景。
func (r *Response) Collect() (text string, toolCalls []ContentBlock) {
	for evt := r.ReadEvent(); evt != nil; evt = r.ReadEvent() {
		switch e := evt.(type) {
		case *ContentBlockDeltaEvent:
			if e.Delta.Type == "text_delta" {
				text += e.Delta.Text
			}
		case *ContentBlockStopEvent:
			// tool_use block 在 start/stop 之间通过 delta 累积，
			// 外部可配合 ContentBlockStartEvent 自行维护状态。
		}
	}
	return
}
