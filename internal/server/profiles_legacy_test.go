package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// 旧布局遗留的 profiles/default 如果只是空骨架，列表接口应顺手清掉：
// 它不参与运行，留着只会让人以为那里有一份数据。
func TestScanProfilesRemovesEmptyLegacyDefault(t *testing.T) {
	s := newProfileTestServer(t)

	legacy := filepath.Join(s.magicHome, "profiles", appconfig.DefaultProfileName)
	for _, sub := range []string{"cache", "logs", "memory", "plugins", "sessions", "skills"} {
		if err := os.MkdirAll(filepath.Join(legacy, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	rec := doProfileReq(t, s, http.MethodGet, "/api/profiles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("空的遗留 profiles/default 应被清理，stat err=%v", err)
	}
	// 列表里仍然只有一张 default 卡片。
	if n := len(profileNames(t, rec)); n != 1 {
		t.Fatalf("profiles count = %d, want 1: %v", n, profileNames(t, rec))
	}
}

// 有实际文件的遗留目录一律不动：那是用户数据，自动删除/迁移都可能不可逆。
func TestScanProfilesKeepsNonEmptyLegacyDefault(t *testing.T) {
	s := newProfileTestServer(t)

	legacy := filepath.Join(s.magicHome, "profiles", appconfig.DefaultProfileName)
	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sessionFile := filepath.Join(legacy, "sessions", "old.json")
	if err := os.WriteFile(sessionFile, []byte(`{"keep":true}`), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	rec := doProfileReq(t, s, http.MethodGet, "/api/profiles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(sessionFile); err != nil {
		t.Fatalf("含内容的遗留目录被误删: %v", err)
	}
	if names := profileNames(t, rec); len(names) != 1 || names[0] != appconfig.DefaultProfileName {
		t.Fatalf("profiles = %v, want 仅 default（遗留 default 不应出现在列表里）", names)
	}
}

func profileNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var resp struct {
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal profiles: %v (body=%s)", err, rec.Body.String())
	}
	names := make([]string, 0, len(resp.Profiles))
	for _, p := range resp.Profiles {
		names = append(names, p.Name)
	}
	return names
}
