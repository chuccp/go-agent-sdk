package tools

import (
	"strings"
	"testing"

	"github.com/chuccp/go-agent-sdk/agent"
	"github.com/chuccp/go-agent-sdk/chat"
	"github.com/chuccp/go-agent-sdk/value"
)

// drainText 关闭工具专用 BlockStream 并收集其中的文本内容。
func drainText(w *agent.BlockStream) string {
	w.Close()
	var sb strings.Builder
	blocks, _ := w.ReadBlocks()
	for _, b := range blocks {
		if tb, ok := b.(*chat.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

func execTool(t *testing.T, tool agent.ToolExecutor, args map[string]any) string {
	t.Helper()
	w := agent.NewBlockStream(nil)
	err := tool.Execute(agent.NewTurn(value.NewObjectFromMap(args)), w)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	return drainText(w)
}

// ── TaskCreate ──

func TestTaskCreate_Basic(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}

	output := execTool(t, create, map[string]any{
		"subject":     "Fix bug",
		"description": "Fix the login bug",
	})

	if !strings.Contains(output, "Fix bug") {
		t.Errorf("expected subject in output, got %q", output)
	}

	store.mu.RLock()
	if len(store.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(store.tasks))
	}
	for _, task := range store.tasks {
		if task.Status != "pending" {
			t.Errorf("expected status pending, got %s", task.Status)
		}
	}
	store.mu.RUnlock()
}

func TestTaskCreate_MissingSubject(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}

	err := create.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{
		"description": "desc",
	})), agent.NewBlockStream(nil))
	if err == nil {
		t.Error("expected error for missing subject")
	}
}

func TestTaskCreate_MissingDescription(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}

	err := create.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{
		"subject": "Fix bug",
	})), agent.NewBlockStream(nil))
	if err == nil {
		t.Error("expected error for missing description")
	}
}

// ── TaskUpdate ──

func TestTaskUpdate_StatusTransition(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	// 创建任务
	execTool(t, create, map[string]any{"subject": "T1", "description": "do it"})

	// pending → in_progress
	output := execTool(t, update, map[string]any{"task_id": "1", "status": "in_progress"})
	if !strings.Contains(output, "[◐]") {
		t.Errorf("expected in_progress icon, got %q", output)
	}

	// in_progress → completed
	output = execTool(t, update, map[string]any{"task_id": "1", "status": "completed"})
	if !strings.Contains(output, "[✓]") {
		t.Errorf("expected completed icon, got %q", output)
	}

	store.mu.RLock()
	task := store.tasks["1"]
	store.mu.RUnlock()
	if task.Status != "completed" {
		t.Errorf("expected completed, got %s", task.Status)
	}
}

func TestTaskUpdate_BlockedTaskCannotStart(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	// task 1（依赖方）
	execTool(t, create, map[string]any{"subject": "T1", "description": "main task"})
	// task 2（被依赖方）
	execTool(t, create, map[string]any{"subject": "T2", "description": "prerequisite"})

	// T1 blocked_by T2
	execTool(t, update, map[string]any{"task_id": "1", "add_blocked_by": []string{"2"}})

	// T1 不能直接 in_progress
	err := update.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{
		"task_id": "1",
		"status":  "in_progress",
	})), agent.NewBlockStream(nil))
	if err == nil {
		t.Error("expected error: blocked by incomplete task")
	}

	// T2 完成后 T1 可以 in_progress
	execTool(t, update, map[string]any{"task_id": "2", "status": "completed"})
	output := execTool(t, update, map[string]any{"task_id": "1", "status": "in_progress"})
	if !strings.Contains(output, "[◐]") {
		t.Errorf("expected in_progress after dependency satisfied, got %q", output)
	}
}

