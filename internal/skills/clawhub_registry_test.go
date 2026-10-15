package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 技能库曾经在两条路径上返回一份写死的 8 条「推荐」列表：
//   - 空关键词浏览（那时直接返回 featured，与 registry 里有什么毫无关系）
//   - 搜索无结果 / 接口解码失败（用 featured 兜底，把「没找到」伪装成「找到了」）
//
// 安装失败时还会用这份写死列表拼一个 SKILL.md 桩并报成功，用户拿到的是一个
// 正文等于自己描述的空壳技能：能被加载、能按名字命中、然后什么都不做。
//
// 这组测试锁死的是「列表必须来自 registry 真实返回」以及「失败必须报错」，
// 全部走本地 mock，不碰网络 —— 否则 registry 一抖动测试就开始随机失败。

// newTestClawHub 返回一个指向本地 mock 的 registry。
func newTestClawHub(t *testing.T, handler http.HandlerFunc) *ClawHubRegistry {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &ClawHubRegistry{client: srv.Client(), baseURL: srv.URL}
}

// TestClawHubEmptyQueryBrowsesRegistry：空关键词是「浏览」，必须走 /api/v1/skills
// 拿到 registry 真实持有的技能，而不是任何内置列表。
func TestClawHubEmptyQueryBrowsesRegistry(t *testing.T) {
	var gotPath, gotQuery string
	reg := newTestClawHub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"slug":"alpha","displayName":"Alpha","summary":"does alpha","ownerHandle":"alice","tags":{"latest":"v1.2.0"},"stats":{"downloads":42,"stars":7}},
			{"slug":"beta","displayName":"Beta","summary":"does beta","ownerHandle":"bob"}
		],"nextCursor":"cursor-1"}`))
	})

	got, err := reg.Search(context.Background(), "", 20)
	if err != nil {
		t.Fatalf("browse Search returned error: %v", err)
	}
	if gotPath != "/api/v1/skills" {
		t.Fatalf("empty query must browse /api/v1/skills, went to %q", gotPath)
	}
	if !strings.Contains(gotQuery, "limit=20") {
		t.Fatalf("browse must forward the limit, query was %q", gotQuery)
	}
	if len(got) != 2 {
		t.Fatalf("browse returned %d skills, want 2", len(got))
	}

	// 浏览响应里带着 stars/installs/author，这些字段在搜索响应里是没有的，
	// 所以浏览必须自己解析，不能事后靠别的表补齐。
	first := got[0]
	if first.SourceID != "alpha" || first.Name != "Alpha" {
		t.Errorf("first skill = %+v, want slug=alpha name=Alpha", first)
	}
	if first.Author != "alice" {
		t.Errorf("Author = %q, want alice", first.Author)
	}
	if first.Installs != 42 || first.Stars != 7 {
		t.Errorf("Installs/Stars = %d/%d, want 42/7", first.Installs, first.Stars)
	}
	if len(first.Tags) != 1 || first.Tags[0] != "v1.2.0" {
		t.Errorf("Tags = %v, want [v1.2.0]", first.Tags)
	}
	if got[1].SourceID != "beta" {
		t.Errorf("second skill source_id = %q, want beta", got[1].SourceID)
	}
}

// TestClawHubSearchHitsSearchEndpoint：有关键词才走 /api/v1/search，并且要把
// 关键词原样带过去。
func TestClawHubSearchHitsSearchEndpoint(t *testing.T) {
	var gotPath, gotQ string
	reg := newTestClawHub(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQ = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"score":1.5,"slug":"python","displayName":"Python Coding Guidelines","summary":"pep8 on rails","ownerHandle":"pydata"}
		]}`))
	})

	got, err := reg.Search(context.Background(), "python", 10)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotPath != "/api/v1/search" {
		t.Fatalf("keyword search must hit /api/v1/search, went to %q", gotPath)
	}
	if gotQ != "python" {
		t.Fatalf("q = %q, want python", gotQ)
	}
	if len(got) != 1 {
		t.Fatalf("Search returned %d skills, want 1", len(got))
	}
	if got[0].SourceID != "python" || got[0].Name != "Python Coding Guidelines" {
		t.Errorf("skill = %+v", got[0])
	}
	if got[0].Installs != 0 || got[0].Stars != 0 {
		t.Errorf("search response carries no counts, got installs=%d stars=%d", got[0].Installs, got[0].Stars)
	}
}

// TestClawHubEmptyResultsAreNotPadded：没命中就是没命中。用内置列表兜底会把
// 一堆无关技能伪装成搜索结果，用户无法区分「没有这个技能」和「这就是结果」。
func TestClawHubEmptyResultsAreNotPadded(t *testing.T) {
	reg := newTestClawHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	})

	got, err := reg.Search(context.Background(), "definitely-not-a-real-skill", 10)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a search miss returned %d skills (first=%q); it must stay empty so the UI can say \"no match\"",
			len(got), got[0].Name)
	}
}

// TestClawHubBrowseSkipsItemsWithoutSlug：没有 slug 就装不上，不能塞进列表。
func TestClawHubBrowseSkipsItemsWithoutSlug(t *testing.T) {
	reg := newTestClawHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"slug":"","displayName":"Ghost","summary":"cannot be installed"},
			{"slug":"real","displayName":"Real","summary":"can be installed"}
		]}`))
	})

	got, err := reg.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("browse returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("browse returned %d skills, want 1 (slug-less item skipped)", len(got))
	}
	if got[0].SourceID != "real" {
		t.Errorf("surviving skill source_id = %q, want real", got[0].SourceID)
	}
}

// TestClawHubBrowseDecodeFailureIsAnError：解码失败要报错，不能悄悄换成内置列表。
func TestClawHubBrowseDecodeFailureIsAnError(t *testing.T) {
	reg := newTestClawHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json at all`))
	})

	if _, err := reg.Search(context.Background(), "", 10); err == nil {
		t.Fatal("undecodable browse response must return an error, not fall back to a canned list")
	}
}

// TestClawHubDownloadFailureLeavesNoStub：安装失败必须报错，且不能在目标目录
// 留下一个看起来装成功了的文件。
func TestClawHubDownloadFailureLeavesNoStub(t *testing.T) {
	reg := newTestClawHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	dir := t.TempDir()
	err := reg.DownloadAndInstall(context.Background(), "some-skill", "", dir)
	if err == nil {
		t.Fatal("failed download must return an error")
	}
	if !strings.Contains(err.Error(), "clawhub download returned 500") {
		t.Errorf("error should name the failing step, got %q", err.Error())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("failed install left files behind %v: it must not write a stub SKILL.md", names)
	}
}
