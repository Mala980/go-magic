package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

func newProfileTestServer(t *testing.T) *Server {
	t.Helper()
	// 清掉 --profile 的环境变量，让 ActiveProfile 落到 default，测试不互相污染。
	t.Setenv(appconfig.ProfileEnvVar, "")
	return &Server{magicHome: t.TempDir()}
}

func doProfileReq(t *testing.T, s *Server, method, target string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	switch {
	case target == "/api/profiles":
		s.handleProfiles(rec, req)
	default:
		s.handleProfileByName(rec, req)
	}
	return rec
}

func TestProfileHandlersRejectPathTraversal(t *testing.T) {
	s := newProfileTestServer(t)

	targets := []string{
		"/api/profiles/..%5C..%5Cevil/soul", // Windows 反斜杠：不按 "/" 切分，能绕过 mux 清洗
		"/api/profiles/..%2F..%2Fevil/soul",
		"/api/profiles/C:evil/soul",
		"/api/profiles/./soul",
		"/api/profiles/work%00/soul",
	}
	for _, target := range targets {
		rec := doProfileReq(t, s, http.MethodGet, target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body=%s)", target, rec.Code, rec.Body.String())
		}
	}

	// 删除内置 default：必须拒绝，否则会 RemoveAll 掉整个 cortex 目录。
	rec := doProfileReq(t, s, http.MethodDelete, "/api/profiles/default", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete default: status = %d, want 400", rec.Code)
	}

	// 删除穿越路径同样必须拒绝。
	rec = doProfileReq(t, s, http.MethodDelete, "/api/profiles/..%5C..%5Cevil", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete traversal: status = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.magicHome), "evil")); err == nil {
		t.Fatal("traversal delete escaped the profiles root")
	}
}

func TestCreateProfileValidatesName(t *testing.T) {
	s := newProfileTestServer(t)

	rec := doProfileReq(t, s, http.MethodPost, "/api/profiles", map[string]interface{}{
		"name": "../escape",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal create: status = %d, want 400", rec.Code)
	}

	rec = doProfileReq(t, s, http.MethodPost, "/api/profiles", map[string]interface{}{
		"name": "work",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create work: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.magicHome, "profiles", "work")); err != nil {
		t.Fatalf("profile dir not created: %v", err)
	}

	// 重复创建应当 409，而不是静默覆盖。
	rec = doProfileReq(t, s, http.MethodPost, "/api/profiles", map[string]interface{}{
		"name": "work",
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want 409", rec.Code)
	}
}

func TestProfileSoulRoundTrip(t *testing.T) {
	s := newProfileTestServer(t)

	rec := doProfileReq(t, s, http.MethodPut, "/api/profiles/work/soul", map[string]string{
		"content": "# Soul\nBe concise.\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put soul: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 落在 profiles/<name>/SOUL.md（与 cortex 读的是同一份）。
	soulPath := filepath.Join(s.magicHome, "profiles", "work", "SOUL.md")
	data, err := os.ReadFile(soulPath)
	if err != nil {
		t.Fatalf("read %s: %v", soulPath, err)
	}
	if string(data) != "# Soul\nBe concise.\n" {
		t.Errorf("soul content = %q", string(data))
	}

	rec = doProfileReq(t, s, http.MethodGet, "/api/profiles/work/soul", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get soul: status = %d", rec.Code)
	}
	var resp struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Exists || resp.Content != "# Soul\nBe concise.\n" {
		t.Errorf("get soul = %+v", resp)
	}
}

func TestProfileUserRoundTripAndPreferences(t *testing.T) {
	s := newProfileTestServer(t)

	put := map[string]interface{}{
		"data": map[string]interface{}{
			"name":                "Tom",
			"role":                "backend engineer",
			"communication_style": "concise",
			"code_style":          "clean",
			"tech_stack":          []string{"Go", "Vue"},
			"interests":           []string{"distributed systems"},
		},
	}
	rec := doProfileReq(t, s, http.MethodPut, "/api/profiles/work/user", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("put user: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 表单值必须同时进入权威存储 USER.json 和人类可读视图 USER.md，
	// 否则"保存成功但对话没变化"会复现。
	dir := filepath.Join(s.magicHome, "profiles", "work")
	if _, err := os.Stat(filepath.Join(dir, "USER.json")); err != nil {
		t.Errorf("USER.json missing: %v", err)
	}
	md, err := os.ReadFile(filepath.Join(dir, "USER.md"))
	if err != nil {
		t.Fatalf("read USER.md: %v", err)
	}
	if !bytes.Contains(md, []byte("Tom")) {
		t.Errorf("USER.md does not contain the name: %s", md)
	}

	rec = doProfileReq(t, s, http.MethodGet, "/api/profiles/work/user", nil)
	var got struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if got.Data["name"] != "Tom" || got.Data["communication_style"] != "concise" {
		t.Errorf("user data = %+v", got.Data)
	}
	if stack, ok := got.Data["tech_stack"].([]interface{}); !ok || len(stack) != 2 {
		t.Errorf("tech_stack = %+v", got.Data["tech_stack"])
	}

	// 偏好页必须是真实数据，而不是写死的 mock。
	rec = doProfileReq(t, s, http.MethodGet, "/api/profiles/work/preferences", nil)
	var prefs struct {
		Preferences []struct {
			Key        string  `json:"key"`
			Value      string  `json:"value"`
			Confidence float64 `json:"confidence"`
		} `json:"preferences"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &prefs); err != nil {
		t.Fatalf("decode prefs: %v", err)
	}
	var nameConf float64
	found := false
	for _, p := range prefs.Preferences {
		if p.Key == "Name" {
			found = true
			nameConf = p.Confidence
		}
	}
	if !found {
		t.Fatalf("preferences missing Name: %+v", prefs.Preferences)
	}
	if nameConf != 1.0 {
		t.Errorf("explicit preference confidence = %v, want 1.0", nameConf)
	}

	// "不准"反馈要真正压低置信度（影响 GetForPrompt 的注入阈值）。
	rec = doProfileReq(t, s, http.MethodPost, "/api/profiles/work/preferences/Name/feedback", map[string]bool{
		"accurate": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("feedback: status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = doProfileReq(t, s, http.MethodGet, "/api/profiles/work/preferences", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &prefs); err != nil {
		t.Fatalf("decode prefs 2: %v", err)
	}
	for _, p := range prefs.Preferences {
		if p.Key == "Name" && p.Confidence >= nameConf {
			t.Errorf("confidence not reduced by negative feedback: %v -> %v", nameConf, p.Confidence)
		}
	}
}

func TestDeleteProfileRemovesDir(t *testing.T) {
	s := newProfileTestServer(t)
	dir := filepath.Join(s.magicHome, "profiles", "work")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	rec := doProfileReq(t, s, http.MethodDelete, "/api/profiles/work", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("profile dir still exists: %v", err)
	}

	// 删除不存在的分身：404，而不是假装成功。
	rec = doProfileReq(t, s, http.MethodDelete, "/api/profiles/work", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("delete missing: status = %d, want 404", rec.Code)
	}
}
