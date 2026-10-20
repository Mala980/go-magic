package cortex

import (
	"strings"
	"testing"
)

// TestCompactSoulMergesDuplicateSections 覆盖「语义重复章节从不被回收」这条缺口。
//
// 旧实现的淘汰机制只认"自动生成载荷"（needles）：内容以不同标题重复注入时
// 谁也删不掉，SOUL 长期只增不减（线上曾出现 196 个 `## ` 章节、85.9% 是注入
// 内容）。现在同一段正文只保留第一次出现的那一节。
func TestCompactSoulMergesDuplicateSections(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	body := strings.Join([]string{
		"Prefer small, verifiable changes.",
		"Run the tests before claiming success.",
		"Report blockers instead of guessing.",
	}, " ")
	content := DefaultSoul +
		"\n## Core Values\n" + body + "\n" +
		"\n## Core Values\n" + body + "\n" + // 逐字节重复
		"\n## core_drive\n" + strings.ReplaceAll(body, " ", "\n  ") + "\n" + // 仅排版不同
		"\n## Keep This One\nUnique content that must survive compaction, with enough length to count.\n"
	if err := sm.SetSoul(content); err != nil {
		t.Fatalf("SetSoul: %v", err)
	}

	removed, err := sm.CompactSoul(nil)
	if err != nil {
		t.Fatalf("CompactSoul: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2 (two duplicates)", removed)
	}

	got := sm.GetSoul()
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("personality header must survive compaction")
	}
	if n := strings.Count(got, "Prefer small, verifiable changes."); n != 1 {
		t.Errorf("duplicate body kept %d times, want 1", n)
	}
	if !strings.Contains(got, "Unique content that must survive") {
		t.Error("non-duplicate section must be kept")
	}
}

// TestCompactSoulKeepsShortSections 保证短区块（占位标题等）不参与去重：
// 它们规范化后接近空串，若一并压缩会把结构压平而不是回收冗余。
func TestCompactSoulKeepsShortSections(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	content := DefaultSoul + "\n## A\nTODO\n\n## B\nTODO\n"
	if err := sm.SetSoul(content); err != nil {
		t.Fatalf("SetSoul: %v", err)
	}

	removed, err := sm.CompactSoul(nil)
	if err != nil {
		t.Fatalf("CompactSoul: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (short sections are never deduped)", removed)
	}
	if n := strings.Count(sm.GetSoul(), "TODO"); n != 2 {
		t.Errorf("short sections were dropped: TODO count = %d, want 2", n)
	}
}

// TestDedupBodyKeyIgnoresFormatting 单元级锁定指纹口径。
func TestDedupBodyKeyIgnoresFormatting(t *testing.T) {
	a := "## x\nline one\nline two with some extra padding text here\n"
	b := "\n## y\nline one line two with some extra padding text here"
	ka, okA := dedupBodyKey(a)
	kb, okB := dedupBodyKey(b)
	if !okA || !okB {
		t.Fatalf("both bodies should qualify: %v %v", okA, okB)
	}
	if ka != kb {
		t.Errorf("keys differ:\n%q\n%q", ka, kb)
	}
	if _, ok := dedupBodyKey("## t\nshort"); ok {
		t.Error("short body must not qualify")
	}
}
