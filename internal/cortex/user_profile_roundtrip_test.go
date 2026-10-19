package cortex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// USER.json 丢失后只能靠 USER.md 回退。回退路径必须解析出干净的值：
// 旧实现会把 "## All Preferences" 的展示行 "- Name: Tom (source: explicit,
// confidence: 1.00)" 也解析成偏好，一次回退就把 "(source: ...)" 永久写进值里。
func TestUserProfileMarkdownRoundTripKeepsValueClean(t *testing.T) {
	dir := t.TempDir()
	up := NewUserProfile(dir)
	if err := up.SetPreference("Name", "Tom"); err != nil {
		t.Fatalf("SetPreference: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "USER.json")); err != nil {
		t.Fatalf("remove json: %v", err)
	}

	restored := NewUserProfile(dir)
	if err := restored.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	pref := restored.GetPreference("Name")
	if pref == nil {
		t.Fatal("Name preference lost after markdown fallback")
	}
	if pref.Value != "Tom" {
		t.Errorf("Name = %q, want %q (## All Preferences must not be parsed)", pref.Value, "Tom")
	}
	if restored.GetPreference("source") != nil || restored.GetPreference("confidence") != nil {
		t.Error("display-only fields leaked into preferences")
	}
}

// 同一份画像两次落盘必须逐字节一致：map 遍历顺序随机，不排序会让 USER.md
// 每次保存都产生无意义 diff。
func TestUserProfileSaveIsDeterministic(t *testing.T) {
	render := func(order []string) []byte {
		up := NewUserProfile(t.TempDir())
		for _, k := range order {
			up.preferences[k] = &UserPreference{Key: k, Value: "v-" + k, Source: "explicit", Confidence: 1.0}
		}
		if err := up.save(); err != nil {
			t.Fatalf("save: %v", err)
		}
		data, err := os.ReadFile(up.userPath)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return data
	}

	a := render([]string{"alpha", "beta", "gamma", "delta"})
	b := render([]string{"delta", "gamma", "beta", "alpha"})
	if string(a) != string(b) {
		t.Errorf("USER.md is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

func TestApplyFeedbackAdjustsConfidence(t *testing.T) {
	up := NewUserProfile(t.TempDir())
	if err := up.SetPreference("Preferred language", "Go"); err != nil {
		t.Fatalf("SetPreference: %v", err)
	}

	if err := up.ApplyFeedback("Preferred language", false); err != nil {
		t.Fatalf("ApplyFeedback(false): %v", err)
	}
	pref := up.GetPreference("Preferred language")
	if pref.Confidence >= 1.0 {
		t.Errorf("confidence = %v, want reduced", pref.Confidence)
	}
	if pref.Source != "feedback" {
		t.Errorf("source = %q, want feedback", pref.Source)
	}

	// 再否定一次应跌破 0.5 的注入阈值，从 prompt 里消失。
	if err := up.ApplyFeedback("Preferred language", false); err != nil {
		t.Fatalf("ApplyFeedback(false) 2: %v", err)
	}
	if prompt := up.GetForPrompt(); strings.Contains(prompt, "Preferred language") {
		t.Errorf("rejected preference still injected into prompt:\n%s", prompt)
	}

	if err := up.ApplyFeedback("Preferred language", true); err != nil {
		t.Fatalf("ApplyFeedback(true): %v", err)
	}
	if prompt := up.GetForPrompt(); !strings.Contains(prompt, "Preferred language") {
		t.Errorf("confirmed preference missing from prompt:\n%s", prompt)
	}

	if err := up.ApplyFeedback("missing", true); err == nil {
		t.Error("ApplyFeedback on unknown key should error")
	}
}

func TestUpdateProfileInfo(t *testing.T) {
	dir := t.TempDir()
	up := NewUserProfile(dir)

	if err := up.UpdateProfileInfo(UserProfileInfo{
		Name:               "Tom",
		Role:               "backend",
		CommunicationStyle: "concise",
		TechStack:          []string{"Go", "go ", "Vue"},
		Interests:          []string{"distributed systems"},
	}); err != nil {
		t.Fatalf("UpdateProfileInfo: %v", err)
	}

	pref := up.GetPreference("Name")
	if pref == nil || pref.Value != "Tom" || pref.Confidence != 1.0 {
		t.Fatalf("Name preference = %+v", pref)
	}
	if pref.Source != "explicit" {
		t.Errorf("source = %q, want explicit", pref.Source)
	}

	// 整体覆盖语义：空字符串 = 清空该项，未清空的字段必须原样保留。
	if err := up.UpdateProfileInfo(UserProfileInfo{Role: "backend", CommunicationStyle: "concise"}); err != nil {
		t.Fatalf("UpdateProfileInfo clear: %v", err)
	}
	if p := up.GetPreference("Name"); p != nil {
		t.Errorf("Name should be cleared, got %+v", p)
	}
	if p := up.GetPreference("Role"); p == nil || p.Value != "backend" {
		t.Errorf("Role should be kept, got %+v", p)
	}

	// 重新加载后仍然是同一份（JSON 与 md 都已落盘）。
	reloaded := NewUserProfile(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p := reloaded.GetPreference("Communication style"); p == nil || p.Value != "concise" {
		t.Errorf("communication style lost after reload: %+v", p)
	}
}

func TestGetPreferenceReturnsCopy(t *testing.T) {
	up := NewUserProfile(t.TempDir())
	if err := up.SetPreference("Name", "Tom"); err != nil {
		t.Fatalf("SetPreference: %v", err)
	}

	got := up.GetPreference("Name")
	got.Value = "mutated"

	if again := up.GetPreference("Name"); again.Value != "Tom" {
		t.Errorf("GetPreference returned an internal pointer: %q", again.Value)
	}
}
