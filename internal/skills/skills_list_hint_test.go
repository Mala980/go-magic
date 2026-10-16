package skills

import (
	"sort"
	"strings"
	"testing"
)

// TestGetSkillsListHintIsModelActionable 锁定注入系统提示的技能清单提示语。
//
// 背景：清单此前写的是 "use /skill <name> for details"，但 /skill 只在
// **用户消息**里被 expandChatDirectives 解析（sessions.go 只扫用户输入），
// 模型发不出用户消息——照提示做等于死路，内置技能因此几乎从不被调用。
// 提示必须指向模型可执行的 skill 工具。
func TestGetSkillsListHintIsModelActionable(t *testing.T) {
	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs: []string{t.TempDir()}, // 空目录：不加载任何磁盘技能
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}

	if err := m.Add(&Skill{
		SkillMeta: SkillMeta{
			Name:        "demo",
			Description: "demo skill",
			Source:      SkillSourceBuiltin,
		},
		Content: "# demo",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	out := m.GetSkillsList()

	if strings.Contains(out, "/skill <name>") {
		t.Errorf("hint must not tell the model to use the /skill directive (model cannot emit user messages):\n%s", out)
	}
	for _, want := range []string{"skill tool", "action=info", "demo: demo skill"} {
		if !strings.Contains(out, want) {
			t.Errorf("hint missing %q in:\n%s", want, out)
		}
	}

	// GetSkillsContextForPrompt 必须与 GetSkillsList 同源，不允许再漂移。
	// （技能表是 map，遍历顺序随机，且 AutoSkillCreation 可能并发追加技能，
	//   故按排序后的行集合比较。）
	a := strings.Split(m.GetSkillsContextForPrompt(), "\n")
	b := strings.Split(out, "\n")
	sort.Strings(a)
	sort.Strings(b)
	if strings.Join(a, "|") != strings.Join(b, "|") {
		t.Errorf("GetSkillsContextForPrompt diverged from GetSkillsList:\nA=%v\nB=%v", a, b)
	}
}
