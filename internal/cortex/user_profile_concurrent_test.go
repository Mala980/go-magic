package cortex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 两个实例指向同一目录 = 两个进程（gateway / CLI）持有各自的画像副本。
// 各自写不同的键，落盘后两边的结果都必须还在：不能整份覆盖。
func TestConcurrentWritersMergePreferences(t *testing.T) {
	dir := t.TempDir()

	gateway := NewUserProfile(dir)
	if err := gateway.Load(); err != nil {
		t.Fatalf("gateway Load: %v", err)
	}
	cli := NewUserProfile(dir)
	if err := cli.Load(); err != nil {
		t.Fatalf("cli Load: %v", err)
	}

	if err := gateway.LearnPreference("Editor", "neovim", "session"); err != nil {
		t.Fatalf("gateway LearnPreference: %v", err)
	}
	if err := cli.SetPreference("Shell", "zsh"); err != nil {
		t.Fatalf("cli SetPreference: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "USER.json"))
	if err != nil {
		t.Fatalf("read USER.json: %v", err)
	}
	var doc struct {
		Preferences map[string]*UserPreference `json:"preferences"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal USER.json: %v", err)
	}
	if p := doc.Preferences["Editor"]; p == nil || p.Value != "neovim" {
		t.Fatalf("Editor preference lost: %+v", doc.Preferences)
	}
	if p := doc.Preferences["Shell"]; p == nil || p.Value != "zsh" {
		t.Fatalf("Shell preference lost: %+v", doc.Preferences)
	}
}

// 本进程删除的键不能被磁盘上的旧版本"合并复活"。
func TestDeletedKeyIsNotResurrected(t *testing.T) {
	dir := t.TempDir()

	seed := NewUserProfile(dir)
	if err := seed.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	if err := seed.SetPreference("Role", "backend"); err != nil {
		t.Fatalf("seed SetPreference: %v", err)
	}

	editor := NewUserProfile(dir)
	if err := editor.Load(); err != nil {
		t.Fatalf("editor Load: %v", err)
	}
	// 清空 Role（面板"整体提交"时空值 = 删除）。
	if err := editor.UpdateProfileInfo(UserProfileInfo{Name: "Tom"}); err != nil {
		t.Fatalf("UpdateProfileInfo: %v", err)
	}

	reloaded := NewUserProfile(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if p := reloaded.GetPreference("Role"); p != nil {
		t.Fatalf("Role should be deleted, got %+v", p)
	}
	if p := reloaded.GetPreference("Name"); p == nil || p.Value != "Tom" {
		t.Fatalf("Name = %+v, want Tom", p)
	}
}

// 常驻进程（gateway）必须能读到别的进程刚写的偏好，而不只是在下次自己写盘时。
func TestReadPathSeesExternalWrites(t *testing.T) {
	dir := t.TempDir()

	gateway := NewUserProfile(dir)
	if err := gateway.Load(); err != nil {
		t.Fatalf("gateway Load: %v", err)
	}
	// 先让 gateway 缓存一份非空状态并拼一次 prompt。
	if err := gateway.SetPreference("Style", "concise"); err != nil {
		t.Fatalf("gateway SetPreference: %v", err)
	}
	_ = gateway.GetForPrompt()

	cli := NewUserProfile(dir)
	if err := cli.Load(); err != nil {
		t.Fatalf("cli Load: %v", err)
	}
	if err := cli.SetPreference("Editor", "emacs"); err != nil {
		t.Fatalf("cli SetPreference: %v", err)
	}

	if prompt := gateway.GetForPrompt(); !strings.Contains(prompt, "Editor: emacs") {
		t.Fatalf("prompt 未包含其它进程写入的偏好:\n%s", prompt)
	}
	// 面板读接口同样要看到外部改动。
	found := false
	for _, p := range gateway.GetAllPreferences() {
		if p.Key == "Editor" && p.Value == "emacs" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GetAllPreferences 未包含外部写入的 Editor")
	}
	// 本地已有偏好不能被外部状态冲掉。
	if p := gateway.GetPreference("Style"); p == nil || p.Value != "concise" {
		t.Fatalf("Style preference lost after refresh: %+v", p)
	}
}

// 面板删掉某项后，另一个进程（gateway，内存里还留着旧副本）不能把它写回去。
// USER.json 是整份快照，"内存有、磁盘没有且本进程没改过" = 已被别人删除。
func TestStaleReaderDoesNotResurrectDeletion(t *testing.T) {
	dir := t.TempDir()

	seed := NewUserProfile(dir)
	if err := seed.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	if err := seed.SetPreference("Role", "backend"); err != nil {
		t.Fatalf("seed SetPreference: %v", err)
	}

	// gateway 先加载（此时 Role 还在它的内存里）。
	gateway := NewUserProfile(dir)
	if err := gateway.Load(); err != nil {
		t.Fatalf("gateway Load: %v", err)
	}
	if p := gateway.GetPreference("Role"); p == nil {
		t.Fatalf("gateway 应看到 Role")
	}

	// 面板在另一个实例里删掉 Role。
	panel := NewUserProfile(dir)
	if err := panel.Load(); err != nil {
		t.Fatalf("panel Load: %v", err)
	}
	if err := panel.UpdateProfileInfo(UserProfileInfo{Name: "Tom"}); err != nil {
		t.Fatalf("panel UpdateProfileInfo: %v", err)
	}

	// gateway 之后再写别的键，不能把 Role 顺带写回去。
	if err := gateway.SetPreference("Editor", "vim"); err != nil {
		t.Fatalf("gateway SetPreference: %v", err)
	}

	final := NewUserProfile(dir)
	if err := final.Load(); err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if p := final.GetPreference("Role"); p != nil {
		t.Fatalf("被删除的 Role 被复活了: %+v", p)
	}
	if p := final.GetPreference("Editor"); p == nil || p.Value != "vim" {
		t.Fatalf("Editor = %+v, want vim", p)
	}
	if p := final.GetPreference("Name"); p == nil || p.Value != "Tom" {
		t.Fatalf("Name = %+v, want Tom", p)
	}
}

// 并发写同一份画像：文件锁 + 合并要同时做到"不丢更新"和"不写坏文件"。
func TestConcurrentSavesAreSerialized(t *testing.T) {
	dir := t.TempDir()

	seed := NewUserProfile(dir)
	if err := seed.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}

	keys := []string{"K0", "K1", "K2", "K3"}
	var wg sync.WaitGroup
	errs := make(chan error, len(keys))
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			up := NewUserProfile(dir)
			if err := up.Load(); err != nil {
				errs <- err
				return
			}
			if err := up.SetPreference(key, "v"); err != nil {
				errs <- err
			}
		}(i, key)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent writer: %v", err)
	}

	final := NewUserProfile(dir)
	if err := final.Load(); err != nil {
		t.Fatalf("final Load: %v", err)
	}
	for _, k := range keys {
		if p := final.GetPreference(k); p == nil || p.Value != "v" {
			t.Fatalf("偏好 %s 丢失: %+v", k, final.GetAllPreferences())
		}
	}
}
