package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// newEnvTestServer 造一个只关心 .env 的最小 Server。
func newEnvTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	s := &Server{magicHome: home}
	// 让 activeProfile() 有快照可读（多半返回 default）。
	s.setCfg(&appconfig.Config{})
	return s, home
}

func doEnvRequest(t *testing.T, s *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/env", nil)
	} else {
		r = httptest.NewRequest(method, "/api/env", strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	s.handleEnv(w, r)
	return w
}

// 面板写入的 key 必须**立刻**在进程内可见。
//
// 这是本次修复的核心：以前只写文件，工具侧的 os.Getenv 读不到任何东西，
// 用户填完 key 依然得到"未配置"。
func TestHandleEnvSetTakesEffectImmediately(t *testing.T) {
	s, home := newEnvTestServer(t)
	const key = "MAGIC_TEST_PANEL_ENV"
	_ = os.Unsetenv(key)
	appconfig.ProcessEnv().Clear()
	t.Cleanup(func() {
		appconfig.ProcessEnv().Clear()
		_ = os.Unsetenv(key)
	})

	w := doEnvRequest(t, s, http.MethodPost, `{"key":"`+key+`","value":"sk-test"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if applied, _ := resp["applied"].(bool); !applied {
		t.Fatalf("response should report the value as applied: %v", resp)
	}
	if got := os.Getenv(key); got != "sk-test" {
		t.Fatalf("env var not visible to the process: %q", got)
	}

	// 落盘位置必须是 default 的 .env（活动分身是 default）。
	data, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("env file not written: %v", err)
	}
	if !strings.Contains(string(data), key+"=sk-test") {
		t.Fatalf("env file missing key: %s", data)
	}
}

func TestHandleEnvDeleteRevokesImmediately(t *testing.T) {
	s, home := newEnvTestServer(t)
	const key = "MAGIC_TEST_PANEL_ENV_DEL"
	_ = os.Unsetenv(key)
	appconfig.ProcessEnv().Clear()
	t.Cleanup(func() {
		appconfig.ProcessEnv().Clear()
		_ = os.Unsetenv(key)
	})

	doEnvRequest(t, s, http.MethodPost, `{"key":"`+key+`","value":"x"}`)
	if os.Getenv(key) != "x" {
		t.Fatalf("setup failed: key not set")
	}

	w := doEnvRequest(t, s, http.MethodDelete, `{"key":"`+key+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatalf("key still visible after delete")
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".env")); strings.Contains(string(data), key) {
		t.Fatalf("key still on disk after delete: %s", data)
	}
}

// 外部环境变量优先：面板写入不能覆盖它，且要如实告诉调用方"没生效"。
func TestHandleEnvSetDoesNotOverrideRealEnvironment(t *testing.T) {
	s, _ := newEnvTestServer(t)
	const key = "MAGIC_TEST_PANEL_ENV_REAL"
	t.Setenv(key, "from-os")
	appconfig.ProcessEnv().Clear()
	t.Cleanup(appconfig.ProcessEnv().Clear)

	w := doEnvRequest(t, s, http.MethodPost, `{"key":"`+key+`","value":"from-panel"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if applied, _ := resp["applied"].(bool); applied {
		t.Fatalf("must not claim a real env var was applied: %v", resp)
	}
	if note, _ := resp["note"].(string); note == "" {
		t.Fatalf("response should explain why the value is inert: %v", resp)
	}
	if got := os.Getenv(key); got != "from-os" {
		t.Fatalf("real env var overwritten: %q", got)
	}
}

// 键名里的换行能让一次请求往 .env 里塞进任意变量，必须拦住。
func TestHandleEnvRejectsKeyInjection(t *testing.T) {
	s, home := newEnvTestServer(t)

	for _, bad := range []string{
		`{"key":"A=1\nB=2","value":"x"}`,
		`{"key":"A B","value":"x"}`,
		`{"key":"1ABC","value":"x"}`,
		`{"key":"","value":"x"}`,
		`{"key":"A-B","value":"x"}`,
	} {
		w := doEnvRequest(t, s, http.MethodPost, bad)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", bad, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env should not have been created by rejected requests")
	}
}

// .env 路径必须跟随活动分身，否则在 work 分身里改的是 default 的凭据。
func TestEnvFilePathFollowsActiveProfile(t *testing.T) {
	s, home := newEnvTestServer(t)
	t.Setenv(appconfig.ProfileEnvVar, "")
	s.setCfg(&appconfig.Config{})

	if got, want := s.envFilePath(), filepath.Join(home, ".env"); got != want {
		t.Fatalf("default profile env path: got %q want %q", got, want)
	}

	s.setCfg(&appconfig.Config{Profile: "work"})
	want := filepath.Join(home, "profiles", "work", ".env")
	if got := s.envFilePath(); got != want {
		t.Fatalf("work profile env path: got %q want %q", got, want)
	}
}

// GET 必须告诉面板"这个 key 是不是外部环境提供的"，否则用户会反复重填。
func TestHandleEnvGetReportsSource(t *testing.T) {
	s, home := newEnvTestServer(t)
	const ours = "MAGIC_TEST_PANEL_SRC_OURS"
	const real = "MAGIC_TEST_PANEL_SRC_REAL"
	_ = os.Unsetenv(ours)
	t.Setenv(real, "from-os")
	appconfig.ProcessEnv().Clear()
	t.Cleanup(func() {
		appconfig.ProcessEnv().Clear()
		_ = os.Unsetenv(ours)
	})

	if err := os.WriteFile(filepath.Join(home, ".env"),
		[]byte(ours+"=a\n"+real+"=b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 必须用显式 home：server 的 magicHome 未必等于全局 GO_MAGIC_HOME。
	appconfig.LoadEnvFor(home, appconfig.DefaultProfileName)

	w := doEnvRequest(t, s, http.MethodGet, "")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if got := resp[ours]["source"]; got != string(appconfig.EnvSourceDotEnv) {
		t.Fatalf("%s source: got %v want dotenv", ours, got)
	}
	if editable, _ := resp[ours]["editable"].(bool); !editable {
		t.Fatalf("%s should be editable via the panel", ours)
	}

	if got := resp[real]["source"]; got != string(appconfig.EnvSourceReal) {
		t.Fatalf("%s source: got %v want environment", real, got)
	}
	if editable, _ := resp[real]["editable"].(bool); editable {
		t.Fatalf("%s must be reported as not editable", real)
	}
	if note, _ := resp[real]["note"].(string); note == "" {
		t.Fatalf("%s should carry an explanatory note", real)
	}
}

// .env 的注释与分组顺序在一次面板保存后必须还在（用户手工整理过）。
func TestHandleEnvPreservesCommentsAndOrder(t *testing.T) {
	s, home := newEnvTestServer(t)
	const key = "MAGIC_TEST_PANEL_ORDER"
	appconfig.ProcessEnv().Clear()
	t.Cleanup(func() {
		appconfig.ProcessEnv().Clear()
		_ = os.Unsetenv(key)
	})

	original := "# providers\nOPENAI_API_KEY=old\n\n# tools\nGITHUB_TOKEN=gh\n"
	path := filepath.Join(home, ".env")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if w := doEnvRequest(t, s, http.MethodPost, `{"key":"`+key+`","value":"v"}`); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "# providers\nOPENAI_API_KEY=old\n") {
		t.Fatalf("comments/order lost:\n%s", got)
	}
	if !strings.Contains(got, "# tools\nGITHUB_TOKEN=gh") {
		t.Fatalf("second group lost:\n%s", got)
	}
	if !strings.Contains(got, key+"=v") {
		t.Fatalf("new key not written:\n%s", got)
	}
}

// cloneProfile 的凭据来源必须走统一裁决：default 在 magicHome 下。
func TestCloneProfileCopiesDefaultEnv(t *testing.T) {
	s, home := newEnvTestServer(t)

	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("K=self\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 目标分身目录必须先存在（cloneProfile 只负责内容，不建目录）。
	workDir := filepath.Join(home, "profiles", "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := s.cloneProfile(appconfig.DefaultProfileName, "work"); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workDir, ".env"))
	if err != nil {
		t.Fatalf("target .env not created: %v", err)
	}
	if !strings.Contains(string(data), "K=self") {
		t.Fatalf("env not copied: %s", data)
	}
}
