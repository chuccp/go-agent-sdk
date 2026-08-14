package chat

import (
	"log"
	"sync"

	"github.com/chuccp/go-agent-sdk/value"
)

// StopReason 是模型停止生成的原因
type StopReason string

const (
	StopReasonEndTurn   StopReason = "end_turn"      // 自然结束
	StopReasonMaxTokens StopReason = "max_tokens"    // 达到 max_tokens 上限
	StopReasonToolUse   StopReason = "tool_use"      // 需要调用工具
	StopReasonStopSeq   StopReason = "stop_sequence" // 命中停止序列
)

type StreamType string
type BlockType string

const (
	StreamStartType StreamType = "Start"
	BlockStartType  StreamType = "BlockStart"
	DeltaType       StreamType = "Delta"
)
const (
	TextBlockType    BlockType = "text"
	ThinkBlockType   BlockType = "think"
	ToolUseBlockType BlockType = "toolUse"
)

// -------- StreamWriter / EventReceiver --------

// EventReceiver 事件接收方：流式过程中产生的客户端推送事件（文本/思考链增量等）
// 通过它向外发送，典型实现是 SessionContext（传入其 AddEvent 接收）。
type EventReceiver interface {
	AddEvent(event *ClientEvent)
}

// blockAssembler 是独享的 block 组装器：每个 StreamWriter 持有自己的实例，
// 在流式接收过程中累积构建一个 content block（start → delta… → 下一个 start/Close 时 flush）。
type blockAssembler struct {
	blockType BlockType
	stream    *value.Stream
	id        string // tool_use block 的 ID
	name      string // tool_use block 的工具名
	active    bool
}

// start 开始构建一个新的 content block（自动 flush 上一个未完成的 block，返回给调用方入队）。
func (a *blockAssembler) start(blockType BlockType, id, name string) Block {
	prev := a.flush()
	a.blockType = blockType
	a.id = id
	a.name = name
	a.active = true
	return prev
}

// append 向当前 block 追加增量内容（文本/思考链/工具入参 JSON 片段）。
func (a *blockAssembler) append(content string) {
	if a.active {
		a.stream.WriteString(content)
	}
}

// flush 完成当前 block 并返回具体的 Block 实现（无活动 block 时返回 nil）。
func (a *blockAssembler) flush() Block {
	if !a.active {
		return nil
	}
	a.active = false
	content := a.stream.Text()
	defer a.stream.Reset()

	switch a.blockType {
	case TextBlockType:
		return NewTextBlock(content)
	case ThinkBlockType:
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if len(content) == 0 {
			return nil
		}
		return NewThinkingBlock(content)
	case ToolUseBlockType:
		input := value.NewObject()
		if len(content) > 0 {
			if err := input.PutJson([]byte(content)); err != nil {
				log.Printf("tool_use JSON 解析失败: %v, raw=%s", err, content)
			}
		}
		return NewToolUseBlock(a.id, a.name, input)
	default:
		return NewTextBlock(content)
	}
}

// Usage 记录本次请求的 token 消耗。
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamWriter 是单次请求独享的流式写入器，仅用于接收大模型的输出。
// 生产方（LLM provider）通过 Write 写入 Stream 项（块开始/增量），
// 内部的 blockAssembler 将增量组装为完整的 Block 收集到列表；
// 调用方先 Close（flush 未完成的 block），再通过 ReadBlocks() 一次性取回全部 Block。
// 每个 StreamWriter 独享自己的收集列表与组装器，不同请求之间互不干扰。
// 写入方法（Write/WriteError/Close/StopReason/Usage）内部加锁，并发调用安全。
type StreamWriter struct {
	mu             sync.Mutex
	blocks         []Block
	receiver       EventReceiver
	usage          *Usage
	stopReason     StopReason
	blockAssembler *blockAssembler
	closed         bool
}

