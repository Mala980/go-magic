package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// default 没有独立目录（它就是 <magicHome>/cortex），所以 PATCH 改名必须被拒。
//
// 此前只挡了 DELETE：PATCH /api/profiles/default {"new_name":"x"} 会走
// os.Rename("<magicHome>/cortex", "<magicHome>/profiles/x")，把正在使用的
// SOUL/USER/记忆/日构建整体搬走 —— 运行中的进程立刻读不到自己的数据，而
// "default" 是内置名字，改完也回不去。这是不可逆的数据事故，务必钉住。
func TestRenameDefaultProfileIsRejected(t *testing.T) {
	s := newProfileTestServer(t)

	cortexDir := appconfig.ProfileDir(s.magicHome, appconfig.DefaultProfileName)
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		t.Fatalf("mkdir cortex: %v", err)
	}
	soulPath := filepath.Join(cortexDir, "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	rec := doProfileReq(t, s, http.MethodPatch, "/api/profiles/default", map[string]interface{}{
		"new_name": "renamed",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rename default: status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}

	// 数据必须原地不动：cortex 还在，新的 profiles/renamed 不能出现。
	if _, err := os.Stat(soulPath); err != nil {
		t.Fatalf("cortex SOUL.md moved away: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.profileRoot(), "renamed")); !os.IsNotExist(err) {
		t.Fatalf("profiles/renamed should not exist, stat err=%v", err)
	}
}

// 普通分身改名仍然要能工作（别把闸门做成"谁都改不了"）。
func TestRenameRegularProfileStillWorks(t *testing.T) {
	s := newProfileTestServer(t)

	oldDir := filepath.Join(s.profileRoot(), "work")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "SOUL.md"), []byte("work soul\n"), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	rec := doProfileReq(t, s, http.MethodPatch, "/api/profiles/work", map[string]interface{}{
		"new_name": "office",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename work: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	newDir := filepath.Join(s.profileRoot(), "office")
	if _, err := os.Stat(filepath.Join(newDir, "SOUL.md")); err != nil {
		t.Fatalf("renamed soul missing: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old dir should be gone, stat err=%v", err)
	}
}

// clone_from_default 必须真的克隆 default。此前它克隆的是"当前生效分身"：
// 用户在 work 分身下新建一个"从默认克隆"的分身，拿到的其实是 work 的人设。
func TestCreateProfileCloneFromDefaultIgnoresActiveProfile(t *testing.T) {
	s := newProfileTestServer(t)

	// 让 work 成为生效分身，并给它一份可辨识的人设。
	t.Setenv(appconfig.ProfileEnvVar, "work")
	workDir := filepath.Join(s.profileRoot(), "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "SOUL.md"), []byte("I am work\n"), 0o644); err != nil {
		t.Fatalf("write work soul: %v", err)
	}

	// default 的人设另行一份，内容必须能区分。
	cortexDir := appconfig.ProfileDir(s.magicHome, appconfig.DefaultProfileName)
	if err := os.MkdirAll(cortexDir, 0o755); err != nil {
		t.Fatalf("mkdir cortex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cortexDir, "SOUL.md"), []byte("I am default\n"), 0o644); err != nil {
		t.Fatalf("write default soul: %v", err)
	}

	rec := doProfileReq(t, s, http.MethodPost, "/api/profiles", map[string]interface{}{
		"name":               "copy",
		"clone_from_default": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create copy: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(s.profileRoot(), "copy", "SOUL.md"))
	if err != nil {
		t.Fatalf("read cloned soul: %v", err)
	}
	if string(got) != "I am default\n" {
		t.Fatalf("cloned soul = %q, want default's persona", string(got))
	}
}
