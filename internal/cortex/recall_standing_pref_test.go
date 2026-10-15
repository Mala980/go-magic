package cortex

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/internal/memory"
)

// 端到端验证 RecallForInputScope：常驻偏好无条件注入 + 打分召回按需补充，
// 二者合并去重，且不受 query 语言影响。
//
// 背景（2026-10-15）：用户把「commit message 必须用英文」写进记忆库，实际
// 提交仍是中文。纯英文偏好在中文 query 下打分恒为 0 被整条丢弃，指令失效。

func newRecallTestManager(t *testing.T) (*Manager, *memory.Store) {
	t.Helper()
	cfg := memory.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "memory.db")
	cfg.WorkspaceScope = "d:/work/proj"
	store, err := memory.NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := &Manager{memoryStore: store}
	return m, store
}

// TestRecallInjectsEnglishPreferenceOnChineseQuery 是本次修复的核心断言：
// 英文偏好必须在中文输入下被注入。
func TestRecallInjectsEnglishPreferenceOnChineseQuery(t *testing.T) {
	m, store := newRecallTestManager(t)

	if err := store.Store(&memory.Memory{
		Type:       memory.TypePreference,
		Content:    "git commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	for _, q := range []string{"提交代码到仓库", "帮我提交一下", "commit the code", "ok", ""} {
		got := m.RecallForInputScope("d:/work/proj", q)
		if !strings.Contains(got, "must be written in English") {
			t.Fatalf("query=%q 未注入英文偏好，得到: %q", q, got)
		}
	}
}

// TestRecallMergesStandingAndRecalled：常驻偏好与打分召回的知识记忆要合并，
// 且同一条不能重复出现。
func TestRecallMergesStandingAndRecalled(t *testing.T) {
	m, store := newRecallTestManager(t)

	if err := store.Store(&memory.Memory{
		Type:       memory.TypePreference,
		Content:    "commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store pref: %v", err)
	}
	// 知识记忆：查询词与内容同语言，才能被打分召回命中
	if err := store.Store(&memory.Memory{
		Type:       memory.TypeProject,
		Content:    "go-magic uses n-data-table for the connector list",
		Importance: 0.8,
	}); err != nil {
		t.Fatalf("store proj: %v", err)
	}

	got := m.RecallForInputScope("d:/work/proj", "n-data-table connector list")
	if !strings.Contains(got, "must be written in English") {
		t.Fatal("常驻偏好缺失")
	}
	if !strings.Contains(got, "n-data-table") {
		t.Fatal("打分召回的知识记忆缺失")
	}
	if n := strings.Count(got, "must be written in English"); n != 1 {
		t.Fatalf("常驻偏好重复注入 %d 次", n)
	}
}

// TestRecallCapsTotalItems：常驻偏好膨胀时不能挤爆 prompt。
func TestRecallCapsTotalItems(t *testing.T) {
	m, store := newRecallTestManager(t)

	for i := 0; i < 20; i++ {
		if err := store.Store(&memory.Memory{
			Type:       memory.TypePreference,
			Content:    "standing preference number " + strings.Repeat("x", i) + " padding",
			Importance: 0.86,
		}); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	got := m.RecallForInputScope("d:/work/proj", "提交")
	if got == "" {
		t.Fatal("召回为空")
	}
	if r := []rune(got); len(r) > dynamicMemoryMaxChars+64 {
		t.Fatalf("召回结果超长：%d 字符", len(r))
	}
	// 条数封顶：dynamicMemoryMaxItems（按 SummarizeMemories 的编号行计数，
	// 不能数 "preference" 字样——类型标签与内容里各出现一次）
	items := 0
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "padding") {
			items++
		}
	}
	if items == 0 {
		t.Fatal("未注入任何常驻偏好")
	}
	if items > dynamicMemoryMaxItems {
		t.Fatalf("注入条数 %d 超过上限 %d", items, dynamicMemoryMaxItems)
	}
}

// TestRecallNilStoreSafe：未初始化记忆库时不能 panic。
func TestRecallNilStoreSafe(t *testing.T) {
	var m *Manager
	if got := m.RecallForInputScope("d:/work/proj", "提交"); got != "" {
		t.Fatalf("期望空串，得到 %q", got)
	}
	m2 := &Manager{}
	if got := m2.RecallForInputScope("d:/work/proj", "提交"); got != "" {
		t.Fatalf("期望空串，得到 %q", got)
	}
}
