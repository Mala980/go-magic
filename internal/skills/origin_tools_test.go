package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkill 在 dir 下创建一个名为 name 的技能目录，写入 SKILL.md 正文 content。
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// P1-1 回归：本地技能覆盖同名内置技能后，Source 必须是 local/global，
// 不能再保留 builtin 标签（否则 --source builtin 会把本地文件当成打包基线）。
func TestShadowedBuiltinSkillIsNotLabeledBuiltin(t *testing.T) {
	globalDir := t.TempDir()
	workDir := t.TempDir()

	// "code-review" 是内置技能名；在**工作区**目录放一个同名技能。
	writeSkill(t, workDir, "code-review", "---\nname: code-review\ndescription: local override\n---\n\nlocal body\n")

	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs: []string{globalDir, workDir},
		ToolNames:  []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	sk, err := m.Get("code-review")
	if err != nil {
		t.Fatalf("Get(code-review): %v", err)
	}
	if sk.Source == SkillSourceBuiltin {
		t.Fatalf("shadowed skill must not be labeled %q", sk.Source)
	}
	if sk.Source != SkillSourceLocal {
		t.Fatalf("source = %q, want %q", sk.Source, SkillSourceLocal)
	}
	if v, _ := sk.Metadata["shadows_builtin"].(bool); !v {
		t.Fatalf("expected shadows_builtin metadata marker, got %#v", sk.Metadata)
	}
	if sk.Description != "local override" {
		t.Fatalf("local content must win, description = %q", sk.Description)
	}
}

// 全局目录（SearchDirs[0]）里的技能应标注为 global，而不是 local。
func TestGlobalDirSkillIsLabeledGlobal(t *testing.T) {
	globalDir := t.TempDir()
	workDir := t.TempDir()
	writeSkill(t, globalDir, "only-global", "---\nname: only-global\ndescription: g\n---\n\nbody\n")

	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs: []string{globalDir, workDir},
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	sk, err := m.Get("only-global")
	if err != nil {
		t.Fatalf("Get(only-global): %v", err)
	}
	if sk.Source != SkillSourceGlobal {
		t.Fatalf("source = %q, want %q", sk.Source, SkillSourceGlobal)
	}
}

// P1-2 回归：声明的 tools 必须被校验，未注册的记入 MissingTools。
func TestValidateSkillToolsFlagsUnregistered(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "tool-declaring",
		"---\nname: tool-declaring\ndescription: x\ntools: [read_file, definitely_not_a_tool]\n---\n\nbody\n")

	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs: []string{dir},
		ToolNames:  []string{"read_file", "lint"},
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	sk, err := m.Get("tool-declaring")
	if err != nil {
		t.Fatalf("Get(tool-declaring): %v", err)
	}
	if len(sk.MissingTools) != 1 || sk.MissingTools[0] != "definitely_not_a_tool" {
		t.Fatalf("MissingTools = %v, want [definitely_not_a_tool]", sk.MissingTools)
	}
}

// 未注入 ToolNames 时不做校验，避免调用方未接线时误报。
func TestValidateSkillToolsSkippedWithoutToolNames(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "anything", "---\nname: anything\ndescription: x\ntools: [nope]\n---\n\nbody\n")

	m, err := NewManagerWithConfig(&ManagerConfig{SearchDirs: []string{dir}})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	sk, err := m.Get("anything")
	if err != nil {
		t.Fatalf("Get(anything): %v", err)
	}
	if len(sk.MissingTools) != 0 {
		t.Fatalf("MissingTools = %v, want empty when ToolNames is not injected", sk.MissingTools)
	}
}

// 回归：内置技能走 ParseYAMLFrontmatter，其 tags/tools 必须真的被解析出来
// （旧实现断言 []interface{}，而 parseSimpleYAML 返回 []string，两者不匹配 → 恒为空）。
func TestBuiltinSkillFrontmatterIsParsed(t *testing.T) {
	m, err := NewManagerWithConfig(&ManagerConfig{SearchDirs: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	defer m.Close()

	sk, err := m.Get("code-review")
	if err != nil {
		t.Fatalf("Get(code-review): %v", err)
	}
	if sk.Source != SkillSourceBuiltin {
		t.Fatalf("source = %q, want builtin", sk.Source)
	}
	if len(sk.Tags) == 0 {
		t.Fatal("builtin skill lost its tags: frontmatter was not parsed")
	}
	if len(sk.GetTools()) == 0 {
		t.Fatal("builtin skill lost its tools: frontmatter was not parsed")
	}
	if sk.Description == "" || sk.Description == "Built-in skill" {
		t.Fatalf("builtin skill description was not parsed, got %q", sk.Description)
	}
}
