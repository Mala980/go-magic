package memory

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 回归：常驻偏好（StandingPreferences）必须在**不依赖 query 匹配**的前提下
// 被取到，且只限 user/preference 类型 + 高重要度。
//
// 起因（2026-10-15）：用户把「commit message 必须用英文」写进记忆库，
// 实际提交仍是中文。根因是打分召回链路：中文 query 走
// cjkBigramRelevance，而记忆正文是纯英文 → bigram 重叠 0 → score=0 →
// 被 `score > 0.01` 丢弃。偏好是**指令**不是知识，不能因为措辞不匹配就消失。
//
// KnownIssue（本次未修）：打分召回**仍**不支持跨语言匹配——纯英文的
// knowledge/project 记忆在中文 query 下依旧召回不到。常驻偏好通道只解决
// user/preference 类指令，知识类跨语言召回需要另一套方案（如查询向量扩展或
// CJK→英文别名表），不在本文件范围。
func newStandingStore(t *testing.T, scope string) *Store {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "memory.db")
	cfg.WorkspaceScope = scope
	s, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStandingPreferencesIgnoresQuery(t *testing.T) {
	s := newStandingStore(t, "d:/work/proj")

	// 纯英文正文的硬规则，importance 0.95
	if err := s.Store(&Memory{
		Type:       TypePreference,
		Content:    "git commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}
	// 弱推断偏好，低于门槛，不该被常驻注入
	if err := s.Store(&Memory{
		Type:       TypePreference,
		Content:    "user used the python tool in this session",
		Importance: 0.4,
	}); err != nil {
		t.Fatalf("store weak: %v", err)
	}

	got := s.StandingPreferences("d:/work/proj", 8)
	if !containsContent(got, "must be written in English") {
		t.Fatalf("高重要度英文偏好未被常驻召回，得到 %d 条", len(got))
	}
	if containsContent(got, "python tool") {
		t.Fatal("低重要度偏好被当成常驻指令注入了")
	}
}

func TestStandingPreferencesOnlyUserAndPreference(t *testing.T) {
	s := newStandingStore(t, "d:/work/proj")

	// 高重要度的 project/session/knowledge 也不该常驻注入，否则每轮
	// 会把整个记忆库塞进 prompt。
	for _, typ := range []MemoryType{TypeProject, TypeSession, TypeKnowledge, TypeAgent} {
		if err := s.Store(&Memory{
			Type:       typ,
			Content:    "high importance " + string(typ) + " content",
			Importance: 0.99,
		}); err != nil {
			t.Fatalf("store %s: %v", typ, err)
		}
	}
	if err := s.Store(&Memory{
		Type:       TypePreference,
		Content:    "commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store pref: %v", err)
	}

	got := s.StandingPreferences("d:/work/proj", 50)
	for _, m := range got {
		if m.Type != TypeUser && m.Type != TypePreference {
			t.Fatalf("非画像类型 %s 被常驻注入：%s", m.Type, m.Content)
		}
	}
	if !containsContent(got, "must be written in English") {
		t.Fatal("偏好记忆未被常驻召回")
	}
}

// TestStandingPreferencesCrossScope：user/preference 是跨目录画像，
// 换一个工作目录 scope 依然要能取到（否则换个项目就丢规则）。
func TestStandingPreferencesCrossScope(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "memory.db")
	alpha := newDirScopeStore(t, dbPath, "d:/work/alpha")
	beta := newDirScopeStore(t, dbPath, "d:/work/beta")

	if err := alpha.Store(&Memory{
		Type:       TypePreference,
		Content:    "git commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	if got := beta.StandingPreferences("d:/work/beta", 8); !containsContent(got, "must be written in English") {
		t.Fatalf("跨目录未取到画像偏好，得到 %d 条", len(got))
	}
}

func TestStandingPreferencesOrderedAndCapped(t *testing.T) {
	s := newStandingStore(t, "d:/work/proj")

	for i := 1; i <= 6; i++ {
		if err := s.Store(&Memory{
			Type:       TypePreference,
			Content:    "standing pref " + string(rune('A'+i)),
			Importance: 0.85 + float64(i)/100,
		}); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	got := s.StandingPreferences("d:/work/proj", 3)
	if len(got) != 3 {
		t.Fatalf("limit 未生效：期望 3 条，得到 %d", len(got))
	}
	// 重要度降序：最后写入的（importance 最高）应在最前
	if !strings.Contains(got[0].Content, "standing pref G") {
		t.Fatalf("未按重要度降序，首条=%s", got[0].Content)
	}
}

// TestStandingPreferencesStillScoreRecalled：确认常驻注入是**补充**而非替代——
// 知识类记忆仍要靠打分召回拿到。
func TestStandingPreferencesStillScoreRecalled(t *testing.T) {
	s := newStandingStore(t, "d:/work/proj")

	if err := s.Store(&Memory{
		Type:       TypePreference,
		Content:    "commit messages must be written in English",
		Importance: 0.95,
	}); err != nil {
		t.Fatalf("store pref: %v", err)
	}
	if err := s.Store(&Memory{
		Type:       TypeProject,
		Content:    "go-magic uses n-data-table for the connector list",
		Importance: 0.8,
	}); err != nil {
		t.Fatalf("store proj: %v", err)
	}

	// 知识类记忆仍走打分召回：查询词与内容同语言时才能命中
	// （中英跨语言召回是 KnownIssue，见文件头注释，不在本测试范围）。
	top := s.GetTopMemoriesByScope("d:/work/proj", "n-data-table connector list", 5, time.Now())
	if !containsContent(top, "n-data-table") {
		t.Fatalf("打分召回失效，知识记忆未取到，得到 %d 条", len(top))
	}
	// 常驻偏好则不依赖 query：中文输入也照样注入
	if !containsContent(s.StandingPreferences("d:/work/proj", 8), "must be written in English") {
		t.Fatal("常驻偏好未取到")
	}
}