// NewStreamWriter 创建一个独享的 StreamWriter。receiver 为事件接收方（如 SessionContext），nil 表示不外发事件。
func NewStreamWriter(receiver EventReceiver) *StreamWriter {
	return &StreamWriter{
		blocks:   make([]Block, 0),
		receiver: receiver,
		usage: &Usage{
			InputTokens:  0,
			OutputTokens: 0,
		},
		stopReason: StopReasonEndTurn,
		blockAssembler: &blockAssembler{
			stream: value.NewStream(),
		},
	}
}

// Write 写入一个 Stream 项：块开始事件开启新的组装（上一个 block 自动 flush 入队），
// 增量追加到当前 block，并按当前 block 类型通过 receiver 向外推送客户端事件。
// 状态变更加锁保护；emit 在锁外调用，避免持锁期间执行外部代码（AddEvent）。
func (s *StreamWriter) Write(stream Stream) {
	switch stream.Type() {
	case StreamStartType:
		// 消息开始，无状态需要处理
		//return nil
	case BlockStartType:
		var id, name string
		if tu, ok := stream.(*ToolUseBlockStart); ok {
			id, name = tu.Id, tu.Name
		}
		s.mu.Lock()
		if prev := s.blockAssembler.start(stream.(BlockStream).BlockType(), id, name); prev != nil {
			s.blocks = append(s.blocks, prev)
		}
		s.mu.Unlock()
	case DeltaType:
		content := stream.(*Delta).Content
		s.mu.Lock()
		s.blockAssembler.append(content)
		blockType := s.blockAssembler.blockType
		s.mu.Unlock()
		switch blockType {
		case TextBlockType:
			s.emit(NewChunkEvent(content))
		case ThinkBlockType:
			s.emit(NewThinkingEvent(content))
		}
	}
	//return nil
}

// Close 结束写入：flush 未完成的 block。幂等，多次调用安全。
func (s *StreamWriter) flush() {
	if s.closed {
		return
	}
	s.closed = true
	if b := s.blockAssembler.flush(); b != nil {
		s.blocks = append(s.blocks, b)
	}
}

// StopReason 设置模型停止生成的原因。
func (s *StreamWriter) StopReason(stopReason StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopReason = stopReason
}

// Usage 设置本次请求的 token 消耗。
func (s *StreamWriter) Usage(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = usage
}

// ReadBlocks 返回已组装完成的全部 Block、停止原因与流错误（调用前应先 Close）。
func (s *StreamWriter) ReadBlocks() (Blocks, StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	return s.blocks, s.stopReason
}

// GetStopReason 返回模型停止生成的原因（流结束后有效）。
func (s *StreamWriter) GetStopReason() StopReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopReason
}

// GetUsage 返回本次请求的 token 消耗。
func (s *StreamWriter) GetUsage() *Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

// emit 通过 receiver 向外推送客户端事件。
func (s *StreamWriter) emit(evt *ClientEvent) {
	if s.receiver != nil {
		s.receiver.AddEvent(evt)
	}
}

type Stream interface {
	Type() StreamType
}
type BlockStream interface {
	BlockType() BlockType
}
type Start struct {
	Stream
}

func (s *Start) Type() StreamType {
	return StreamStartType
}

type TextBlockStart struct {
	Stream
	BlockStream
}

func (s *TextBlockStart) Type() StreamType {
	return BlockStartType
}
func (s *TextBlockStart) BlockType() BlockType {
	return TextBlockType
}

type ThinkingBlockStart struct {
	Stream
	BlockStream
}

func (s *ThinkingBlockStart) Type() StreamType {
	return BlockStartType
}
func (s *ThinkingBlockStart) BlockType() BlockType {
	return ThinkBlockType
}

type ToolUseBlockStart struct {
	Stream
	BlockStream
	Id   string
	Name string
}

func (s *ToolUseBlockStart) Type() StreamType {
	return BlockStartType
}
func (s *ToolUseBlockStart) BlockType() BlockType {
	return ToolUseBlockType
}

// -------- 增量 Stream 项 --------

// Delta 是一段内容增量：只携带内容本身，不区分文本/思考链/工具入参，
// 语义由它所属的当前 block（最近一次 BlockStart）决定。
type Delta struct {
	Stream
	Content string
}

func (d *Delta) Type() StreamType { return DeltaType }
