package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// deleteProfile 删除**正在生效**的分身时，凭据层必须与 switchProfile 一样回退：
// 撤销被删分身注入的 env 键，再加载 default 的 .env。以前只回退了 config.json，
// 进程内还挂着已删除分身的凭据。
func TestDeleteActiveProfileFallsBackEnvToDefault(t *testing.T) {
	s := newProfileTestServer(t)
	home := s.magicHome
	s.runtimeProfile = "work"
	s.setCfg(&appconfig.Config{})

	const workKey = "PROFILE_FIX_TEST_WORK_KEY"
	const defKey = "PROFILE_FIX_TEST_DEFAULT_KEY"

	workDir := appconfig.ProfileDir(home, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := appconfig.WriteEnvFile(appconfig.EnvFilePath(home, "work"), map[string]string{workKey: "from-work"}); err != nil {
		t.Fatal(err)
	}
	if err := appconfig.WriteEnvFile(appconfig.EnvFilePath(home, appconfig.DefaultProfileName), map[string]string{defKey: "from-default"}); err != nil {
		t.Fatal(err)
	}

	// 模拟进程按 work 启动：先清干净，再注入 work 的凭据。
	appconfig.ProcessEnv().Clear()
	if applied := appconfig.LoadEnvFor(home, "work"); applied == 0 {
		t.Fatalf("expected work .env to inject at least one key")
	}
	if got := os.Getenv(workKey); got != "from-work" {
		t.Fatalf("setup: %s = %q, want from-work", workKey, got)
	}
	t.Cleanup(func() {
		appconfig.ProcessEnv().Clear()
		_ = os.Unsetenv(workKey)
		_ = os.Unsetenv(defKey)
	})

	rec := doProfileReq(t, s, http.MethodDelete, "/api/profiles/work", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete work: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Errorf("work dir should be gone, stat err = %v", err)
	}
	if got := s.cfgSnapshot().Profile; got != appconfig.DefaultProfileName {
		t.Errorf("config profile = %q, want %q", got, appconfig.DefaultProfileName)
	}
	// 被删分身注入的键必须撤销；default 的 .env 必须立即接管。
	if _, ok := os.LookupEnv(workKey); ok {
		t.Errorf("%s should be unset after deleting the active profile", workKey)
	}
	if got := os.Getenv(defKey); got != "from-default" {
		t.Errorf("%s = %q, want from-default (default .env should take over)", defKey, got)
	}
}

// 删除"已选定待重启"的分身时，config.json 同样指向它——不重置就是悬空引用。
func TestDeletePendingProfileResetsConfig(t *testing.T) {
	s := newProfileTestServer(t)
	home := s.magicHome
	s.runtimeProfile = appconfig.DefaultProfileName
	s.setCfg(&appconfig.Config{Profile: "work"})

	workDir := appconfig.ProfileDir(home, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	rec := doProfileReq(t, s, http.MethodDelete, "/api/profiles/work", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete work: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := s.cfgSnapshot().Profile; got != appconfig.DefaultProfileName {
		t.Errorf("config profile = %q, want %q (dangling pending selection must be reset)", got, appconfig.DefaultProfileName)
	}
}

// 改名"待生效"分身时配置必须跟着走；以前只处理了正在生效的情况。
func TestRenamePendingProfileUpdatesConfig(t *testing.T) {
	s := newProfileTestServer(t)
	home := s.magicHome
	s.runtimeProfile = appconfig.DefaultProfileName
	s.setCfg(&appconfig.Config{Profile: "work"})

	workDir := appconfig.ProfileDir(home, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	rec := doProfileReq(t, s, http.MethodPatch, "/api/profiles/work", map[string]interface{}{
		"name": "work", "new_name": "team",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename work: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(appconfig.ProfileDir(home, "work")); !os.IsNotExist(err) {
		t.Errorf("old dir should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "profiles", "team")); err != nil {
		t.Errorf("new dir missing: %v", err)
	}
	if got := s.cfgSnapshot().Profile; got != "team" {
		t.Errorf("config profile = %q, want team", got)
	}
}
