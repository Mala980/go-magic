package tool

import (
	"context"
	"strings"
	"testing"
)

// 任务 ID 解析优先级：显式参数 > ctx 绑定 > KANBAN_TASK 环境变量。
func TestResolveKanbanTaskID(t *testing.T) {
	t.Setenv("KANBAN_TASK", "env-task")

	ctx := context.Background()
	if got := resolveKanbanTaskID(ctx, map[string]interface{}{}); got != "env-task" {
		t.Fatalf("env fallback: want env-task, got %q", got)
	}

	bound := WithKanbanTask(ctx, "ctx-task")
	if got := resolveKanbanTaskID(bound, map[string]interface{}{}); got != "ctx-task" {
		t.Fatalf("ctx binding should win over env: want ctx-task, got %q", got)
	}
	if got := KanbanTaskFromContext(bound); got != "ctx-task" {
		t.Fatalf("KanbanTaskFromContext: want ctx-task, got %q", got)
	}

	withParam := map[string]interface{}{"task_id": "param-task"}
	if got := resolveKanbanTaskID(bound, withParam); got != "param-task" {
		t.Fatalf("explicit param should win: want param-task, got %q", got)
	}

	// 空白参数不应把 ctx 绑定顶掉（LLM 偶尔传空串）
	blank := map[string]interface{}{"task_id": "   "}
	if got := resolveKanbanTaskID(bound, blank); got != "ctx-task" {
		t.Fatalf("blank param should fall through: want ctx-task, got %q", got)
	}
}

// 没有绑定任务时 kanban_show 不应报错，而要返回可用的指引。
func TestKanbanShowWithoutTaskReturnsHint(t *testing.T) {
	t.Setenv("KANBAN_TASK", "")

	prev := KanbanManager
	KanbanManager = nil
	defer func() { KanbanManager = prev }()

	res, err := NewKanbanShowTool().Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("unbound kanban_show should not fail: %v", err)
	}

	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	if m["success"] != false {
		t.Fatalf("want success=false, got %v", m["success"])
	}
	msg, _ := m["message"].(string)
	if !strings.Contains(msg, "task_id") {
		t.Fatalf("hint should mention task_id, got %q", msg)
	}
}
