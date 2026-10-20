package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAutoSkill 在 <autoDir>/<state>/<id>/ 下写一份完整的自动技能
// （SKILL.md 带 frontmatter + meta.json），与 generateSkillMarkdown 的产物同构。
func writeAutoSkill(t *testing.T, autoDir, state, id, name, description string, tools []string) string {
	t.Helper()
	dir := filepath.Join(autoDir, state, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + name + "\n")
	sb.WriteString("description: " + description + "\n")
	sb.WriteString("version: 1.0.0\n")
	sb.WriteString("author: cortex-auto\n")
	sb.WriteString("tags: [auto-generated]\n")
	if len(tools) > 0 {
		sb.WriteString("tools: [" + strings.Join(tools, ", ") + "]\n")
	}
	sb.WriteString("---\n\n")
	sb.WriteString("# " + name + "\n\nbody of " + name + "\n")

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"` + id + `","name":"` + name + `","description":"` + description + `",` +
		`"author":"cortex-auto","created_at":"2026-09-01T00:00:00Z","frequency":3}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newAutoSkillTestManager(t *testing.T, skillsRoot, autoDir string) *Manager {
	t.Helper()
	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs:    []string{skillsRoot},
		AutoSkillsDir: autoDir,
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

// TestAutoSkillStateStatusCoversDirs 保证"写目录"与"加载时认目录"用同一套状态名。
func TestAutoSkillStateStatusCoversDirs(t *testing.T) {
	if len(autoSkillStateStatus) != len(autoSkillStateDirs) {
		t.Fatalf("state map has %d entries, dirs has %d", len(autoSkillStateStatus), len(autoSkillStateDirs))
	}
	for _, d := range autoSkillStateDirs {
		status, ok := autoSkillStateStatus[d]
		if !ok || string(status) != d {
			t.Fatalf("state dir %q maps to %q (ok=%v), want %q", d, status, ok, d)
		}
	}
}

// TestNewManagerLoadsApprovedAutoSkills 覆盖主诉：不走 server 的进程（NewManager）
// 也必须能在构造期看到已批准的自动技能，且元数据来自 SKILL.md 的 frontmatter。
func TestNewManagerLoadsApprovedAutoSkills(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	autoDir := filepath.Join(skillsRoot, "auto_skills")
	writeAutoSkill(t, autoDir, "approved", "auto-Demo-1", "Automated Demo", "demo pattern",
		[]string{"read_file", "write_file"})

	m := newAutoSkillTestManager(t, skillsRoot, autoDir)

	sk, err := m.Get("Automated Demo")
	if err != nil {
		t.Fatalf("approved auto skill not registered at construction: %v", err)
	}
	if sk.Source != SkillSourceAuto || sk.Status != SkillStatusApproved {
		t.Fatalf("unexpected source/status: %s/%s", sk.Source, sk.Status)
	}
	if sk.Description != "demo pattern" {
		t.Fatalf("description lost: %q", sk.Description)
	}
	if len(sk.Tools) != 2 || sk.Tools[0] != "read_file" {
		t.Fatalf("tools lost: %v", sk.Tools)
	}
	if !strings.Contains(m.GetSkillsList(), "Automated Demo") {
		t.Fatal("approved auto skill missing from agent skills list")
	}
}

// TestReloadKeepsApprovedAutoSkills 覆盖第二个断点：Reload 是"按磁盘重建整张表"，
// 曾经会把通过 RegisterSkill 塞进内存的自动技能整体抹掉（install/upload/rollback 后复现）。
func TestReloadKeepsApprovedAutoSkills(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	autoDir := filepath.Join(skillsRoot, "auto_skills")
	writeAutoSkill(t, autoDir, "approved", "auto-Demo-1", "Automated Demo", "demo pattern", nil)

	m := newAutoSkillTestManager(t, skillsRoot, autoDir)
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	sk, err := m.Get("Automated Demo")
	if err != nil {
		t.Fatalf("approved auto skill lost after Reload: %v", err)
	}
	if sk.Status != SkillStatusApproved {
		t.Fatalf("status after Reload = %q, want approved", sk.Status)
	}
	if !strings.Contains(m.GetSkillsList(), "Automated Demo") {
		t.Fatal("approved auto skill missing from agent skills list after Reload")
	}
}

// TestAutoSkillStatusFiltering 锁定注入口径：只有 approved 的自动技能进入提示词。
func TestAutoSkillStatusFiltering(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	autoDir := filepath.Join(skillsRoot, "auto_skills")
	writeAutoSkill(t, autoDir, "pending", "auto-Pending-1", "Automated Pending", "p", nil)
	writeAutoSkill(t, autoDir, "approved", "auto-Approved-1", "Automated Approved", "a", nil)
	writeAutoSkill(t, autoDir, "archived", "auto-Archived-1", "Automated Archived", "r", nil)
	writeAutoSkill(t, autoDir, "rejected", "auto-Rejected-1", "Automated Rejected", "j", nil)

	m := newAutoSkillTestManager(t, skillsRoot, autoDir)

	// 四种状态都必须能被状态接口看到，否则 UI 无法审批/恢复
	for _, status := range []SkillStatus{
		SkillStatusPending, SkillStatusApproved, SkillStatusArchived, SkillStatusRejected,
	} {
		if len(m.ListAutoSkillsByStatus(status)) != 1 {
			t.Fatalf("status %q not loaded from disk (got %d)", status, len(m.ListAutoSkillsByStatus(status)))
		}
	}

	list := m.GetSkillsList()
	if !strings.Contains(list, "Automated Approved") {
		t.Fatal("approved auto skill missing from skills list")
	}
	for _, name := range []string{"Automated Pending", "Automated Archived", "Automated Rejected"} {
		if strings.Contains(list, name) {
			t.Fatalf("%s must not be injected into agent context", name)
		}
	}
}

// TestManualSkillShadowsAutoSkill 自动沉淀不应覆盖同名的手写技能。
func TestManualSkillShadowsAutoSkill(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	autoDir := filepath.Join(skillsRoot, "auto_skills")

	manualDir := filepath.Join(skillsRoot, "my-demo")
	if err := os.MkdirAll(manualDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manual := "---\nname: Automated Demo\ndescription: hand written\n---\n\n# Automated Demo\n"
	if err := os.WriteFile(filepath.Join(manualDir, "SKILL.md"), []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAutoSkill(t, autoDir, "approved", "auto-Demo-1", "Automated Demo", "auto generated", nil)

	m := newAutoSkillTestManager(t, skillsRoot, autoDir)

	sk, err := m.Get("Automated Demo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sk.Source == SkillSourceAuto {
		t.Fatalf("auto skill must not shadow the manual skill (source=%s)", sk.Source)
	}
	if sk.Description != "hand written" {
		t.Fatalf("manual skill content overwritten: %q", sk.Description)
	}
}

// TestLegacyAutoSkillAtTopLevelIsPending 兼容历史数据：没有状态子目录的旧技能按 pending 处理。
func TestLegacyAutoSkillAtTopLevelIsPending(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	autoDir := filepath.Join(skillsRoot, "auto_skills")

	dir := filepath.Join(autoDir, "auto-Legacy-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Automated Legacy\n\nlegacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"),
		[]byte(`{"name":"Automated Legacy","description":"legacy pattern"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newAutoSkillTestManager(t, skillsRoot, autoDir)

	sk, err := m.Get("Automated Legacy")
	if err != nil {
		t.Fatalf("legacy auto skill not loaded: %v", err)
	}
	if sk.Status != SkillStatusPending {
		t.Fatalf("legacy skill status = %q, want pending", sk.Status)
	}
	if sk.Description != "legacy pattern" {
		t.Fatalf("meta.json description not used as fallback: %q", sk.Description)
	}
}

// TestSetAutoSkillsDirRegistersExistingSkills 运行期切换目录后不等到下一次 Reload 就可看到技能。
func TestSetAutoSkillsDirRegistersExistingSkills(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	initial := filepath.Join(skillsRoot, "auto_skills")

	m := newAutoSkillTestManager(t, skillsRoot, initial)

	other := filepath.Join(root, "other-auto")
	writeAutoSkill(t, other, "approved", "auto-Moved-1", "Automated Moved", "moved", nil)

	m.SetAutoSkillsDir(other)

	if _, err := m.Get("Automated Moved"); err != nil {
		t.Fatalf("skill in the new auto dir not registered: %v", err)
	}
	// 新目录的状态子目录也必须就绪（否则 RejectAutoSkill 的 Rename 会失败）
	for _, state := range autoSkillStateDirs {
		if info, err := os.Stat(filepath.Join(other, state)); err != nil || !info.IsDir() {
			t.Fatalf("state dir %q missing after SetAutoSkillsDir", state)
		}
	}
}
