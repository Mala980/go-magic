package server

import (
	"context"
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/internal/goal"
)

// newTestServerWithGoals 构造一个只带 goalMgr 的 Server（其余字段为 nil 即可，
// goalContextBlock 只依赖 goalMgr）。
func newTestServerWithGoals(t *testing.T) *Server {
	t.Helper()
	mgr, err := goal.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("new goal manager: %v", err)
	}
	return &Server{goalMgr: mgr}
}

// TestGoalContextBlockEmpty 没有关联目标时返回空串 —— 调用方据此清除旧块，
// 而不是注入一个空的目标说明。
func TestGoalContextBlockEmpty(t *testing.T) {
	s := newTestServerWithGoals(t)

	if got := s.goalContextBlock("sess_none"); got != "" {
		t.Fatalf("expected empty block for session without goals, got %q", got)
	}

	// 关联了目标但目标是 completed/abandoned → 同样不应该注入
	ctx := context.Background()
	g, err := s.goalMgr.Create(ctx, "已完成的目标", "desc")
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if err := s.goalMgr.LinkSession(ctx, g.ID, "sess_done"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := s.goalMgr.Update(ctx, g.ID, map[string]interface{}{"status": "completed"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := s.goalContextBlock("sess_done"); got != "" {
		t.Fatalf("expected empty block for completed goal, got %q", got)
	}

	// goalMgr 未初始化（启动失败时 server 的真实状态）不能 panic
	var nilSrv Server
	if got := nilSrv.goalContextBlock("sess_x"); got != "" {
		t.Fatalf("expected empty block without manager, got %q", got)
	}
}

// TestGoalContextBlockContent 目标块的字段必须完整渲染，进度用 % 转义（否则
// fmt.Sprintf 会因为裸 % 把后续内容吃掉）。
func TestGoalContextBlockContent(t *testing.T) {
	s := newTestServerWithGoals(t)
	ctx := context.Background()

	g, err := s.goalMgr.Create(ctx, "上线目标模块", "修好目标与会话的关联")
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}
	if err := s.goalMgr.LinkSession(ctx, g.ID, "sess_a"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := s.goalMgr.Update(ctx, g.ID, map[string]interface{}{"progress": float64(40)}); err != nil {
		t.Fatalf("update progress: %v", err)
	}

	block := s.goalContextBlock("sess_a")
	for _, want := range []string{"CURRENT USER GOAL", "上线目标模块", "修好目标与会话的关联", "40%", "active", "GOAL GUIDANCE"} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "%!") || strings.Contains(block, "MISSING") {
		t.Fatalf("Sprintf verb error in block:\n%s", block)
	}

	// 其他会话不应看到这个目标
	if got := s.goalContextBlock("sess_other"); got != "" {
		t.Fatalf("goal leaked to unrelated session: %q", got)
	}
}

// TestGoalContextBlockPicksMostRecentActive 一个会话关联多个活跃目标时，
// 取最近更新的那个（多目标同时进系统提示会让模型摇摆）。
func TestGoalContextBlockPicksMostRecentActive(t *testing.T) {
	s := newTestServerWithGoals(t)
	ctx := context.Background()

	oldGoal, err := s.goalMgr.Create(ctx, "旧目标", "old")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newGoal, err := s.goalMgr.Create(ctx, "新目标", "new")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, id := range []string{oldGoal.ID, newGoal.ID} {
		if err := s.goalMgr.LinkSession(ctx, id, "sess_multi"); err != nil {
			t.Fatalf("link: %v", err)
		}
	}
	// 让 oldGoal 成为"最近更新"的那个
	if _, err := s.goalMgr.Update(ctx, oldGoal.ID, map[string]interface{}{"progress": float64(10)}); err != nil {
		t.Fatalf("update: %v", err)
	}

	block := s.goalContextBlock("sess_multi")
	if !strings.Contains(block, "旧目标") {
		t.Fatalf("expected most recently updated goal, got:\n%s", block)
	}
	if strings.Contains(block, "新目标") {
		t.Fatalf("only one goal should be injected, got:\n%s", block)
	}
}
