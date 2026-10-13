package cortex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealSoulFileIsNotCorrupted 用**线上真实文件**做回归验证。
//
// 线上 SOUL.md 的实测形态：65,385 B / 上限 65,536 B（余量 151 B），且不含
// `## Learned Preferences` 管理区标记 —— 也就是说早在任何一次注入之前，
// 它就已经处在"下一次写入必然触发旧代码头部截断"的位置。
//
// 跳过条件：真实文件不存在（CI / 新环境）。
func TestRealSoulFileIsNotCorrupted(t *testing.T) {
	const realPath = "/root/.magic/cortex/SOUL.md"
	if _, err := os.Stat(realPath); err != nil {
		t.Skipf("real SOUL.md not present: %v", err)
	}

	raw, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatalf("read real SOUL.md: %v", err)
	}
	if !strings.Contains(string(raw), "You are Magic") {
		t.Fatalf("real SOUL.md is already damaged (header missing); nothing to protect")
	}
	t.Logf("real SOUL.md: %d bytes (limit %d, headroom %d)", len(raw), maxSoulSize, maxSoulSize-len(raw))

	// 在副本上操作，绝不触碰线上文件。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), raw, 0644); err != nil {
		t.Fatalf("seed copy: %v", err)
	}

	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sm.GetSoul(); len(got) != len(raw) {
		t.Fatalf("copy mismatch: %d != %d", len(got), len(raw))
	}

	// 模拟"下一次后台注入"：旧实现在这里会把文件开头的整段人格头砍掉。
	oversized := sm.GetSoul() + "\n\n## gepa_injected\n" + strings.Repeat("X", 1000)
	werr := sm.SetSoul(oversized)

	got := sm.GetSoul()
	if !strings.Contains(got, "You are Magic") {
		t.Fatalf("REGRESSION on real file: header destroyed; first 80 bytes = %q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "Your core traits") {
		t.Fatal("REGRESSION on real file: personality body destroyed")
	}
	if got != string(raw) {
		t.Fatal("REGRESSION on real file: content changed even though the write should have been refused")
	}
	if werr == nil {
		t.Fatal("expected the over-limit write to be refused (no trimmable region exists)")
	}
	if st := sm.GetStats(); st.WritesRefused == 0 || st.HeadCutsAvoided == 0 {
		t.Fatalf("refusal not counted: %+v", st)
	}
	t.Logf("write correctly refused, header preserved: %v", werr)

	// 建立管理区后，同样的注入必须变成"可安全回收"，而不是继续顶上限。
	// 注意：真实文件已占用 99.8% 上限，因此这里的关键断言是"头部与上限都守住"，
	// 而非"注入内容一定留得住" —— 空间不足时淘汰注入内容正是期望行为。
	for i := 1; i <= 30; i++ {
		body := fmt.Sprintf("%s%d", strings.Repeat("gepa-generated-", 12), i)
		if err := sm.ApplyManagedSection(fmt.Sprintf("gepa_gen%d", i), body); err != nil {
			t.Fatalf("managed apply %d: %v", i, err)
		}
	}
	after := sm.GetSoul()
	if len(after) > maxSoulSize {
		t.Fatalf("managed growth escaped the limit: %d > %d", len(after), maxSoulSize)
	}
	if !strings.Contains(after, "You are Magic") {
		t.Fatal("header lost during managed growth")
	}
	st := sm.GetStats()
	t.Logf("after managed growth: %d bytes, dropped %d section(s), refused %d write(s)",
		len(after), st.SectionsDropped, st.WritesRefused)
}
