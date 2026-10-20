package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProfileEnv 在 t.TempDir() 造一个 magic home，并写入指定分身的 .env。
func writeProfileEnv(t *testing.T, home, profile, content string) string {
	t.Helper()
	path := EnvFilePath(home, profile)
	if path == "" {
		t.Fatalf("invalid profile %q", profile)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadProfileEnvInjectsActiveProfileVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GO_MAGIC_HOME", home)
	t.Setenv(ProfileEnvVar, "") // 让 config.json 决定

	const key = "MAGIC_TEST_PROFILE_ENV"
	_ = os.Unsetenv(key)
	processEnv.Clear()
	t.Cleanup(processEnv.Clear)

	writeProfileEnv(t, home, DefaultProfileName, key+"=from-dotenv\n")
	// config.json 不存在 → ActiveProfile() 落到 default，正好覆盖首次启动。
	if applied := LoadProfileEnv(); applied != 1 {
		t.Fatalf("expected 1 applied var, got %d", applied)
	}
	if got := os.Getenv(key); got != "from-dotenv" {
		t.Fatalf("env var not injected: %q", got)
	}
	if EnvSourceOf(key) != EnvSourceDotEnv {
		t.Fatalf("source should be dotenv, got %q", EnvSourceOf(key))
	}
}

func TestLoadProfileEnvUsesPinnedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GO_MAGIC_HOME", home)

	const key = "MAGIC_TEST_PROFILE_ENV_PINNED"
	_ = os.Unsetenv(key)
	processEnv.Clear()
	t.Cleanup(processEnv.Clear)

	// default 里的值不应生效：--profile work 已经把活动分身钉住了。
	writeProfileEnv(t, home, DefaultProfileName, key+"=default-value\n")
	writeProfileEnv(t, home, "work", key+"=work-value\n")
	t.Setenv(ProfileEnvVar, "work")

	LoadProfileEnv()
	if got := os.Getenv(key); got != "work-value" {
		t.Fatalf("pinned profile .env not used: %q", got)
	}
}

func TestLoadProfileEnvMissingFileIsQuiet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GO_MAGIC_HOME", home)
	processEnv.Clear()
	t.Cleanup(processEnv.Clear)

	if applied := LoadProfileEnv(); applied != 0 {
		t.Fatalf("no .env should mean 0 applied, got %d", applied)
	}
}

func TestActiveEnvPathFollowsProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GO_MAGIC_HOME", home)
	t.Setenv(ProfileEnvVar, "")

	if got, want := ActiveEnvPath(), filepath.Join(home, ".env"); got != want {
		t.Fatalf("default profile env path: got %q want %q", got, want)
	}

	t.Setenv(ProfileEnvVar, "work")
	want := filepath.Join(home, "profiles", "work", ".env")
	if got := ActiveEnvPath(); got != want {
		t.Fatalf("work profile env path: got %q want %q", got, want)
	}
}

func TestSetEnvVarInstantRespectsRealEnvironment(t *testing.T) {
	const key = "MAGIC_TEST_INSTANT_REAL"
	t.Setenv(key, "from-os")
	processEnv.Clear()
	t.Cleanup(processEnv.Clear)

	if SetEnvVarInstant(key, "from-panel") {
		t.Fatalf("must not claim a real env var as managed")
	}
	if got := os.Getenv(key); got != "from-os" {
		t.Fatalf("real env var was overwritten: %q", got)
	}
}

func TestUnsetEnvVarInstantLeavesRealEnvironmentAlone(t *testing.T) {
	const key = "MAGIC_TEST_INSTANT_KEEP"
	t.Setenv(key, "keep")
	processEnv.Clear()
	t.Cleanup(processEnv.Clear)

	if UnsetEnvVarInstant(key) {
		t.Fatalf("must not unset a variable it never set")
	}
	if got := os.Getenv(key); got != "keep" {
		t.Fatalf("real env var removed: %q", got)
	}
}
