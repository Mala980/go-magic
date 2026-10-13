package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/magicwubiao/go-magic/pkg/config"
)

// ---------------------------------------------------------------------------
// P1-3：模式自动建技能的阈值与数据一致化
// ---------------------------------------------------------------------------

// 阈值必须只有一个默认来源，且 0/负数归一为默认值而不是 0。
// 回归点：此前构造器硬编码 5、配置默认 3；SetMinFrequency(0) 会把阈值改成 0，
// 使 `Frequency >= 0` 恒真 → 任一模式首见即建技能。
func TestMinFrequencyDefaultAndNormalization(t *testing.T) {
	if DefaultAutoSkillMinFrequency != config.DefaultAutoSkillMinFrequency {
		t.Fatalf("threshold default drifted away from pkg/config: %d != %d",
			DefaultAutoSkillMinFrequency, config.DefaultAutoSkillMinFrequency)
	}

	ac := NewEnhancedAutoCreator(t.TempDir())
	if got := ac.GetMinFrequency(); got != DefaultAutoSkillMinFrequency {
		t.Fatalf("constructor default = %d, want %d", got, DefaultAutoSkillMinFrequency)
	}

	for _, bad := range []int{0, -1, -100} {
		ac.SetMinFrequency(bad)
		if got := ac.GetMinFrequency(); got != DefaultAutoSkillMinFrequency {
			t.Fatalf("SetMinFrequency(%d) => %d, want normalized %d (0 must not disable the gate)",
				bad, got, DefaultAutoSkillMinFrequency)
		}
	}

	ac.SetMinFrequency(7)
	if got := ac.GetMinFrequency(); got != 7 {
		t.Fatalf("SetMinFrequency(7) => %d, want 7", got)
	}
}

// 未达阈值的模式不得生成技能；达到阈值的才生成。
// 注意：闸门在 checkAndGenerateSkillsFromSnapshot，GenerateSkillFromPattern
// 只是底层写盘动作。
func TestPatternThresholdGatesSkillGeneration(t *testing.T) {
	dir := t.TempDir()
	ac := NewEnhancedAutoCreator(dir)
	ac.SetMinFrequency(3)

	ac.mu.Lock()
	ac.patterns = []Pattern{
		{
			Name: "Pattern-below", Tools: []string{"read_file", "write_file", "terminal"},
			ToolSequence: []string{"read_file", "write_file", "terminal"},
			Frequency:    2, Confidence: 0.9,
		},
	}
	ac.mu.Unlock()
	ac.CheckAndGenerateSkills()

	if got := len(ac.GetGeneratedSkills()); got != 0 {
		t.Fatalf("below-threshold pattern produced %d skill(s), want 0", got)
	}

	ac.mu.Lock()
	ac.patterns = append(ac.patterns, Pattern{
		Name: "Pattern-at", Tools: []string{"search_in_files", "read_file", "write_file"},
		ToolSequence: []string{"search_in_files", "read_file", "write_file"},
		Frequency:    3, Confidence: 0.9,
	})
	ac.mu.Unlock()
	ac.CheckAndGenerateSkills()

	if got := len(ac.GetGeneratedSkills()); got != 1 {
		t.Fatalf("at-threshold pattern produced %d skill(s), want 1 (got %v)",
			got, ac.GetGeneratedSkills())
	}
}

// GetGeneratedSkills 必须扫描四态子目录（技能真实落盘位置），而不是 baseDir 顶层。
// 回归点：此前只扫顶层，顶层只有 pending/approved/archived/rejected 四个目录，
// 永远返回空 → cortex 的 "Generated N new skills" 从不打印。
func TestGetGeneratedSkillsScansStateDirs(t *testing.T) {
	dir := t.TempDir()
	ac := NewEnhancedAutoCreator(dir)

	if got := ac.GetGeneratedSkills(); len(got) != 0 {
		t.Fatalf("fresh creator should report 0 skills, got %v", got)
	}

	ac.GenerateSkillFromPattern(Pattern{
		Name: "Pattern-x", Tools: []string{"a", "b", "c"},
		ToolSequence: []string{"a", "b", "c"},
		Frequency:    5, Confidence: 0.9,
	})

	got := ac.GetGeneratedSkills()
	if len(got) != 1 {
		t.Fatalf("GetGeneratedSkills() = %v, want exactly 1 (written under pending/)", got)
	}

	// 必须真的位于 pending/ 下。
	if _, err := os.Stat(filepath.Join(dir, "auto_skills", "pending", got[0])); err != nil {
		t.Fatalf("reported skill %q is not under pending/: %v", got[0], err)
	}
}

// GetStats 的 skills_generated 必须与磁盘一致，而不是易失的进程内计数器。
// 回归点：计数器重启归零，导致面板出现 "patterns_detected=2, skills_generated=0"
// 这种与磁盘实际技能自相矛盾的读数。
func TestGetStatsSkillCountIsPersistent(t *testing.T) {
	dir := t.TempDir()
	ac := NewEnhancedAutoCreator(dir)
	ac.GenerateSkillFromPattern(Pattern{
		Name: "Pattern-y", Tools: []string{"a", "b", "c"},
		ToolSequence: []string{"a", "b", "c"},
		Frequency:    5, Confidence: 0.9,
	})

	// 模拟进程重启：新的 creator 指向同一目录，进程内计数器归零。
	restarted := NewEnhancedAutoCreator(dir)
	stats := restarted.GetStats()

	if got := stats["skills_generated"]; got != 1 {
		t.Fatalf("after restart skills_generated = %v, want 1 (persisted on disk)", got)
	}
	if got := stats["skills_generated_this_run"]; got != 0 {
		t.Fatalf("this-run counter should be 0 after restart, got %v", got)
	}
	if got := stats["min_frequency"]; got != DefaultAutoSkillMinFrequency {
		t.Fatalf("min_frequency = %v, want %d", got, DefaultAutoSkillMinFrequency)
	}
}

