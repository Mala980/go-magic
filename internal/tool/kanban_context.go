package tool

import (
	"context"
	"strings"
)

type kanbanTaskKey struct{}

// WithKanbanTask 把「当前正在执行的看板任务 ID」绑定到 ctx。
//
// 看板 worker 由 dispatcher 在**同一个进程内** goroutine 里 spawn，多个任务
// 可能并发跑，进程级环境变量 KANBAN_TASK 无法表达"哪个任务属于哪个 worker"
// （后一个会覆盖前一个）。因此任务归属只能走请求级 ctx。
func WithKanbanTask(ctx context.Context, taskID string) context.Context {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, kanbanTaskKey{}, taskID)
}

// KanbanTaskFromContext 返回 ctx 中绑定的看板任务 ID，未绑定时返回空串。
func KanbanTaskFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(kanbanTaskKey{}).(string); ok {
		return v
	}
	return ""
}
