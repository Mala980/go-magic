package cortex

import (
	"strings"
	"testing"
)

// SplitSoulForPrompt 必须把 GEPA 机器区（`## Learned Preferences`）从稳定人格头里
// 分出去。回归点：GEPA 每 ~10 分钟就往机器区注入一次，而 agent 此前把整份 SOUL
// 作为**一个** system 块放进可缓存前缀 ⇒ 每次注入都让整个 system 前缀缓存失效，
// 几百轮的长任务里规则链/工作目录等稳定段被迫原价重发。
func TestSplitSoulForPromptSeparatesManagedRegion(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	payload := strings.Repeat("learned-", 10)
	sm.mu.Lock()
	sm.content = DefaultSoul + "\n\n" + learnedPreferencesMarker + "\n\n### injected\n" + payload + "\n"
	sm.mu.Unlock()

	stable, volatile := sm.SplitSoulForPrompt()

	if !strings.Contains(stable, "You are Magic") {
		t.Fatalf("stable part must keep the personality header, got:\n%s", stable)
	}
	if strings.Contains(stable, learnedPreferencesMarker) {
		t.Fatal("stable part must NOT contain the managed region (it changes every ~10 minutes)")
	}
	if !strings.Contains(volatile, learnedPreferencesMarker) || !strings.Contains(volatile, payload) {
		t.Fatalf("volatile part must carry the managed region, got:\n%s", volatile)
	}

	// 没有机器区时退化为单块，volatile 必须为空。
	sm.mu.Lock()
	sm.content = DefaultSoul
	sm.mu.Unlock()

	stable2, volatile2 := sm.SplitSoulForPrompt()
	if volatile2 != "" {
		t.Fatalf("no managed region → volatile must be empty, got %q", volatile2)
	}
	if stable2 != strings.TrimSpace(DefaultSoul) {
		t.Fatal("stable part must be the whole soul when there is no managed region")
	}
}

// GetSoulForPrompt 在拆分之后仍必须返回**完整**内容（兼容没有拆分注入能力的调用方）。
func TestGetSoulForPromptStaysComplete(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	payload := strings.Repeat("x", 80)
	sm.mu.Lock()
	sm.content = DefaultSoul + "\n\n" + learnedPreferencesMarker + "\n\n### s\n" + payload
	sm.mu.Unlock()

	got := sm.GetSoulForPrompt()
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("personality header missing from GetSoulForPrompt")
	}
	if !strings.Contains(got, learnedPreferencesMarker) {
		t.Fatal("managed region missing from GetSoulForPrompt")
	}
	if !strings.Contains(got, payload) {
		t.Fatal("managed payload missing from GetSoulForPrompt")
	}
}
