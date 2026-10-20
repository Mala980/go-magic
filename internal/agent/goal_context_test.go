package agent

import (
	"strings"
	"testing"
)

// TestSetGoalContextReplaces 验证目标块是「替换」而非追加：
// 先聊天后关联目标、切换目标、完成/解绑，都必须对已经在跑的会话生效。
// 回归：目标块此前只在 getOrCreateAgent 新建 agent 时拼进 systemPrompt，
// 而 agent 按会话缓存，导致关联了目标的会话「不知道自己有目标」。
func TestSetGoalContextReplaces(t *testing.T) {
	a := NewAIAgent(nil, nil, nil, "base system prompt")

	// 会话先跑起来，没有任何目标
	sys := systemContent(t, a)
	if strings.Contains(sys, "CURRENT USER GOAL") {
		t.Fatalf("goal block present before any injection: %q", sys)
	}

	// 用户中途关联目标 → 必须立即生效
	a.SetGoalContext("CURRENT USER GOAL: GOAL_A")
	sys = systemContent(t, a)
	if !strings.Contains(sys, "GOAL_A") || !strings.Contains(sys, "base system prompt") {
		t.Fatalf("first injection missing: %q", sys)
	}

	// 切换到另一个目标 → 旧目标不能残留
	a.SetGoalContext("CURRENT USER GOAL: GOAL_B")
	sys = systemContent(t, a)
	if strings.Contains(sys, "GOAL_A") {
		t.Fatalf("old goal block not removed: %q", sys)
	}
	if !strings.Contains(sys, "GOAL_B") || !strings.Contains(sys, "base system prompt") {
		t.Fatalf("new goal block missing or base prompt lost: %q", sys)
	}

	// 重复刷新同一目标 → 不能堆叠
	a.SetGoalContext("CURRENT USER GOAL: GOAL_B")
	sys = systemContent(t, a)
	if n := strings.Count(sys, "CURRENT USER GOAL"); n != 1 {
		t.Fatalf("goal block duplicated (%d copies): %q", n, sys)
	}

	// 解绑/完成 → 清除目标块，保留基础提示
	a.SetGoalContext("")
	sys = systemContent(t, a)
	if strings.Contains(sys, "GOAL_B") {
		t.Fatalf("goal block not cleared: %q", sys)
	}
	if !strings.Contains(sys, "base system prompt") {
		t.Fatalf("base system prompt lost after clearing goal: %q", sys)
	}
}

// TestGoalAndSkillsContextIndependent 验证两个块各自刷新时互不吞并：
// 技能块和目标块的增删顺序任意组合，结果都必须是「两块都在、各一份」。
func TestGoalAndSkillsContextIndependent(t *testing.T) {
	// 以固定顺序刷新，模拟 getOrCreateAgent 的调用顺序（先技能、后目标）
	order := func(a *Agent, skills, goal string) {
		a.SetSkillsContext(skills)
		a.SetGoalContext(goal)
	}

	a := NewAIAgent(nil, nil, nil, "base")
	order(a, "SKILLS_V1", "GOAL_A")
	sys := systemContent(t, a)
	if !strings.Contains(sys, "SKILLS_V1") || !strings.Contains(sys, "GOAL_A") {
		t.Fatalf("both blocks expected: %q", sys)
	}

	// 只换目标：技能块必须原样保留
	a.SetGoalContext("GOAL_B")
	sys = systemContent(t, a)
	if !strings.Contains(sys, "SKILLS_V1") {
		t.Fatalf("skills block lost when refreshing goal: %q", sys)
	}
	if strings.Contains(sys, "GOAL_A") || !strings.Contains(sys, "GOAL_B") {
		t.Fatalf("goal not replaced cleanly: %q", sys)
	}

	// 只换技能：目标块必须原样保留
	a.SetSkillsContext("SKILLS_V2")
	sys = systemContent(t, a)
	if !strings.Contains(sys, "GOAL_B") {
		t.Fatalf("goal block lost when refreshing skills: %q", sys)
	}
	if strings.Contains(sys, "SKILLS_V1") || !strings.Contains(sys, "SKILLS_V2") {
		t.Fatalf("skills not replaced cleanly: %q", sys)
	}

	// 清目标、留技能
	a.SetGoalContext("")
	sys = systemContent(t, a)
	if strings.Contains(sys, "GOAL_B") || !strings.Contains(sys, "SKILLS_V2") {
		t.Fatalf("expected goal cleared and skills kept: %q", sys)
	}

	// 反复刷新不应累积空行/残留
	order(a, "SKILLS_V3", "GOAL_C")
	sys = systemContent(t, a)
	if n := strings.Count(sys, "SKILLS_V3"); n != 1 {
		t.Fatalf("skills block duplicated (%d): %q", n, sys)
	}
	if n := strings.Count(sys, "GOAL_C"); n != 1 {
		t.Fatalf("goal block duplicated (%d): %q", n, sys)
	}
	if n := strings.Count(sys, "base"); n != 1 {
		t.Fatalf("base prompt duplicated (%d): %q", n, sys)
	}
}

// TestSetGoalContextEmptySystemPrompt 系统提示只由目标块构成时，
// 清除后不应残留分隔符或空白。
func TestSetGoalContextEmptySystemPrompt(t *testing.T) {
	a := NewAIAgent(nil, nil, nil, "")
	a.SetGoalContext("ONLY_GOAL")
	if got := systemContent(t, a); got != "ONLY_GOAL" {
		t.Fatalf("expected goal-only system prompt, got %q", got)
	}
	a.SetGoalContext("")
	if got := systemContent(t, a); strings.TrimSpace(got) != "" {
		t.Fatalf("expected empty system prompt, got %q", got)
	}
}