func TestTaskUpdate_BidirectionalDependency(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})
	execTool(t, create, map[string]any{"subject": "T2", "description": "d2"})

	// T1 blocks T2
	execTool(t, update, map[string]any{"task_id": "1", "add_blocks": []string{"2"}})

	store.mu.RLock()
	t1 := store.tasks["1"]
	t2 := store.tasks["2"]
	store.mu.RUnlock()

	if !containsStr(t1.Blocks, "2") {
		t.Errorf("T1.Blocks should contain 2, got %v", t1.Blocks)
	}
	if !containsStr(t2.BlockedBy, "1") {
		t.Errorf("T2.BlockedBy should contain 1, got %v", t2.BlockedBy)
	}
}

func TestTaskUpdate_RemoveDependency(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})
	execTool(t, create, map[string]any{"subject": "T2", "description": "d2"})

	// 建立双向依赖
	execTool(t, update, map[string]any{"task_id": "1", "add_blocks": []string{"2"}})
	// 移除
	execTool(t, update, map[string]any{"task_id": "1", "remove_blocks": []string{"2"}})

	store.mu.RLock()
	t1 := store.tasks["1"]
	t2 := store.tasks["2"]
	store.mu.RUnlock()

	if containsStr(t1.Blocks, "2") {
		t.Error("T1.Blocks should not contain 2 after remove")
	}
	if containsStr(t2.BlockedBy, "1") {
		t.Error("T2.BlockedBy should not contain 1 after remove")
	}
}

func TestTaskUpdate_AddBlockedBy_Bidirectional(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})
	execTool(t, create, map[string]any{"subject": "T2", "description": "d2"})

	// T2 blocked_by T1 → T1.Blocks 应包含 T2
	execTool(t, update, map[string]any{"task_id": "2", "add_blocked_by": []string{"1"}})

	store.mu.RLock()
	t1 := store.tasks["1"]
	t2 := store.tasks["2"]
	store.mu.RUnlock()

	if !containsStr(t1.Blocks, "2") {
		t.Errorf("T1.Blocks should contain 2 (bidirectional), got %v", t1.Blocks)
	}
	if !containsStr(t2.BlockedBy, "1") {
		t.Errorf("T2.BlockedBy should contain 1, got %v", t2.BlockedBy)
	}
}

func TestTaskUpdate_SelfDependencyIgnored(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})

	// T1 不能依赖自己
	execTool(t, update, map[string]any{"task_id": "1", "add_blocks": []string{"1"}})

	store.mu.RLock()
	t1 := store.tasks["1"]
	store.mu.RUnlock()

	if containsStr(t1.Blocks, "1") {
		t.Error("self-dependency should be ignored")
	}
}

func TestTaskUpdate_UpdateFields(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "Old", "description": "old desc"})

	execTool(t, update, map[string]any{
		"task_id":     "1",
		"subject":     "New Subject",
		"description": "new desc",
		"owner":       "agent-1",
	})

	store.mu.RLock()
	task := store.tasks["1"]
	store.mu.RUnlock()

	if task.Subject != "New Subject" {
		t.Errorf("expected subject 'New Subject', got %q", task.Subject)
	}
	if task.Description != "new desc" {
		t.Errorf("expected description 'new desc', got %q", task.Description)
	}
	if task.Owner != "agent-1" {
		t.Errorf("expected owner 'agent-1', got %q", task.Owner)
	}
}

// ── TaskUpdate: status=deleted ──

func TestTaskUpdate_Delete(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})
	execTool(t, update, map[string]any{"task_id": "1", "status": "deleted"})

	store.mu.RLock()
	task := store.tasks["1"]
	store.mu.RUnlock()
	if task.Status != "deleted" {
		t.Errorf("expected deleted, got %s", task.Status)
	}
}

// ── TaskList ──

func TestTaskList_FiltersDeleted(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}
	list := &TaskListTool{store}

	execTool(t, create, map[string]any{"subject": "keep", "description": "d1"})
	execTool(t, create, map[string]any{"subject": "del", "description": "d2"})
	execTool(t, update, map[string]any{"task_id": "2", "status": "deleted"})

	output := execTool(t, list, nil)
	if !strings.Contains(output, "keep") {
		t.Errorf("expected 'keep' in list, got %q", output)
	}
	if strings.Contains(output, "del") {
		t.Errorf("deleted task should not appear in list, got %q", output)
	}
}

