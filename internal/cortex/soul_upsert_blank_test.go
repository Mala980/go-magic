package cortex

import (
	"strings"
	"testing"
)

// maxConsecutiveBlankLines 返回 content 中最长连续空行数（以 \n 计）。
func maxConsecutiveBlankLines(content string) int {
	best, run := 0, 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			run++
			if run > best {
				best = run
			}
			continue
		}
		run = 0
	}
	return best
}

// TestUpsertManagedSectionNoBlankLineAccumulation 锁死空行累积回归。
//
// 线上症状：SOUL.md 的 `## Learned Preferences` 标记之后出现 48 行连续空行，
// 且每发生一次写入就+1（快照序列 maxblankrun 41→42→…→48，与写入次数一一对应）。
//
// 根因：upsertManagedSection 在重建管理区时无条件写成
//
//	header + marker + "\n" + region + tail
//
// 而 region 本身是以 "\n" 开头（regionStart 恰好落在 marker 之后，
// 吞掉了标记行自己的换行符）。于是每次写入都在 marker 与首个 `### ` 区块之间
// 多塞一个 "\n"，形成 `marker\n\n\n\n…### sec0`。追加分支只TrimRight 了尾部，
// 管不到这个逐次增长的头部空行。
func TestUpsertManagedSectionNoBlankLineAccumulation(t *testing.T) {
	const iterations = 20

	content := DefaultSoul
	for i := 0; i < iterations; i++ {
		content = upsertManagedSection(content, "sec", "body")
		content = upsertManagedSection(content, "other", "body")
	}

	// 标记之后最多只应有一个空行（Markdown 标题与首个子区块之间的分隔）。
	if got := maxConsecutiveBlankLines(content); got > 1 {
		t.Fatalf("blank lines accumulated in the managed region: %d consecutive blank lines after %d upserts\n%s",
			got, iterations*2, content)
	}
}

// TestUpsertManagedSectionPreservesLayout 锁死"区块被就地更新"时正文不被截断。
func TestUpsertManagedSectionPreservesLayout(t *testing.T) {
	content := upsertManagedSection(DefaultSoul, "a", "alpha")
	content = upsertManagedSection(content, "b", "beta")
	content = upsertManagedSection(content, "a", "alpha-v2")

	for _, want := range []string{"alpha-v2", "beta"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q after in-place update:\n%s", want, content)
		}
	}
	if strings.Contains(content, "\nalpha\n") {
		t.Fatalf("stale body survived the in-place update:\n%s", content)
	}
}