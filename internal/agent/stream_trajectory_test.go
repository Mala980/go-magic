package agent

import (
	"context"
	"testing"

	"github.com/magicwubiao/go-magic/internal/cortex"
)

// TestStreamRecordsTrajectory 锁死 P2 缺口：流式入口必须写轨迹。
//
// 缺口原貌：只有非流式 RunWithCortex 调 recordTrajectory，而 web 聊天与 bot
// 全部走 RunConversationStreamWithMedia。于是 GEPA 的唯一学习输入（轨迹）在
// 真实流量下恒为空 —— 世代照涨、轨迹恒少（线上实测 133 个世代 vs 6 条轨迹），
// "自进化"退化成空转：引擎一直在"进化"，却没有可学的东西。
//
// 断言：一次流式回合结束后，TrajectoryStore 里必须出现一条轨迹，且轨迹里
// 记录了本回合真实发生的工具调用（否则轨迹只是个空壳，同样学不到东西）。
func TestStreamRecordsTrajectory(t *testing.T) {
	prov := &scriptedLoopProvider{}
	dir := t.TempDir()

	mgr := cortex.NewManagerWithProfileAndConfig(dir, prov, "", &cortex.ManagerConfig{Enabled: true})
	if mgr == nil || !mgr.IsEnabled() {
		t.Fatal("precondition failed: expected an enabled cortex manager")
	}
	// TrajectoryStore 在 Start() 里才创建（与 server 同序：先 Start 再挂 agent）。
	if err := mgr.Start(); err != nil {
		t.Fatalf("cortex manager Start: %v", err)
	}
	// 收尾沉淀是异步写盘，等它落定再让 TempDir 清理；Stop 收掉后台循环。
	t.Cleanup(func() { mgr.WaitPendingWrites(); mgr.Stop() })

	store := mgr.GetTrajectoryStore()
	if store == nil {
		t.Fatal("precondition failed: manager has no trajectory store")
	}
	if got := len(store.GetTrajectories(10)); got != 0 {
		t.Fatalf("precondition failed: expected a clean store, found %d trajectories", got)
	}

	ag := newLoopTestAgent(t, prov, WithCortex(mgr))

	// 走流式入口（web 聊天 / bot 的实际路径）。
	_ = ag.RunConversationStream(context.Background(), "keep going", func(string, bool) {})

	trajs := store.GetTrajectories(10)
	if len(trajs) == 0 {
		t.Fatal("streaming turn recorded no trajectory — GEPA has nothing to learn from on the real request path")
	}

	var withSteps int
	for _, tr := range trajs {
		if tr.Task == "keep going" {
			if len(tr.Steps) > 0 {
				withSteps++
			}
		}
	}
	if withSteps == 0 {
		t.Fatalf("trajectory recorded but carries no tool steps (every entry is an empty shell): %d trajectories", len(trajs))
	}
}

// TestStreamTrajectoryRecordsRealToolCalls 验证步骤内容是**真实工具调用**，
// 而不是占位符：工具名与输入都必须落到轨迹里，GEPA 才能学到"哪类调用更有效"。
func TestStreamTrajectoryRecordsRealToolCalls(t *testing.T) {
	prov := &scriptedLoopProvider{}
	dir := t.TempDir()
	mgr := cortex.NewManagerWithProfileAndConfig(dir, prov, "", &cortex.ManagerConfig{Enabled: true})
	if err := mgr.Start(); err != nil {
		t.Fatalf("cortex manager Start: %v", err)
	}
	t.Cleanup(func() { mgr.WaitPendingWrites(); mgr.Stop() })

	ag := newLoopTestAgent(t, prov, WithCortex(mgr))
	_ = ag.RunConversationStream(context.Background(), "strategic task", func(string, bool) {})

	trajs := mgr.GetTrajectoryStore().GetTrajectories(10)
	if len(trajs) == 0 {
		t.Fatal("no trajectory recorded")
	}

	found := false
	for _, tr := range trajs {
		for _, step := range tr.Steps {
			if step.ToolName == testLoopToolName {
				found = true
				if step.ToolInput == "" {
					t.Error("step recorded without tool input — GEPA cannot learn which call shape works")
				}
			}
		}
	}
	if !found {
		t.Fatalf("no step for the tool that actually ran (%s); recorded steps do not reflect the turn", testLoopToolName)
	}
}