// ---------------------------------------------------------------------------
// P1-1：有效性统计 + 触发优化接线
// ---------------------------------------------------------------------------

// 技能被调用后必须留下有效性记录 —— 这是进化链路的唯一原始输入。
// 回归点：RecordInvocation 此前没有任何调用方，统计数据恒为空。
func TestRecordSkillInvocationFeedsEffectiveness(t *testing.T) {
	m := newTestManager(t)

	m.RecordSkillInvocation("demo-skill", "skill_invoke", "run the demo",
		75, true, 120*time.Millisecond, []string{"read_file"}, "sess-1")

	stats := m.GetAllStatistics()
	if len(stats) == 0 {
		t.Fatal("no statistics recorded after RecordSkillInvocation")
	}

	var found *SkillStatistics
	for _, s := range stats {
		if s.SkillName == "demo-skill" {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatalf("statistics for demo-skill missing: %+v", stats)
	}
	if found.TotalInvocations != 1 {
		t.Fatalf("TotalInvocations = %d, want 1", found.TotalInvocations)
	}
	if found.SuccessRate != 100 {
		t.Fatalf("SuccessRate = %v, want 100 (one success / one invocation)", found.SuccessRate)
	}
}

// 触发优化必须接到真实统计数据上（此前 TriggerOptimizer 从未被构造）。
func TestTriggerOptimizerIsWired(t *testing.T) {
	m := newTestManager(t)

	m.RecordSkillInvocation("trig-skill", "skill_invoke", "input", 60, true, time.Millisecond, nil, "")
	for i := 0; i < 3; i++ {
		m.RecordMissedSkillTrigger("trig-skill", "deploy the docker image")
	}

	stats := m.GetTriggerStats()
	if len(stats) == 0 {
		t.Fatal("trigger stats empty: TriggerOptimizer is not being fed")
	}
	if _, ok := stats["trig-skill"]; !ok {
		t.Fatalf("trig-skill missing from trigger stats: %+v", stats)
	}

	cond, err := m.OptimizeSkillTriggers("trig-skill")
	if err != nil {
		t.Fatalf("OptimizeSkillTriggers: %v", err)
	}
	if cond == nil {
		t.Fatal("OptimizeSkillTriggers returned nil condition")
	}
	if len(cond.Keywords) == 0 {
		t.Fatal("optimizer produced no keywords from repeated missed triggers")
	}

	// 优化结果必须能被读取（闭环的读侧）。
	if got := m.triggerOpt.GetOptimizedTriggers("trig-skill"); got == nil || len(got.Keywords) == 0 {
		t.Fatal("optimized triggers were written but not readable")
	}
}

// ---------------------------------------------------------------------------
// P1-2：版本快照 + 回滚
// ---------------------------------------------------------------------------

// 创建/更新/删除都必须落版本快照，且回滚必须**真的恢复 SKILL.md 内容**。
// 回归点有三个，此前都不可用：
//  1. SaveVersion 没有任何调用方 → 版本历史恒为空；
//  2. SaveVersion 内部二次加锁 → 一旦被调用即自死锁；
//  3. Rollback 只改版本标记、不回写内容 → "回滚成功"但内容一字未变。
func TestVersionSnapshotAndRollback(t *testing.T) {
	m := newTestManager(t)

	skill, err := m.Create("snap-skill", "snapshot demo", "# v1 body", []string{"test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v1Content := skill.Content // 按值拷贝：skill 是活指针，后续 Update 会改写它

	if len(m.GetVersions("snap-skill")) == 0 {
		t.Fatal("Create did not record a version snapshot")
	}

	const v2Content = "# v2 body (broken by evolution)"
	if err := m.Update("snap-skill", v2Content); err != nil {
		t.Fatalf("Update: %v", err)
	}

	versions := m.GetVersions("snap-skill")
	if len(versions) < 2 {
		t.Fatalf("expected >=2 versions after create+update, got %d", len(versions))
	}

	// 找到内容为 v1 的那一版并回滚到它。
	var target string
	for _, v := range versions {
		if v.Content == v1Content {
			target = v.Version
			break
		}
	}
	if target == "" {
		t.Fatalf("no version holds the original content %q; versions=%+v", v1Content, versions)
	}

	if err := m.RollbackSkillVersion("snap-skill", target); err != nil {
		t.Fatalf("RollbackSkillVersion(%q): %v", target, err)
	}

	// 回滚必须落到磁盘上的 SKILL.md，而不只是内存/版本元数据。
	onDisk, err := os.ReadFile(filepath.Join(skill.Dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md after rollback: %v", err)
	}
	if string(onDisk) != v1Content {
		t.Fatalf("SKILL.md not restored by rollback:\n got: %q\nwant: %q", string(onDisk), v1Content)
	}

	// 重新加载后内存视图也应回到 v1。
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got, err := m.Get("snap-skill")
	if err != nil {
		t.Fatalf("Get after rollback: %v", err)
	}
	if got.Content != v1Content {
		t.Fatalf("in-memory content mismatch after rollback:\n got: %q\nwant: %q", got.Content, v1Content)
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// newTestManager 构造一个指向临时目录的 Manager，隔离全局 MAGIC_HOME。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManagerWithConfig(&ManagerConfig{
		SearchDirs: []string{dir},
		BuiltinDir: filepath.Join(dir, "builtin"),
	})
	if err != nil {
		t.Fatalf("NewManagerWithConfig: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}
