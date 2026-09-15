package agent

import (
	"math"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
	sdklog "github.com/chuccp/go-agent-sdk/log"
	"github.com/chuccp/go-agent-sdk/util"
)

// Compressor 上下文压缩策略接口。
type Compressor interface {
	Compress(context Context, messages []*chat.Message) *chat.Message
}

type Summary interface {
	// LoadSummary 读取压缩摘要；返回 nil 表示尚未压缩（等价于分界点 0）。
	// 返回值需与 SaveSummary 写入的一致。
	LoadSummary(sessionID string) (*chat.Message, error)

	// SaveSummary 保存压缩摘要（记录分界点），不删除任何历史消息。
	// summary.Start 即分界点：Start < summary.Start 的旧消息在上下文中由摘要取代。
	SaveSummary(sessionID string, summary *chat.Message) error
}

type CompressorOptions struct {
	maxContextLength int
	keepRatio        float64
}

func (o *CompressorOptions) MaxContextLength() int {
	return o.maxContextLength
}
func (o *CompressorOptions) KeepRatio() float64 {
	return o.keepRatio
}

// CutCompressor 只做强行切割的压缩器：不调用 LLM、不生成摘要。切割本身由
// CompressorManager 按比例完成（见 CompressorManager.compress），这里只把被切掉的
// 那段旧历史压成一条占位消息，告诉模型前面还有对话、只是已被截断。
//
// 适用于「宁可丢掉上下文也要把请求发出去」的场景：零额外调用、零延迟，
// 代价是被丢弃的对话内容不可恢复。
type CutCompressor struct{}

var _ Compressor = (*CutCompressor)(nil)

// defaultKeepRatio CompressorOptions.KeepRatio 的缺省值：保留后一半，压缩后上下文大致减半。
const defaultKeepRatio = 0.5

// cutPlaceholder 是被丢弃历史在上下文中的占位文本：免得模型把保留下来的尾巴
// 当成对话的开头。
const cutPlaceholder = "（更早的历史对话已超出上下文上限被截断）"

// Compress 把被切掉的那段历史压成一条占位消息：分界点取该段的末尾，
// 调用方把它拼在保留段前面。没有历史可压（空切片）时返回 nil。
func (c *CutCompressor) Compress(ctx Context, messages []*chat.Message) *chat.Message {
	if len(messages) == 0 {
		return nil
	}
	return &chat.Message{
		Start:   cutBoundary(messages),
		Role:    chat.RoleUser,
		Content: chat.Blocks{chat.NewFullTextBlock(cutPlaceholder)},
	}
}

// cutBoundary 返回被切掉那段的末尾位置，也就是新的分界点：Start 小于它的历史由
// 摘要/占位消息取代，它正好等于保留段第一条的 Start。空切片返回 0。
func cutBoundary(dropped []*chat.Message) uint64 {
	if len(dropped) == 0 {
		return 0
	}
	last := dropped[len(dropped)-1]
	return last.Start + last.Offset
}

// SummaryCompressor 用大模型把被切掉的历史压成一段摘要：不丢上下文，代价是每次
// 压缩多一次同步 LLM 调用——压在 buildRequest 里，本轮首字会晚一个来回。
//
// 摘要失败（调用报错、返回空文本）时返回 nil：切割照常生效，manager 只记分界点，
// 那一段历史就真丢了，退化成 CutCompressor 的行为——宁可丢上下文，也别让这一轮发不出去。
type SummaryCompressor struct {
	// Prompt 摘要提示词，留空用内置默认提示词。
	Prompt string

	// ServiceID 指定用哪个已注册的 Service 生成摘要，留空用默认 Service。
	// 摘要是后台活儿，通常可以用更便宜的模型跑。
	ServiceID string

	// MaxTokens 摘要输出上限，<=0 用 defaultSummaryMaxTokens。
	MaxTokens int
}

var _ Compressor = (*SummaryCompressor)(nil)

const (
	defaultSummaryMaxTokens = 1024
	summaryPrefix           = "[历史摘要] "
	defaultSummaryPrompt    = "请把下面这段对话历史压缩成一段简洁的摘要，保留关键事实、结论、" +
		"待办与未解决的问题。如果历史里已经包含之前的摘要，把新旧信息合并成一段，不要丢掉" +
		"旧摘要里的要点。直接输出摘要正文，不要加标题或其他说明。"
)

// Compress 让大模型把被切掉的那段历史压成摘要消息；没有历史可压或摘要失败时返回 nil。
func (c *SummaryCompressor) Compress(ctx Context, messages []*chat.Message) *chat.Message {
	if len(messages) == 0 || ctx == nil || ctx.GetChat() == nil {
		return nil
	}
	text, err := c.summarize(ctx, messages)
	if err != nil {
		sdklog.Error("[compressor] 生成摘要失败，本轮退化成强行切割",
			"session", ctx.SessionId(), "boundary", cutBoundary(messages), "error", err)
		return nil
	}
	if util.IsBlank(text) {
		sdklog.Warn("[compressor] 模型没吐出摘要，本轮退化成强行切割",
			"session", ctx.SessionId(), "boundary", cutBoundary(messages))
		return nil
	}
	return &chat.Message{
		Start:   cutBoundary(messages),
		Role:    chat.RoleUser,
		Content: chat.Blocks{chat.NewFullTextBlock(summaryPrefix + text)},
	}
}

