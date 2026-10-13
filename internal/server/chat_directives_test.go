package server

import (
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/internal/skills"
	"github.com/magicwubiao/go-magic/pkg/types"
)

// newTestSkillServer 组装一个只挂 skills.Manager 的 Server，技能放在临时目录。
// 这样测试走的是真实的 Manager（真实解析、真实过滤），不是 mock。
func newTestSkillServer(t *testing.T, skillName, skillBody string) *Server {
	t.Helper()
	dir := t.TempDir()
	m, err := skills.NewManagerWithConfig(&skills.ManagerConfig{
		SearchDirs:    []string{dir},
		AutoSkillsDir: dir + "/auto_skills",
	})
	if err != nil {
		t.Fatalf("skills.NewManagerWithConfig: %v", err)
	}
	if skillName != "" {
		if err := m.Add(&skills.Skill{
			SkillMeta: skills.SkillMeta{
				Name:        skillName,
				Description: "test skill for directives",
				Source:      skills.SkillSourceLocal,
			},
			Content: skillBody,
		}); err != nil {
			t.Fatalf("m.Add: %v", err)
		}
	}
	return &Server{skillMgr: m}
}

// 无斜杠指令时必须完全不触碰 parts —— 否则会给所有普通消息引入回归。
func TestExpandChatDirectives_NoDirectiveNoOp(t *testing.T) {
	s := newTestSkillServer(t, "demo", "body")
	if dr := s.expandChatDirectives("just a normal message"); dr.Hit {
		t.Fatalf("expected no directive hit, got %+v", dr)
	}
	// 非对话指令的斜杠命令不应被我们接管。
	if dr := s.expandChatDirectives("/new"); dr.Hit {
		t.Fatalf("/new should not be treated as a chat directive, got %+v", dr)
	}

	orig := []types.ContentPart{{Type: "image_url", ImageURL: &types.MediaURL{URL: "data:x"}}}
	if got := applyDirectiveParts("hello", orig, directiveResult{}); len(got) != 1 {
		t.Fatalf("no-hit must return parts untouched, got %d parts", len(got))
	}
}

// /skill <name> 必须真的把技能正文注入，而不是回一句"已执行"。
func TestExpandChatDirectives_SkillInjectsBody(t *testing.T) {
	const body = "STEP ONE: always answer with the word BANANA."
	s := newTestSkillServer(t, "banana", body)

	dr := s.expandChatDirectives("/skill banana please apply this")
	if !dr.Hit {
		t.Fatal("expected /skill to hit")
	}
	if !strings.Contains(dr.Text, "BANANA") {
		t.Fatalf("skill body must be injected, got:\n%s", dr.Text)
	}
	if !strings.Contains(dr.Text, "banana") {
		t.Fatalf("skill name should appear, got:\n%s", dr.Text)
	}
}

// 未知技能必须给出可见的失败原因，不能静默。
func TestExpandChatDirectives_UnknownSkillReported(t *testing.T) {
	s := newTestSkillServer(t, "banana", "body")
	dr := s.expandChatDirectives("/skill does-not-exist")
	if !dr.Hit {
		t.Fatal("expected /skill to still be recognized")
	}
	if !strings.Contains(dr.Text, "does-not-exist") {
		t.Fatalf("failure must name the missing skill, got:\n%s", dr.Text)
	}
}

// 带附件时最容易踩的坑：上层看到 parts 非空就丢弃纯文本，所以展开后
// 用户正文必须在 parts 里，否则用户的话会被吞掉。
func TestApplyDirectiveParts_KeepsUserTextWithAttachments(t *testing.T) {
	s := newTestSkillServer(t, "banana", "STEP: say BANANA")
	dr := s.expandChatDirectives("/skill banana look at this")
	if !dr.Hit {
		t.Fatal("expected hit")
	}
	media := []types.ContentPart{{Type: "image_url", ImageURL: &types.MediaURL{URL: "data:image/png;base64,AAA"}}}

	got := applyDirectiveParts("/skill banana look at this", media, dr)

	if len(got) != 3 {
		t.Fatalf("expected directive+usertext+attachment = 3 parts, got %d", len(got))
	}
	if got[0].Type != "text" || !strings.Contains(got[0].Text, "BANANA") {
		t.Fatalf("part 0 must be the expanded directive, got %+v", got[0])
	}
	if got[1].Type != "text" || got[1].Text != "/skill banana look at this" {
		t.Fatalf("part 1 must preserve the user's text, got %+v", got[1])
	}
	if got[2].ImageURL == nil {
		t.Fatalf("attachment must be preserved last, got %+v", got[2])
	}
}

// 多条指令应全部展开。
func TestExpandChatDirectives_MultipleDirectives(t *testing.T) {
	s := newTestSkillServer(t, "banana", "BODY-ONE")
	dr := s.expandChatDirectives("/skill banana\n/skill banana")
	if !dr.Hit {
		t.Fatal("expected hit")
	}
	if n := strings.Count(dr.Text, "BODY-ONE"); n != 2 {
		t.Fatalf("expected both directives expanded, found %d occurrences:\n%s", n, dr.Text)
	}
}

// /skill 显式命中必须回灌给触发优化器。
//
// 回归点：生产侧此前唯一的触发上报是 RecordSkillInvocation 内部的
// RecordTriggerHit(name, "invocation", ...)，而 OptimizeTriggers 只认
// triggerType == "keyword" ⇒ keywordStats 恒空 ⇒ 优化器永远学不到东西、
// applyOptimizedTriggers 恒为 no-op。显式 /skill 是最可靠的真实信号。
func TestExpandChatDirectives_RecordsTriggerSignals(t *testing.T) {
	s := newTestSkillServer(t, "banana", "STEP: say BANANA")

	s.expandChatDirectives("/skill banana please apply this")

	stats := s.skillMgr.GetTriggerStats()
	st, ok := stats["banana"]
	if !ok {
		t.Fatalf("no trigger stats recorded for the explicitly invoked skill: %v", stats)
	}
	if st.TriggerHits["keyword:banana"] == 0 {
		t.Fatalf("explicit /skill hit must be recorded as a keyword hit, got %v", st.TriggerHits)
	}
	if len(st.MissedTriggers) == 0 {
		t.Fatal("the casual sentence next to /skill must be recorded as a missed trigger " +
			"(it is the only source of new keywords)")
	}
}

// 显式命中但没有任何"随手那句话"时，不应凭空造出漏触发样本。
func TestExpandChatDirectives_NoResidualNoMissedTrigger(t *testing.T) {
	s := newTestSkillServer(t, "banana", "body")
	s.expandChatDirectives("/skill banana")

	stats := s.skillMgr.GetTriggerStats()
	st, ok := stats["banana"]
	if !ok {
		t.Fatalf("hit not recorded: %v", stats)
	}
	if len(st.MissedTriggers) != 0 {
		t.Fatalf("bare /skill must not fabricate a missed trigger, got %v", st.MissedTriggers)
	}
}
