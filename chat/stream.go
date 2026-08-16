package chat

import (
	"log"
	"strings"
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

const (
	StreamStartType StreamType = "Start"
	BlockStartType  StreamType = "BlockStart"
	DeltaType       StreamType = "Delta"
)

// -------- BlockStream / EventReceiver --------

// EventReceiver 事件接收方：流式过程中产生的客户端推送事件（文本/思考链增量等）
// 通过它外发，典型实现是 SessionContext（传入其 AddEvent 接收）。
type EventReceiver interface {
	AddEvent(event *ClientEvent)
}

// blockAssembler 是独享的 block 组装器：每个 BlockStream 持有自己的实例，
// 在流式接收过程中累积构建一个 content block（start → delta… → 下一个 start/flush 时完成）。
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
	case BlockTypeText:
		return NewTextBlock(content)
	case BlockTypeThinking:
		// 跳过空的 thinking block（避免产生 {"type":"thinking"} 脏数据）
		if len(content) == 0 {
			return nil
		}
		return NewThinkingBlock(content)
	case BlockTypeToolUse:
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

// BlockStream 是统一的输出收集管道，LLM 流式输出与工具输出共用：
// ─ LLM 场景：provider 通过 Write 写入简化流项（块开始/增量），内部 blockAssembler
//
//	组装为完整 Block；停止原因/token 消耗以 StopReasonBlock/UsageBlock 写入同一列表。
//
// ─ 工具场景：WriteBlock 写完整内容块（连续 TextBlock 拼接为一个），
//
//	WriteEvent 推送流式输出（chunk 事件实时回显），WriteErrorText 以文本写入执行错误。
//
// 调用方经 ReadBlocks() 一次性取回内容 Block（不含 usage/stop_reason 元数据块）；
// 元数据经 GetStopReason/GetUsage 取回。
// 写入方法（Write/WriteBlock/WriteEvent/WriteErrorText/StopReason/Usage）内部加锁，并发调用安全。
type BlockStream struct {
	mu            sync.Mutex
	blocks        []Block // 内容 block 与元数据 block（usage/stop_reason）统一存放
	pending       strings.Builder
	hasPending    bool // 连续 TextBlock 的拼接缓冲（仅工具路径产生）
	pendingIsError bool // 当前拼接缓冲是否为错误文本
	receiver      EventReceiver
	assembler     *blockAssembler
}

// NewBlockStream 创建一个 BlockStream。receiver 为事件接收方（如 SessionContext），nil 表示不外发事件。
func NewBlockStream(receiver EventReceiver) *BlockStream {
	return &BlockStream{
		blocks:   make([]Block, 0),
		receiver: receiver,
		assembler: &blockAssembler{
			stream: value.NewStream(),
		},
	}
}

// Write 写入一个 Stream 项（LLM 流式路径）：块开始事件开启新的组装（上一个 block 自动
// flush 入队），增量追加到当前 block，并按当前 block 类型通过 receiver 向外推送客户端事件。
// 状态变更加锁保护；emit 在锁外调用，避免持锁期间执行外部代码（AddEvent）。
func (s *BlockStream) Write(stream Stream) {
	switch stream.Type() {
	case StreamStartType:
		// 消息开始，无状态需要处理
	case BlockStartType:
		var id, name string
		var blockType BlockType
		switch v := stream.(type) {
		case *TextBlockStart:
			blockType = BlockTypeText
		case *ThinkingBlockStart:
			blockType = BlockTypeThinking
		case *ToolUseBlockStart:
			blockType = BlockTypeToolUse
			id, name = v.Id, v.Name
		}
		s.mu.Lock()
		if prev := s.assembler.start(blockType, id, name); prev != nil {
			s.writeBlockLocked(prev)
		}
		s.mu.Unlock()
	case DeltaType:
		content := stream.(*Delta).Content
		s.mu.Lock()
		s.assembler.append(content)
		blockType := s.assembler.blockType
		s.mu.Unlock()
		switch blockType {
		case BlockTypeText:
			s.emit(NewChunkEvent(content))
		case BlockTypeThinking:
			s.emit(NewThinkingEvent(content))
		}
	}
}

// WriteBlock 写入一个内容块（工具路径）：连续的 TextBlock 会被拼接为一个，
// 遇到其他类型（或 ReadBlocks）时输出拼接结果，其余类型直接收集。
func (s *BlockStream) WriteBlock(block Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeBlockLocked(block)
}

// WriteEvent 推送一段流式输出（工具路径）：经 receiver 向外发送 chunk 事件（实时回显），
// 同时作为文本块收集，随 ReadBlocks 进入 tool_result。
// 兼容长耗时命令等不是一次性出结果、而是流式输出的场景。
func (s *BlockStream) WriteEvent(content string) {
	if content == "" {
		return
	}
	if s.receiver != nil {
		s.receiver.AddEvent(NewChunkEvent(content))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeBlockLocked(NewTextBlock(content))
}

// WriteErrorText 将错误以文本写入（IsError=true），错误仅回传给模型：
// 与正文不合并（先 flush 已有正常文本），连续错误文本会拼接为一个块。
func (s *BlockStream) WriteErrorText(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 错误文本与正常文本不合并：先 flush 已有的正常文本缓冲
	if s.hasPending && !s.pendingIsError {
		s.flushPendingLocked()
	}
	s.pending.WriteString(err.Error())
	s.hasPending = true
	s.pendingIsError = true
}

// writeBlockLocked 要求调用方持有 mu。
func (s *BlockStream) writeBlockLocked(block Block) {
	if tb, ok := block.(*TextBlock); ok {
		// 错误文本与正常文本不合并：类型不同时先 flush 已有缓冲
		if s.hasPending && s.pendingIsError != tb.IsError {
			s.flushPendingLocked()
		}
		s.pending.WriteString(tb.Text)
		s.hasPending = true
		s.pendingIsError = tb.IsError
		return
	}
	s.flushPendingLocked()
	s.blocks = append(s.blocks, block)
}

// flushPendingLocked 将累积的文本拼接结果作为一个 TextBlock 收集。要求调用方持有 mu。
func (s *BlockStream) flushPendingLocked() {
	if !s.hasPending {
		return
	}
	s.hasPending = false
	tb := NewTextBlock(s.pending.String())
	tb.IsError = s.pendingIsError
	s.pendingIsError = false
	s.blocks = append(s.blocks, tb)
	s.pending.Reset()
}

// flush 将组装中的 block 与未完成的文本拼接收集入列表。幂等（assembler/拼接缓冲
// 各自带状态守卫，读取方法可在写入间隙多次调用）。要求调用方持有 mu。
func (s *BlockStream) flush() {
	if b := s.assembler.flush(); b != nil {
		s.writeBlockLocked(b)
	}
	s.flushPendingLocked()
}

// StopReason 设置模型停止生成的原因：以 StopReasonBlock 写入收集列表（重复设置覆盖）。
func (s *BlockStream) StopReason(stopReason StopReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendMetaLocked(NewStopReasonBlock(stopReason))
}

// Usage 设置本次请求的 token 消耗：以 UsageBlock 写入收集列表（重复设置覆盖，
// 兼容 Anthropic 在 message_start 与 message_delta 两次上报）。
func (s *BlockStream) Usage(usage *Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendMetaLocked(NewUsageBlock(usage))
}

// appendMetaLocked 追加元数据 block：同类型已存在时原位覆盖（不产生重复）。要求调用方持有 mu。
func (s *BlockStream) appendMetaLocked(b Block) {
	for i, old := range s.blocks {
		if old.Type() == b.Type() {
			s.blocks[i] = b
			return
		}
	}
	s.blocks = append(s.blocks, b)
}

// snapshot flush 后返回全部已收集 block（含元数据块）的拷贝。
func (s *BlockStream) snapshot() []Block {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	blocks := make([]Block, len(s.blocks))
	copy(blocks, s.blocks)
	return blocks
}

// ReadBlocks 返回已收集的内容 Block（自动 flush 未完成的组装/拼接）；
// 元数据 block（usage/stop_reason）不进入内容列表，分别经 GetUsage/GetStopReason 取回。
func (s *BlockStream) ReadBlocks() Blocks {
	content := make(Blocks, 0)
	for _, b := range s.snapshot() {
		switch b.(type) {
		case *UsageBlock, *StopReasonBlock:
			continue
		}
		content = append(content, b)
	}
	return content
}

// GetBlock 按类型取回全部匹配的 Block（含元数据块，自动 flush；无匹配返回空切片）。
func (s *BlockStream) GetBlock(blockType BlockType) Blocks {
	result := make(Blocks, 0)
	for _, b := range s.snapshot() {
		if b.Type() == blockType {
			result = append(result, b)
		}
	}
	return result
}

// GetFirstBlock 按类型取回第一个匹配的 Block（含元数据块，无匹配时返回 nil）。
func (s *BlockStream) GetFirstBlock(blockType BlockType) Block {
	for _, b := range s.snapshot() {
		if b.Type() == blockType {
			return b
		}
	}
	return nil
}

// GetStopReason 返回模型停止生成的原因（GetFirstBlock 取 StopReasonBlock，未上报时默认 end_turn）。
func (s *BlockStream) GetStopReason() StopReason {
	if sb, ok := s.GetFirstBlock(BlockTypeStopReason).(*StopReasonBlock); ok {
		return sb.Reason
	}
	return StopReasonEndTurn
}

// GetUsage 返回本次请求的 token 消耗（GetFirstBlock 取 UsageBlock，未上报时返回 nil）。
func (s *BlockStream) GetUsage() *Usage {
	if ub, ok := s.GetFirstBlock(BlockTypeUsage).(*UsageBlock); ok {
		return ub.Usage
	}
	return nil
}

// emit 通过 receiver 向外推送客户端事件。
func (s *BlockStream) emit(evt *ClientEvent) {
	if s.receiver != nil {
		s.receiver.AddEvent(evt)
	}
}

type Stream interface {
	Type() StreamType
}

type Start struct {
	Stream
}

func (s *Start) Type() StreamType {
	return StreamStartType
}

type TextBlockStart struct {
	Stream
}

func (s *TextBlockStart) Type() StreamType {
	return BlockStartType
}

type ThinkingBlockStart struct {
	Stream
}

func (s *ThinkingBlockStart) Type() StreamType {
	return BlockStartType
}

type ToolUseBlockStart struct {
	Stream
	Id   string
	Name string
}

func (s *ToolUseBlockStart) Type() StreamType {
	return BlockStartType
}

// -------- 增量 Stream 项 --------

// Delta 是一段内容增量：只携带内容本身，不区分文本/思考链/工具入参，
// 语义由它所属的当前 block（最近一次 BlockStart）决定。
type Delta struct {
	Stream
	Content string
}

func (d *Delta) Type() StreamType { return DeltaType }
