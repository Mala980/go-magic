package cortex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealSoulFileIsNotCorrupted 用线上真实文件做回归验证。
//
// 线上 SOUL.md 曾经长期停在 65,385 B / 上限 65,536 B（余量 151 B，64 KB 时代），
// 不含任何 `## Learned Preferences` 管理区 —— 也就是说下一次注入必然触发
// 旧代码的头部截断。上限现已降到 16 KB，用例改为"距当前上限 200 B"。
//
// 注意**不要把"逼近上限"当成真实文件的固有属性**：线上跑过 compaction 之后
// 它可能只有几 KB（实测已降到 6,367 B），此时"超限写入被拒"根本不会发生，
// 断言会误报为回归。所以这里自己把副本填充到接近上限，让前提由测试掌握。
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

	// 把副本垫到"距上限 200 B"，还原出那次事故的文件形态。
	// 填充物放在末尾且不属于任何管理区：它不可被回收，因此超限写入只能被拒绝。
	seed := string(raw)
	const margin = 200
	if pad := maxSoulSize - margin - len(seed); pad > 0 {
		seed += "\n\n" + strings.Repeat("P", pad)
	} else if len(seed) >= maxSoulSize {
		t.Skipf("real SOUL.md already at/over the limit (%d bytes); cannot seed the near-limit state", len(seed))
	}
	if len(seed)+1000 <= maxSoulSize {
		t.Fatalf("test setup failed: padded copy (%d B) is not close enough to the %d B limit", len(seed), maxSoulSize)
	}

	// 在副本上操作，绝不触碰线上文件。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte(seed), 0644); err != nil {
		t.Fatalf("seed copy: %v", err)
	}

	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sm.GetSoul(); len(got) != len(seed) {
		t.Fatalf("copy mismatch: %d != %d", len(got), len(seed))
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

	// 超限写入只有两种合法结局，取决于文件里有没有可回收的管理区：
	//
	//	a) 没有任何可回收区块 → 拒绝写入，内容原样保留；
	//	b) 有可回收区块       → 回收后写入，内容可能变短但**必须仍是原文的
	//	   前缀**（扔掉的是末尾的自动生成段落，绝不能是头部/正文）。
	//
	// 两种都不能出现的第三种结局：头部被截掉。真实文件跑过 compaction 后
	// 带有管理区（实测 6.3 KB），此时会走 (b)，因此不能只断言 (a)。
	switch {
	case werr != nil:
		if got != seed {
			t.Fatal("REGRESSION on real file: write was refused but content still changed")
		}
		if st := sm.GetStats(); st.WritesRefused == 0 || st.HeadCutsAvoided == 0 {
			t.Fatalf("refusal not counted: %+v", st)
		}
		t.Logf("write correctly refused, header preserved: %v", werr)
	default:
		if len(got) > maxSoulSize {
			t.Fatalf("over-limit write slipped through: %d > %d", len(got), maxSoulSize)
		}
		// 回收掉的只能是自动生成的管理区（可能位于文件中段），人格头部必须
		// 原样保留。用"头部 N 字节仍是原文前缀"来卡：截头的事故会立刻失败。
		headLen := min(1024, len(seed))
		if !strings.HasPrefix(got, seed[:headLen]) {
			t.Fatalf("REGRESSION on real file: personality header tampered\n got[0:80]=%q\nseed[0:80]=%q",
				got[:min(80, len(got))], seed[:min(80, len(seed))])
		}
		if st := sm.GetStats(); st.HeadCutsAvoided != 0 {
			t.Fatalf("head cut was applied to the real file: %+v", st)
		}
		t.Logf("over-limit write trimmed down to %d bytes (reclaimed %d auto section(s))",
			len(got), sm.GetStats().SectionsDropped)
	}

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
