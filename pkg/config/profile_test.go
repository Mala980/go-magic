package config

import (
	"path/filepath"
	"testing"
)

func TestValidProfileName(t *testing.T) {
	valid := []string{"default", "work", "work-2", "my_profile", "a.b", "工作", "A1"}
	for _, name := range valid {
		if !ValidProfileName(name) {
			t.Errorf("ValidProfileName(%q) = false, want true", name)
		}
	}

	invalid := []string{
		"", ".", "..", "../x", `..\x`, "a/b", `a\b`, "/abs", "C:", "c:evil",
		" lead", "trail ", "a b", "a\x00b", "con", "NUL", "com1", "lpt9",
	}
	for _, name := range invalid {
		if ValidProfileName(name) {
			t.Errorf("ValidProfileName(%q) = true, want false", name)
		}
	}

	long := make([]rune, 65)
	for i := range long {
		long[i] = 'a'
	}
	if ValidProfileName(string(long)) {
		t.Error("65-char name should be rejected")
	}
}

func TestProfileDir(t *testing.T) {
	home := filepath.Join("C:", "magic-home")

	// default（含空串）指向运行时目录，不是 profiles/default。
	if got, want := ProfileDir(home, ""), filepath.Join(home, "cortex"); got != want {
		t.Errorf("ProfileDir(\"\") = %q, want %q", got, want)
	}
	if got, want := ProfileDir(home, DefaultProfileName), filepath.Join(home, "cortex"); got != want {
		t.Errorf("ProfileDir(default) = %q, want %q", got, want)
	}

	if got, want := ProfileDir(home, "work"), filepath.Join(home, "profiles", "work"); got != want {
		t.Errorf("ProfileDir(work) = %q, want %q", got, want)
	}

	// 非法名字必须返回空串：调用方据此回 400，绝不能拿去拼路径跳出去。
	for _, bad := range []string{"..", "../..", `..\..`, "a/b", "C:evil"} {
		if got := ProfileDir(home, bad); got != "" {
			t.Errorf("ProfileDir(%q) = %q, want empty (traversal guard)", bad, got)
		}
		if got := SoulPath(home, bad); got != "" {
			t.Errorf("SoulPath(%q) = %q, want empty", bad, got)
		}
	}

	if got, want := SoulPath(home, "work"), filepath.Join(home, "profiles", "work", "SOUL.md"); got != want {
		t.Errorf("SoulPath(work) = %q, want %q", got, want)
	}
	if got, want := UserJSONPath(home, "work"), filepath.Join(home, "profiles", "work", "USER.json"); got != want {
		t.Errorf("UserJSONPath(work) = %q, want %q", got, want)
	}
}

func TestActiveProfile(t *testing.T) {
	t.Setenv(ProfileEnvVar, "")
	cfg := &Config{Profile: "work"}
	if got := cfg.ActiveProfile(); got != "work" {
		t.Errorf("ActiveProfile() = %q, want work", got)
	}

	// 环境变量（--profile）优先于持久化配置。
	t.Setenv(ProfileEnvVar, "cli")
	if got := cfg.ActiveProfile(); got != "cli" {
		t.Errorf("ActiveProfile() with env = %q, want cli", got)
	}

	var nilCfg *Config
	t.Setenv(ProfileEnvVar, "")
	if got := nilCfg.ActiveProfile(); got != DefaultProfileName {
		t.Errorf("nil ActiveProfile() = %q, want %q", got, DefaultProfileName)
	}

	// 手改 config.json 塞进穿越路径：回退 default，绝不进文件路径。
	bad := &Config{Profile: "../escape"}
	if got := bad.ActiveProfile(); got != DefaultProfileName {
		t.Errorf("ActiveProfile() with invalid config = %q, want %q", got, DefaultProfileName)
	}
	t.Setenv(ProfileEnvVar, "../escape")
	if got := cfg.ActiveProfile(); got != DefaultProfileName {
		t.Errorf("ActiveProfile() with invalid env = %q, want %q", got, DefaultProfileName)
	}
}