// summarize 调一次大模型，把 messages 压成摘要正文。
func (c *SummaryCompressor) summarize(ctx Context, messages []*chat.Message) (string, error) {
	config := chat.DefaultConfig()
	if util.IsNotBlank(c.ServiceID) {
		config.ID(c.ServiceID)
	}
	if c.MaxTokens > 0 {
		config.Set(chat.MaxOutputTokensConfigKey, c.MaxTokens)
	} else {
		config.Set(chat.MaxOutputTokensConfigKey, defaultSummaryMaxTokens)
	}
	// 摘要请求不带工具：模型只需要读历史、写摘要，别让它顺手调个工具。
	request := chat.NewMessages(config, nil)
	request.AddMessage(&chat.Message{
		Role:    chat.RoleUser,
		Content: chat.Blocks{chat.NewFullTextBlock(c.prompt() + "\n\n" + historyText(messages))},
	})

	// 接收者留空：摘要块只在这里被读完取文本，不往会话事件流里推。
	// （Context 也不满足 chat.BlockReceiver —— 实现它的是 Agent。）
	stream := chat.NewBlockStream(nil)
	if err := ctx.GetChat().ChatWithStream(ctx, request, stream); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, b := range stream.ReadBlockGroup().Content {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (c *SummaryCompressor) prompt() string {
	if util.IsNotBlank(c.Prompt) {
		return c.Prompt
	}
	return defaultSummaryPrompt
}

// historyText 把被切掉的历史拼成带角色的纯文本，供摘要模型阅读：只保留文本、
// 工具调用与工具结果，thinking 之类的过程块不进摘要材料。
func historyText(messages []*chat.Message) string {
	var sb strings.Builder
	for _, m := range messages {
		for _, line := range blockLines(m.Content) {
			sb.WriteString(string(m.Role))
			sb.WriteString(": ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// blockLines 展开一条消息里的块。UserBlock 是事件流包装器，要拆开看里面的内容。
func blockLines(blocks chat.Blocks) []string {
	var lines []string
	for _, b := range blocks {
		switch v := b.(type) {
		case *chat.UserBlock:
			lines = append(lines, blockLines(v.Content)...)
		case *chat.TextBlock:
			if util.IsNotBlank(v.Text) {
				lines = append(lines, v.Text)
			}
		case *chat.ToolUseBlock:
			lines = append(lines, "[调用工具 "+v.Name+"]")
		case *chat.ToolResultBlock:
			lines = append(lines, "[工具结果] "+strings.Join(blockLines(v.Content), " "))
		}
	}
	return lines
}

// isSafeCut 报告从 messages[cut] 开始截断是否安全。
//
// 唯一要避开的是切开 tool_use/tool_result 这一对：tool_result 必须以 user 消息的
// 形式跟在配对的 tool_use 之后，留下 tool_result、丢掉 tool_use，Anthropic 会直接
// 判 400，且失败在请求发出之后、表现为整轮报错。落在这种消息上就把切点往后推。
//
// 其余情况都安全：返回的占位消息是 user 消息且注入在上下文最前面，后面紧跟
// assistant 消息也不违反「首条消息必须是 user」的约束。
func isSafeCut(msg *chat.Message) bool {
	return msg.Role != chat.RoleUser || !hasToolResult(msg.Content)
}

// hasToolResult 查找块中的 tool_result。UserBlock 是事件流包装器，进上下文时会被
// 展开（见 blocksForContext），包在里面的 tool_result 一样会出现在消息顶层。
func hasToolResult(blocks chat.Blocks) bool {
	for _, b := range blocks {
		switch v := b.(type) {
		case *chat.ToolResultBlock:
			return true
		case *chat.UserBlock:
			if hasToolResult(v.Content) {
				return true
			}
		}
	}
	return false
}

func DefaultCompressorOptions() *CompressorOptions {
	return &CompressorOptions{
		maxContextLength: 100_000,
		keepRatio:        0.5, // 超水位后保留一半
	}
}

type CompressorOption func(*CompressorOptions)

func WithMaxContextLength(maxContextLength int) CompressorOption {
	return func(o *CompressorOptions) {
		o.maxContextLength = maxContextLength
	}
}
func WithKeepRatio(keepRatio float64) CompressorOption {
	return func(o *CompressorOptions) {
		o.keepRatio = keepRatio
	}
}

type CompressorManager struct {
	compressor        Compressor
	summary           Summary
	contextLength     int
	summaryMessage    *chat.Message
	sessionId         string
	compressorOptions *CompressorOptions
}

// NewCompressorManager 创建压缩器管理器。
func NewCompressorManager(sessionId string, compressor Compressor,
	compressorOptions *CompressorOptions, summary Summary) *CompressorManager {
	return &CompressorManager{
		compressor:        compressor,
		contextLength:     0,
		compressorOptions: compressorOptions,
		summary:           summary,
		sessionId:         sessionId,
	}
}

// shouldCompress 报告当前上下文是否已达到触发压缩的水位。
// contextLength 取上一轮的真实用量（见 UpdateUsage），超过 maxContextLength 才压；
// keepRatio 只管「压完保留多少」，不参与水位判断。
func (m *CompressorManager) shouldCompress() bool {
	if m == nil || m.compressorOptions == nil {
		return false
	}
	maxContextLength := m.compressorOptions.MaxContextLength()
	if maxContextLength <= 0 {
		return false
	}
	return m.contextLength >= maxContextLength
}
func (m *CompressorManager) UpdateUsage(usage *chat.Usage) {
	if usage != nil {
		if usage.OutputTokens > 0 && usage.InputTokens > 0 {
			m.contextLength = usage.OutputTokens + usage.InputTokens + usage.CacheInputTokens
		}
	}

}

func (m *CompressorManager) loadSummary() (*chat.Message, error) {
	if m.summaryMessage != nil {
		return m.summaryMessage, nil
	}
	if m.summary == nil {
		m.summaryMessage = &chat.Message{
			Start: 0,
		}
		return m.summaryMessage, nil
	}
	if m.summary != nil {
		summaryMessage, err := m.summary.LoadSummary(m.sessionId)
		if err != nil {
			return nil, err
		}
		if summaryMessage == nil {
			m.summaryMessage = &chat.Message{
				Start: 0,
			}
		} else {
			m.summaryMessage = summaryMessage
		}
	}
	return m.summaryMessage, nil
}

// Compress 执行压缩：这里先按比例强行切割，再把切掉的那段交给 Compressor 压缩。
func (m *CompressorManager) compress(context Context, messages []*chat.Message) ([]*chat.Message, bool) {
	if m == nil || m.compressor == nil {
		return nil, false
	}
	// 没超水位就别动：否则每轮都会再切一刀，把上下文压到水位以下
	if !m.shouldCompress() {
		return messages, false
	}
	// 这里按比例切割好，将切割好的往下传
	cut := m.cutIndex(messages)
	if cut <= 0 {
		return messages, false // 一条都没切掉，也就没东西可压
	}

	// Compress只负责压缩，不做切割
	msg := m.compressor.Compress(context, messages[:cut])
	if msg == nil {
		// 压缩器没产出内容，但分界点照样要记：否则重启后这段刚切掉的历史会被
		// 整段加载回来、再压一遍。只记分界点——空 content 的消息进上下文时
		// 本来就会被 buildRequest 跳过，不用往返回的历史里再塞一条。
		m.saveSummary(&chat.Message{Start: cutBoundary(messages[:cut]), Role: chat.RoleUser})
		return messages[cut:], true
	}

	// 保存 summary：分界点必须落盘，否则重启后旧历史会被整段重新加载回来
	m.saveSummary(msg)

	return append([]*chat.Message{msg}, messages[cut:]...), true
}

// saveSummary 记录新的压缩分界点：落盘 + 顶掉内存缓存。
// loadSummary 优先返回缓存，不更新的话后续加载还会拿着旧分界点。
// 落盘失败只记日志：切割已经生效，本轮先按内存里的分界点走，
// 重启后最多退回旧分界点、把这段历史再压一次。
func (m *CompressorManager) saveSummary(msg *chat.Message) {
	if msg == nil {
		return // 没有分界点可记，别把已经记下的清掉
	}
	m.summaryMessage = msg
	if m.summary == nil {
		return
	}
	if err := m.summary.SaveSummary(m.sessionId, msg); err != nil {
		sdklog.Error("[compressor] SaveSummary failed", "session", m.sessionId, "start", msg.Start, "error", err)
	}
}

// cutIndex 计算切割点：messages[cutIndex:] 是保留部分，[0,cutIndex) 交给 Compressor 压缩。
// 返回 0 表示本轮不切（比例配成 >=1、消息太少，或找不到安全的切点）。
func (m *CompressorManager) cutIndex(messages []*chat.Message) int {
	// 只保留末尾 ratio 比例的消息，前面被切掉的那段才是拿去压缩的材料。
	// 没配 options 就按缺省比例切。
	ratio := defaultKeepRatio
	if m.compressorOptions != nil && m.compressorOptions.KeepRatio() > 0 {
		ratio = m.compressorOptions.KeepRatio()
	}
	if ratio >= 1 || len(messages) == 0 {
		return 0
	}
	keep := int(math.Ceil(float64(len(messages)) * ratio)) // 向上取整，至少保留一条
	if keep >= len(messages) {
		return 0
	}
	cut := len(messages) - keep
	// 切点必须落在安全边界上：别把 tool_use/tool_result 切成两半，
	// 不安全就继续往后推，等于多丢几条；推到底说明这段切不得，本轮不动。
	for cut < len(messages) && !isSafeCut(messages[cut]) {
		cut++
	}
	if cut >= len(messages) {
		return 0
	}
	return cut
}