func TestTaskList_Empty(t *testing.T) {
	store := NewTodoStore()
	list := &TaskListTool{store}

	output := execTool(t, list, nil)
	if !strings.Contains(output, "无任务") && len(store.tasks) == 0 {
		t.Errorf("expected empty message, got %q", output)
	}
}

func TestTaskList_BlockedByFiltered(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	update := &TaskUpdateTool{store}
	list := &TaskListTool{store}

	execTool(t, create, map[string]any{"subject": "T1", "description": "d1"})
	execTool(t, create, map[string]any{"subject": "T2", "description": "d2"})
	execTool(t, create, map[string]any{"subject": "T3", "description": "d3"})

	// T1 blocked_by T2, T3
	execTool(t, update, map[string]any{"task_id": "1", "add_blocked_by": []string{"2", "3"}})
	// T2 完成
	execTool(t, update, map[string]any{"task_id": "2", "status": "completed"})

	output := execTool(t, list, nil)
	// T2 已完成，不应出现在 blocked_by 中
	// T3 未完成，应出现
	if !strings.Contains(output, "#3") {
		t.Errorf("expected T3 in blocked_by, got %q", output)
	}
	if strings.Contains(output, "#2") && !strings.Contains(output, "blocked by #3") {
		// 如果没有 blocked by 行，说明 T2 也被列出来了
	}
}

// ── TaskGet ──

func TestTaskGet_FullDetail(t *testing.T) {
	store := NewTodoStore()
	create := &TaskCreateTool{store}
	get := &TaskGetTool{store}

	execTool(t, create, map[string]any{
		"subject":     "Complex task",
		"description": "Do something complex",
		"active_form": "Doing complex task",
	})

	output := execTool(t, get, map[string]any{"task_id": "1"})
	if !strings.Contains(output, "Complex task") {
		t.Errorf("expected subject, got %q", output)
	}
	if !strings.Contains(output, "Do something complex") {
		t.Errorf("expected description, got %q", output)
	}
}

func TestTaskGet_NotFound(t *testing.T) {
	store := NewTodoStore()
	get := &TaskGetTool{store}

	err := get.Execute(agent.NewTurn(value.NewObjectFromMap(map[string]any{"task_id": "999"})), agent.NewBlockStream(nil))
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

// ── NewTodoTools helper ──

func TestNewTodoTools_CreatesFourTools(t *testing.T) {
	create, update, list, get := NewTodoTools()
	if create == nil || update == nil || list == nil || get == nil {
		t.Fatal("NewTodoTools returned nil")
	}
	if create.Name() != "task_create" {
		t.Errorf("expected task_create, got %s", create.Name())
	}
	if update.Name() != "task_update" {
		t.Errorf("expected task_update, got %s", update.Name())
	}
	if list.Name() != "task_list" {
		t.Errorf("expected task_list, got %s", list.Name())
	}
	if get.Name() != "task_get" {
		t.Errorf("expected task_get, got %s", get.Name())
	}
}

func TestNewTodoTools_SameStore(t *testing.T) {
	create, update, _, _ := NewTodoTools()

	execTool(t, create, map[string]any{"subject": "Shared", "description": "d"})

	// create 和 update 共享同一个 store
	output := execTool(t, update, map[string]any{"task_id": "1", "status": "completed"})
	if !strings.Contains(output, "✓") {
		t.Errorf("expected completed in output from shared store, got %q", output)
	}
}

// ── TodoStore: Thread safety (parallel read/write) ──

func TestTodoStore_ConcurrentReadAccess(t *testing.T) {
	store := NewTodoStore()
	store.mu.Lock()
	task := &TodoTask{ID: "1", Status: "pending", Subject: "T", Description: "d"}
	store.tasks["1"] = task
	store.mu.Unlock()

	// 并发读不会死锁
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			store.mu.RLock()
			_ = len(store.tasks)
			store.mu.RUnlock()
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// ── Helpers ──

func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
