package skills

import (
	"strings"
	"testing"
)

// 守卫测试：专家型内置技能必须随二进制嵌入并可加载。
//
// 这些技能是面板"从专家模板创建 Bot"的运行时依赖——BotExpertTemplate.skills
// 引用的名字必须能在 /api/skills 里看到，否则模板创建的 Bot 白名单里挂的是
// 一个永远加载不到的技能。新增专家模板时在这里同步登记名字。
func TestBuiltinExpertSkillsShipped(t *testing.T) {
	m, err := NewManagerWithConfig(&ManagerConfig{
		ToolNames: []string{"read_file", "web_search", "web_fetch", "search_in_files", "list_files"},
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	for _, name := range []string{
		"expert-invest-analysis",
		"expert-code-review",
		"expert-copywriting",
	} {
		sk, err := m.Get(name)
		if err != nil {
			t.Errorf("Get(%s): %v", name, err)
			continue
		}
		if sk.Source != SkillSourceBuiltin {
			t.Errorf("%s: source = %q, want %q", name, sk.Source, SkillSourceBuiltin)
		}
		if strings.TrimSpace(sk.Description) == "" {
			t.Errorf("%s: empty description (frontmatter not parsed?)", name)
		}
	}
}
