package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/chuccp/go-agent-sdk/chat"
)

// Summarizer 摘要生成函数签名。
// ctx 提供会话上下文，existingSummary 是已有摘要（增量摘要时使用），newMessages 是待摘要的消息。
type Summarizer func(ctx LoopContext, existingSummary string, newMessages []*chat.Message) (string, error)

// DefaultCompressor 统一的上下文压缩器，同时支持滑动窗口和 LLM 摘要。
//
// 工作流程：
//  1. 滑动窗口：当未压缩消息超过 MaxMessages 时，从头部标记旧消息 IsCompressor=true
//  2. 摘要压缩：当未压缩消息超过 ThresholdMessages 时，用 LLM 将旧消息压缩为摘要
//  3. 持久化：通过 ctx.GetCompressorStore() 保存/恢复摘要文本和压缩标记
//
// 两种压缩可以同时启用：先触发滑动窗口，再触发摘要。仅需其一时，将另一个设为 0。
type DefaultCompressor struct {
	// MaxMessages 滑动窗口：保留最近 N 条未压缩消息（0 = 不启用滑动窗口）。
	MaxMessages int

	// ThresholdMessages 摘要压缩：未压缩消息超过此阈值时触发（0 = 不启用摘要压缩）。
	ThresholdMessages int

	// KeepRecent 摘要压缩：保留最近 K 条原消息不压缩（0 = ThresholdMessages/2）。
	KeepRecent int

	// Summarizer 自定义摘要生成函数（nil = 使用内置默认实现）。
	Summarizer Summarizer
}

var _ Compressor = (*DefaultCompressor)(nil)

func (c *DefaultCompressor) Compress(ctx LoopContext, messages []*chat.Message) *chat.Message {
	sessionID := ctx.SessionId()

	// --- 1. 滑动窗口 ---
	if c.MaxMessages > 0 {
		c.slidingWindow(messages)
	}

	// --- 2. 摘要压缩 ---
	if c.ThresholdMessages > 0 {
		return c.summaryCompress(ctx, sessionID, messages)
	}

	return nil
}

// slidingWindow 从头部标记旧消息为已压缩。
func (c *DefaultCompressor) slidingWindow(messages []*chat.Message) {
	activeCount := 0
	for _, m := range messages {
		if !m.IsCompressor {
			activeCount++
		}
	}
	if activeCount <= c.MaxMessages {
		return
	}

	toMark := activeCount - c.MaxMessages
	marked := 0
	for _, m := range messages {
		if marked >= toMark {
			break
		}
		if !m.IsCompressor {
			m.IsCompressor = true
			marked++
		}
	}

	// 确保第一条未压缩消息是 role=user
	for _, m := range messages {
		if !m.IsCompressor {
			if m.Role == chat.RoleAssistant {
				m.IsCompressor = true
				continue
			}
			break
		}
	}
}

// summaryCompress 摘要压缩：超阈值时生成摘要，标记旧消息。
func (c *DefaultCompressor) summaryCompress(ctx LoopContext, sessionID string, messages []*chat.Message) *chat.Message {
	store := ctx.GetCompressorStore()

	// 恢复已有摘要
	existingSummary := ""
	if store != nil {
		existingSummary, _ = store.LoadSummary(sessionID)
	}

	// 统计未压缩消息
	activeCount := 0
	for _, m := range messages {
		if !m.IsCompressor {
			activeCount++
		}
	}
	if activeCount <= c.ThresholdMessages {
		return c.buildSummaryMsg(existingSummary)
	}

	keep := c.KeepRecent
	if keep <= 0 {
		keep = c.ThresholdMessages / 2
	}

	// 收集待压缩消息（前面的旧消息）
	var toCompress []*chat.Message
	for _, m := range messages {
		if !m.IsCompressor {
			toCompress = append(toCompress, m)
		}
	}
	if len(toCompress) > keep {
		toCompress = toCompress[:len(toCompress)-keep]
	} else {
		return c.buildSummaryMsg(existingSummary)
	}

	// 生成摘要
	summary, err := c.generateSummary(ctx, existingSummary, toCompress)
	if err != nil {
		// 降级：滑动窗口（如果还没启用则临时启用）
		if c.MaxMessages <= 0 {
			fallback := &DefaultCompressor{MaxMessages: c.ThresholdMessages}
			fallback.Compress(ctx, messages)
		}
		return c.buildSummaryMsg(existingSummary)
	}

	// 标记已压缩
	for _, m := range toCompress {
		m.IsCompressor = true
	}

	// 持久化
	if store != nil {
		if len(toCompress) > 0 {
			store.MarkCompressed(sessionID, toCompress)
		}
		if summary != "" {
			store.SaveSummary(sessionID, summary)
		}
	}

	return c.buildSummaryMsg(summary)
}

func (c *DefaultCompressor) buildSummaryMsg(summary string) *chat.Message {
	if summary == "" {
		return nil
	}
	return &chat.Message{
		Role:    chat.RoleUser,
		Content: chat.Blocks{chat.NewFullTextBlock("[历史摘要] " + summary)},
	}
}

func (c *DefaultCompressor) generateSummary(ctx LoopContext, existingSummary string, newMessages []*chat.Message) (string, error) {
	// 优先使用自定义 Summarizer
	if c.Summarizer != nil {
		return c.Summarizer(ctx, existingSummary, newMessages)
	}

	// 内置默认实现：通过 ctx.GetService() 调用 LLM
	service := ctx.GetService("")
	if service == nil {
		return "", fmt.Errorf("no LLM service available")
	}

	var parts []string
	if existingSummary != "" {
		parts = append(parts, "[已有摘要] "+existingSummary)
	}
	for _, m := range newMessages {
		for _, b := range m.Content {
			if tb, ok := b.(*chat.TextBlock); ok {
				parts = append(parts, string(m.Role)+": "+tb.Text)
			}
		}
	}
	prompt := "请将以下对话历史压缩为简洁摘要，保留关键信息：\n\n" + strings.Join(parts, "\n")

	req := &chat.Request{
		MaxTokens: 1024,
		Messages:  []chat.Message{chat.NewTextMessage(prompt)},
		Stream:    false,
	}
	stream := chat.NewBlockStream(nil)
	if err := service.ChatWithStream(context.Background(), req, stream); err != nil {
		return "", err
	}
	for _, b := range stream.ReadBlockGroup().Content {
		if tb, ok := b.(*chat.TextBlock); ok {
			return tb.Text, nil
		}
	}
	return "", fmt.Errorf("no text in summary response")
}
