package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
)

// ==================== TodoStore ====================

// TodoStore 是所有 Todo 工具共享的任务存储，线程安全。
type TodoStore struct {
	mu     sync.RWMutex
	tasks  map[string]*TodoTask
	nextID int
}

// TodoTask 字段对齐 Claude Code 官方 Task 模型。
// 参考: https://code.claude.com/docs/en/agent-sdk/todo-tracking
type TodoTask struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"active_form,omitempty"`
	Status      string         `json:"status"` // pending | in_progress | completed
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

// NewTodoStore 创建任务存储。
func NewTodoStore() *TodoStore {
	return &TodoStore{tasks: make(map[string]*TodoTask)}
}

// NewTodoTools 创建 4 个 Todo 工具（共享同一存储）。
// 返回: TaskCreate, TaskUpdate, TaskList, TaskGet
func NewTodoTools() (create, update, list, get agent.ToolExecutor) {
	store := NewTodoStore()
	return &TaskCreateTool{store}, &TaskUpdateTool{store}, &TaskListTool{store}, &TaskGetTool{store}
}

// ==================== TaskCreateTool ====================

type TaskCreateTool struct{ store *TodoStore }

// Name 返回工具名称。
func (t *TaskCreateTool) Name() string { return t.Definition().Name }

func (t *TaskCreateTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "task_create",
		Description: "创建一个新的待办任务。新任务初始状态为 pending。" +
			"返回任务 ID 供 task_update / task_get 使用。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject": map[string]any{
					"type":        "string",
					"description": "任务标题，祈使句。例如: 'Fix authentication bug'",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "详细描述，说明需要做什么",
				},
				"active_form": map[string]any{
					"type":        "string",
					"description": "进行中时 spinner 显示的文本（现在进行时）。例如: 'Fixing authentication bug'",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "任意键值对元数据",
				},
			},
			"required": []string{"subject", "description"},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：创建一个新任务，结果写入 writer。
func (t *TaskCreateTool) Execute(turn *agent.Turn, writer chat.StreamWriter) error {
	args := turn.Args()
	subject, _ := args["subject"].(string)
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("缺少 subject 参数")
	}
	desc, _ := args["description"].(string)
	if strings.TrimSpace(desc) == "" {
		return fmt.Errorf("缺少 description 参数")
	}
	activeForm, _ := args["active_form"].(string)
	meta, _ := args["metadata"].(map[string]any)

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	t.store.nextID++
	now := time.Now().Unix()
	task := &TodoTask{
		ID:          fmt.Sprintf("%d", t.store.nextID),
		Subject:     strings.TrimSpace(subject),
		Description: strings.TrimSpace(desc),
		ActiveForm:  strings.TrimSpace(activeForm),
		Status:      "pending",
		Metadata:    meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	t.store.tasks[task.ID] = task

	return writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("任务已创建:\n%s", formatTaskDetail(task))))
}

// ==================== TaskUpdateTool ====================

type TaskUpdateTool struct{ store *TodoStore }

// Name 返回工具名称。
func (t *TaskUpdateTool) Name() string { return t.Definition().Name }

func (t *TaskUpdateTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "task_update",
		Description: "更新一个已有任务。可修改状态、标题、描述、依赖关系等。" +
			" 状态流转: pending → in_progress → completed。" +
			" 设为 deleted 可删除任务。" +
			" add_blocks / add_blocked_by 会双向同步依赖关系——" +
			" 例如对 A 执行 add_blocks: [B] 会同时更新 A.blocks 和 B.blocked_by。" +
			" 只有 blocked_by 中所有未完成的任务都 completed 后，才能将任务设为 in_progress。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "要更新的任务 ID（必需）",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "新的任务标题",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "新的任务描述",
				},
				"active_form": map[string]any{
					"type":        "string",
					"description": "新的 spinner 显示文本",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "新状态",
					"enum":        []string{"pending", "in_progress", "completed", "deleted"},
				},
				"add_blocks": map[string]any{
					"type":        "array",
					"description": "此任务阻塞哪些任务 ID（双向同步: 同时更新对方的 blocked_by）",
					"items":       map[string]any{"type": "string"},
				},
				"remove_blocks": map[string]any{
					"type":        "array",
					"description": "从此任务的 blocks 列表中移除这些任务 ID（双向同步）",
					"items":       map[string]any{"type": "string"},
				},
				"add_blocked_by": map[string]any{
					"type":        "array",
					"description": "哪些任务 ID 阻塞此任务（双向同步: 同时更新对方的 blocks）",
					"items":       map[string]any{"type": "string"},
				},
				"remove_blocked_by": map[string]any{
					"type":        "array",
					"description": "从此任务的 blocked_by 列表中移除这些任务 ID（双向同步）",
					"items":       map[string]any{"type": "string"},
				},
				"owner": map[string]any{
					"type":        "string",
					"description": "任务负责人（agent 名称）",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "合并到现有 metadata 的键值对（值为 null 的 key 会被删除）",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：更新一个已有任务，结果写入 writer。
func (t *TaskUpdateTool) Execute(turn *agent.Turn, writer chat.StreamWriter) error {
	args := turn.Args()
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("缺少 task_id 参数")
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()

	task, ok := t.store.tasks[taskID]
	if !ok {
		return fmt.Errorf("任务不存在: %s", taskID)
	}

	if v, ok := args["subject"].(string); ok && strings.TrimSpace(v) != "" {
		task.Subject = strings.TrimSpace(v)
	}
	if v, ok := args["description"].(string); ok && strings.TrimSpace(v) != "" {
		task.Description = strings.TrimSpace(v)
	}
	if v, ok := args["active_form"].(string); ok {
		task.ActiveForm = strings.TrimSpace(v)
	}
	if v, ok := args["owner"].(string); ok {
		task.Owner = strings.TrimSpace(v)
	}

	// --- 依赖：增量添加（双向同步） ---
	if ids := toStringSlice(args["add_blocks"]); len(ids) > 0 {
		t.store.addBlocks(task, ids)
	}
	if ids := toStringSlice(args["remove_blocks"]); len(ids) > 0 {
		t.store.removeBlocks(task, ids)
	}
	if ids := toStringSlice(args["add_blocked_by"]); len(ids) > 0 {
		t.store.addBlockedBy(task, ids)
	}
	if ids := toStringSlice(args["remove_blocked_by"]); len(ids) > 0 {
		t.store.removeBlockedBy(task, ids)
	}

	// --- metadata 合并 ---
	if raw, ok := args["metadata"]; ok {
		if meta, ok := raw.(map[string]any); ok {
			task.Metadata = mergeMetadata(task.Metadata, meta)
		}
	}

	// --- 状态变更 ---
	if status, ok := args["status"].(string); ok && status != "" {
		if status == "in_progress" {
			if err := t.store.checkDeps(task); err != nil {
				return err
			}
		}
		task.Status = status
	}

	task.UpdatedAt = time.Now().Unix()
	return writer.WriteBlock(chat.NewTextBlock(fmt.Sprintf("任务已更新:\n%s", formatTaskDetail(task))))
}

// ==================== 双向依赖操作（需持有 mu.Lock） ====================

func (s *TodoStore) addBlocks(task *TodoTask, ids []string) {
	for _, id := range ids {
		if id == task.ID {
			continue
		}
		if !contains(task.Blocks, id) {
			task.Blocks = append(task.Blocks, id)
		}
		// 双向同步: A blocks B → B.blockedBy 包含 A
		if other, ok := s.tasks[id]; ok {
			if !contains(other.BlockedBy, task.ID) {
				other.BlockedBy = append(other.BlockedBy, task.ID)
			}
		}
	}
}

func (s *TodoStore) removeBlocks(task *TodoTask, ids []string) {
	task.Blocks = removeItems(task.Blocks, ids)
	for _, id := range ids {
		if other, ok := s.tasks[id]; ok {
			other.BlockedBy = removeItems(other.BlockedBy, []string{task.ID})
		}
	}
}

func (s *TodoStore) addBlockedBy(task *TodoTask, ids []string) {
	for _, id := range ids {
		if id == task.ID {
			continue
		}
		if !contains(task.BlockedBy, id) {
			task.BlockedBy = append(task.BlockedBy, id)
		}
		// 双向同步: B blocks A → B.blocks 包含 A
		if other, ok := s.tasks[id]; ok {
			if !contains(other.Blocks, task.ID) {
				other.Blocks = append(other.Blocks, task.ID)
			}
		}
	}
}

func (s *TodoStore) removeBlockedBy(task *TodoTask, ids []string) {
	task.BlockedBy = removeItems(task.BlockedBy, ids)
	for _, id := range ids {
		if other, ok := s.tasks[id]; ok {
			other.Blocks = removeItems(other.Blocks, []string{task.ID})
		}
	}
}

// checkDeps 要求调用方持有 mu.Lock。
func (s *TodoStore) checkDeps(task *TodoTask) error {
	for _, depID := range task.BlockedBy {
		dep, ok := s.tasks[depID]
		if !ok {
			return fmt.Errorf("无法将任务 %s 设为 in_progress: 依赖任务 %s 不存在", task.ID, depID)
		}
		if dep.Status != "completed" {
			return fmt.Errorf("无法将任务 %s 设为 in_progress: 依赖任务 %s 尚未完成（当前: %s）", task.ID, depID, dep.Status)
		}
	}
	return nil
}

// ==================== TaskListTool ====================

type TaskListTool struct{ store *TodoStore }

// Name 返回工具名称。
func (t *TaskListTool) Name() string { return t.Definition().Name }

func (t *TaskListTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name: "task_list",
		Description: "列出所有活跃任务（不含已删除）及其状态摘要。" +
			" 返回每个任务的 id、status、subject、owner、blocked_by。" +
			" blocked_by 中已完成的任务会被自动过滤。" +
			" 按 ID 数字顺序排列。",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：列出所有活跃任务，结果写入 writer。
func (t *TaskListTool) Execute(_ *agent.Turn, writer chat.StreamWriter) error {
	t.store.mu.RLock()
	defer t.store.mu.RUnlock()

	if len(t.store.tasks) == 0 {
		return writer.WriteBlock(chat.NewTextBlock("(无任务)"))
	}

	tasks := make([]*TodoTask, 0, len(t.store.tasks))
	for _, task := range t.store.tasks {
		if task.Status != "deleted" {
			tasks = append(tasks, task)
		}
	}

	sort.Slice(tasks, func(i, j int) bool {
		idi, erri := strconv.Atoi(tasks[i].ID)
		idj, errj := strconv.Atoi(tasks[j].ID)
		if erri != nil || errj != nil {
			return tasks[i].ID < tasks[j].ID
		}
		return idi < idj
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("共 %d 个任务:\n\n", len(tasks)))
	for i, task := range tasks {
		sb.WriteString(formatTaskSummary(t.store, task))
		if i < len(tasks)-1 {
			sb.WriteString("\n")
		}
	}
	return writer.WriteBlock(chat.NewTextBlock(sb.String()))
}

// ==================== TaskGetTool ====================

type TaskGetTool struct{ store *TodoStore }

// Name 返回工具名称。
func (t *TaskGetTool) Name() string { return t.Definition().Name }

func (t *TaskGetTool) Definition() *chat.ToolFunction {
	return &chat.ToolFunction{
		Name:        "task_get",
		Description: "获取一个任务的完整详情: subject、description、status、blocks、blocked_by、owner、metadata。用于在开始任务前了解完整要求。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "要查询的任务 ID",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

// Execute 实现 agent.ToolExecutor 接口：获取一个任务的完整详情，结果写入 writer。
func (t *TaskGetTool) Execute(turn *agent.Turn, writer chat.StreamWriter) error {
	args := turn.Args()
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("缺少 task_id 参数")
	}

	t.store.mu.RLock()
	defer t.store.mu.RUnlock()

	task, ok := t.store.tasks[taskID]
	if !ok {
		return fmt.Errorf("任务不存在: %s", taskID)
	}
	return writer.WriteBlock(chat.NewTextBlock(formatTaskDetail(task)))
}

// ==================== 格式化输出 ====================

// formatTaskSummary 格式化任务摘要（用于 task_list）。
// 官方格式: #{id} [{status}] {subject} ({owner}) [blocked by #{id}]
// 自动过滤 blocked_by 中已完成的依赖。
func formatTaskSummary(store *TodoStore, task *TodoTask) string {
	icon := statusIcon(task.Status)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("#%s [%s] %s", task.ID, icon, task.Subject))
	if task.Owner != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", task.Owner))
	}

	// 过滤已完成的 blocked_by
	openBlockers := make([]string, 0)
	for _, depID := range task.BlockedBy {
		if dep, ok := store.tasks[depID]; ok && dep.Status != "completed" {
			openBlockers = append(openBlockers, depID)
		}
	}
	if len(openBlockers) > 0 {
		sb.WriteString(fmt.Sprintf(" [blocked by #%s]", strings.Join(openBlockers, ", #")))
	}
	return sb.String()
}

// formatTaskDetail 格式化任务完整详情（用于 task_create / task_update / task_get）。
func formatTaskDetail(task *TodoTask) string {
	icon := statusIcon(task.Status)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("#%s [%s] %s\n", task.ID, icon, task.Subject))
	sb.WriteString(fmt.Sprintf("描述: %s\n", task.Description))
	if task.ActiveForm != "" {
		sb.WriteString(fmt.Sprintf("进行中提示: %s\n", task.ActiveForm))
	}
	sb.WriteString(fmt.Sprintf("状态: %s\n", task.Status))
	if task.Owner != "" {
		sb.WriteString(fmt.Sprintf("负责人: %s\n", task.Owner))
	}
	if len(task.BlockedBy) > 0 {
		sb.WriteString(fmt.Sprintf("依赖（需先完成）: %s\n", strings.Join(task.BlockedBy, ", ")))
	}
	if len(task.Blocks) > 0 {
		sb.WriteString(fmt.Sprintf("阻塞（等待本任务）: %s\n", strings.Join(task.Blocks, ", ")))
	}
	if len(task.Metadata) > 0 {
		if b, err := json.Marshal(task.Metadata); err == nil {
			sb.WriteString(fmt.Sprintf("元数据: %s\n", string(b)))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func statusIcon(status string) string {
	switch status {
	case "pending":
		return "○"
	case "in_progress":
		return "◐"
	case "completed":
		return "✓"
	case "deleted":
		return "✗"
	}
	return "?"
}

// ==================== 通用辅助函数 ====================

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch arr := v.(type) {
	case []string:
		return arr
	case []any:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func removeItems(slice []string, ids []string) []string {
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	out := make([]string, 0, len(slice))
	for _, s := range slice {
		if !remove[s] {
			out = append(out, s)
		}
	}
	return out
}

func mergeMetadata(base, patch map[string]any) map[string]any {
	if base == nil {
		base = make(map[string]any)
	}
	for k, v := range patch {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return base
}
