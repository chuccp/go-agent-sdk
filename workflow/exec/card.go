package exec

import (
	"fmt"
	"strings"
)

// FlowRules 全局执行守则：随每张卡片注入，所有 flow 共用（自然度的总源头）。
const FlowRules = `【执行守则】
1. 先挖掘再提问：提问前先确认用户原话与历史中是否已含答案，已知则直接用 activate_flow 登记并推进，绝不重复问
2. 零提问直通：信息完备时直接执行，不制造形式化的确认
3. 跑题：先回答用户的新问题，然后可用一句话温柔桥接（带出当前进度），但不连续提醒两次
4. 不机械播报进度，不暴露 flow/node/步骤编号等内部概念，保持平常对话语气
5. 失败说人话：用自然语言解释问题并给出重试/调整/放弃选项
6. 中断后回归时，先用一句话概括进度再推进
7. 对话中获得的任何新信息（确认结果、附加要求、修改），先用 activate_flow 补录，再执行节点`

// StepStatus 步骤状态。
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepCompleted StepStatus = "completed"
)

// CardProgress 渲染卡片所需的进度信息（由工具层 FlowStore 计算，LLM 不汇报进度）。
type CardProgress struct {
	Status map[string]StepStatus
	Next   string // 建议下一步：第一个依赖满足的未完成步骤；空表示全部完成
}

// RenderCard 渲染完整卡片：执行守则 + 步骤剧本 + 当前进度 + 建议下一步。
func (w *Workflow) RenderCard(progress *CardProgress) string {
	var sb strings.Builder
	sb.WriteString(FlowRules)
	sb.WriteString("\n\n【当前 Flow】")
	sb.WriteString(fmt.Sprintf("%s（%s）", w.Name, w.Id))
	if w.Description != "" {
		sb.WriteString("\n" + w.Description)
	}
	sb.WriteString("\n\n【步骤】")
	for i, s := range w.Steps {
		mark := "○"
		if progress != nil && progress.Status[s.name] == StepCompleted {
			mark = "✓"
		}
		sb.WriteString(fmt.Sprintf("\n%d. %s %s（%s）", i+1, mark, s.title, s.kind))
		if s.script != "" {
			sb.WriteString("\n" + strings.TrimSpace(s.script))
		}
		if s.kind == StepExec && s.IsIterating() {
			sb.WriteString(fmt.Sprintf("\n[对 %s 逐项执行]", s.iterate))
		}
		if len(s.doneWhen) > 0 {
			sb.WriteString(fmt.Sprintf("\n[完成条件: input 登记 %s]", strings.Join(s.doneWhen, ", ")))
		}
	}
	if progress != nil {
		sb.WriteString("\n\n【进度】" + progressSummary(w, progress))
	}
	return sb.String()
}

// ProgressFooter 渲染一行紧凑进度脚标，附在每次 flow 工具的 tool_result 末尾，
// 防止长对话中 LLM 对剧本的注意力衰减。
func (w *Workflow) ProgressFooter(progress *CardProgress) string {
	return "【进度】" + progressSummary(w, progress)
}

// progressSummary 生成进度摘要：已完成步骤✓ + 建议下一步。
func progressSummary(w *Workflow, p *CardProgress) string {
	var done []string
	for _, s := range w.Steps {
		if p.Status[s.name] == StepCompleted {
			done = append(done, s.name+"✓")
		}
	}
	left := "(无)"
	if len(done) > 0 {
		left = strings.Join(done, " ")
	}
	next := p.Next
	if next == "" {
		next = "全部完成，调用 finish_flow 收尾"
	}
	return fmt.Sprintf("%s → 下一步: %s", left, next)
}
