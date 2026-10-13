package cortex

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSetSoulNeverTruncatesHeader 锁死 P0-1 回归。
//
// 线上真实形态：SOUL.md 已达 65,385 B（上限 65,536 B，余量 151 B），
// 而自动生成内容里**不存在** `## Learned Preferences` 标记（该标记只由
// UpdateFromFeedback 写，GEPA 走 applyAdd 追加 `## <Section>`）。
// 旧实现的裁剪分支在找不到标记时从**文件头部**截断，于是下一次写入
// 会静默删掉 "You are Magic…Your capabilities/Guidelines" 整段人格头。
//
// 本测试用真实线上尺寸构造场景，断言：任何写入之后人格头都完好无损。
func TestSetSoulNeverTruncatesHeader(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// 构造 65,385 B 的 SOUL：人格头 + 大量无标记的自动生成区块。
	header := DefaultSoul
	var sb strings.Builder
	sb.WriteString(header)
	for sb.Len() < 65385 {
		sb.WriteString("\n## injected_section_x\n")
		sb.WriteString(strings.Repeat("y", 200))
	}
	if err := sm.SetSoul(sb.String()); err != nil {
		t.Fatalf("initial SetSoul: %v", err)
	}
	if got := len(sm.GetSoul()); got < 60000 {
		t.Fatalf("setup failed, size=%d", got)
	}
	if !strings.Contains(sm.GetSoul(), "You are Magic") {
		t.Fatal("setup lost header")
	}

	// 模拟 GEPA 再注入 300 B：旧实现会在这里砍掉头部。
	attempt := sm.GetSoul() + "\n\n## gepa_new\n" + strings.Repeat("z", 300)
	err := sm.SetSoul(attempt)

	got := sm.GetSoul()
	if !strings.Contains(got, "You are Magic") {
		t.Fatalf("P0-1 REGRESSION: personality header was truncated; first 80 bytes=%q", got[:min(80, len(got))])
	}
	if !strings.Contains(got, "Your capabilities") {
		t.Fatalf("P0-1 REGRESSION: capabilities block lost")
	}
	if len(got) > maxSoulSize {
		t.Fatalf("soul exceeds limit: %d > %d", len(got), maxSoulSize)
	}
	// 无可淘汰区块时必须拒绝写入（而不是破坏文件）。
	if err == nil {
		t.Fatal("expected write refusal when over limit with no trimmable region")
	}
	stats := sm.GetStats()
	if stats.WritesRefused == 0 {
		t.Fatal("WritesRefused should be > 0")
	}
	if stats.HeadCutsAvoided == 0 {
		t.Fatal("HeadCutsAvoided should be > 0")
	}
}

// TestApplyManagedSectionIsTrimmable 验证走机器可管理区的写入可以被安全淘汰。
func TestApplyManagedSectionIsTrimmable(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	// 大量自动生成内容，全部走管理区入口，区块名唯一（否则会被就地更新）。
	for i := 0; i < 400; i++ {
		name := "sec" + strconv.Itoa(i)
		body := strings.Repeat("q", 300) + strconv.Itoa(i)
		if err := sm.ApplyManagedSection(name, body); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
		if len(sm.GetSoul()) > maxSoulSize {
			t.Fatalf("soul exceeded limit at iteration %d: %d", i, len(sm.GetSoul()))
		}
	}

	got := sm.GetSoul()
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("personality header lost after many managed writes")
	}
	if !strings.Contains(got, learnedPreferencesMarker) {
		t.Fatal("managed region marker missing")
	}
	// 管理区写入超限时应通过淘汰（而非拒绝）收敛。
	if stats := sm.GetStats(); stats.SectionsDropped == 0 {
		t.Error("expected some sections to be dropped to stay within the limit")
	}
}

// TestApplyManagedSectionUpserts 验证同名区块就地更新，不重复追加。
func TestApplyManagedSectionUpserts(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := sm.ApplyManagedSection("topic", "first body"); err != nil {
		t.Fatal(err)
	}
	if err := sm.ApplyManagedSection("topic", "second body"); err != nil {
		t.Fatal(err)
	}

	got := sm.GetSoul()
	if strings.Count(got, "### topic") != 1 {
		t.Fatalf("expected exactly one `### topic` section, got %d\n%s", strings.Count(got, "### topic"), got)
	}
	if !strings.Contains(got, "second body") {
		t.Fatal("section was not updated in place")
	}
	if strings.Contains(got, "first body") {
		t.Fatal("stale body left behind")
	}
}

// TestSoulSnapshotAndRollback 锁死 P0-1b。
func TestSoulSnapshotAndRollback(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	original := sm.GetSoul()

	if err := sm.ApplyManagedSection("a", "content A"); err != nil {
		t.Fatal(err)
	}
	modified := sm.GetSoul()
	if modified == original {
		t.Fatal("expected modification")
	}

	if snaps := sm.ListSnapshots(); len(snaps) == 0 {
		t.Fatal("expected at least one snapshot after write")
	}

	if err := sm.RollbackSoul("latest"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := sm.GetSoul(); got != original {
		t.Fatalf("rollback mismatch:\n got=%q\nwant=%q", got, original)
	}
}

// TestSoulSnapshotsAreBounded 验证快照环形保留不会无限增长。
func TestSoulSnapshotsAreBounded(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxSoulSnapshots*3; i++ {
		if err := sm.ApplyManagedSection("s", strings.Repeat("x", 10+i)); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	if got := len(sm.ListSnapshots()); got > maxSoulSnapshots {
		t.Fatalf("snapshots not bounded: %d > %d", got, maxSoulSnapshots)
	}
}

// TestCompactSoulReclaimsInjectedContent 验证压缩只删自动生成区块，人格头保留。
func TestCompactSoulReclaimsInjectedContent(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	payload := strings.Repeat("injected-payload-", 8) // > 60 chars
	sm.mu.Lock()
	sm.content = DefaultSoul + "\n\n## injected_one\n\n" + payload + "\n"
	sm.mu.Unlock()

	dropped, err := sm.CompactSoul([]string{payload})
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("expected 1 dropped section, got %d", dropped)
	}
	got := sm.GetSoul()
	if strings.Contains(got, "injected-payload-") {
		t.Fatal("injected payload not removed")
	}
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("personality header lost during compaction")
	}
}

// TestCompactionKeepsHeaderGuard 验证压缩安全闸：若会丢头部则放弃，
// 且必须以**可判定**的方式告知调用方（哨兵错误），而不是静默返回 0 ——
// 否则运维看到的是"压缩执行了但什么都没删"，无从区分正常与故障。
func TestCompactionKeepsHeaderGuard(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}
	// 让人格头本身"看起来像"注入载荷，压缩应因安全闸而拒绝。
	sm.mu.Lock()
	sm.content = "## fake\n\n" + strings.Repeat("z", 80) + "\n"
	sm.mu.Unlock()
	before := sm.GetSoul()

	dropped, err := sm.CompactSoul([]string{strings.Repeat("z", 80)})
	if !errors.Is(err, ErrSoulCompactionHeaderGuard) {
		t.Fatalf("expected ErrSoulCompactionHeaderGuard, got %v", err)
	}
	if dropped != 0 {
		t.Fatal("compaction should be aborted when the header would be lost")
	}
	if sm.GetSoul() != before {
		t.Fatal("content changed despite aborted compaction")
	}
}

// TestCompactSoulPreservesCustomHeader 回归 P2：自定义过 SOUL 头的实例
// （不含标准 "You are Magic"）此前会被安全闸一刀切拦下 —— "压缩命令执行了但
// 什么都没删"。正确语义是：只要人格头（第 0 个区块）逐字节保住，就应该允许压缩。
func TestCompactSoulPreservesCustomHeader(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	const customHeader = "You are Atlas, a custom-persona assistant."
	payload := strings.Repeat("injected-", 10)
	sm.mu.Lock()
	sm.content = customHeader + "\n\n## injected\n\n" + payload + "\n"
	sm.mu.Unlock()

	dropped, err := sm.CompactSoul([]string{payload})
	if err != nil {
		t.Fatalf("compaction of a custom-header SOUL must not be refused: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("expected 1 dropped section, got %d", dropped)
	}
	got := sm.GetSoul()
	if !strings.Contains(got, customHeader) {
		t.Fatalf("custom header must survive compaction, got:\n%s", got)
	}
	if strings.Contains(got, "injected-") {
		t.Fatal("auto-generated section not removed")
	}
}

// TestSetSoulTrimDropsManagedSectionsOnly 验证裁剪只删机器区块。
func TestSetSoulTrimDropsManagedSectionsOnly(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	sb.WriteString(DefaultSoul)
	sb.WriteString("\n\n" + learnedPreferencesMarker + "\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("\n### s" + string(rune('a'+i%26)) + string(rune('0'+i%10)) + "\n")
		sb.WriteString(strings.Repeat("p", 250))
	}

	if err := sm.SetSoul(sb.String()); err != nil {
		t.Fatalf("SetSoul: %v", err)
	}
	got := sm.GetSoul()
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("header lost")
	}
	if len(got) > maxSoulSize {
		t.Fatalf("over limit: %d", len(got))
	}
	if got := sm.GetStats(); got.SectionsDropped == 0 {
		t.Error("expected dropped sections")
	}
}

// TestLoadCreatesDefaultAndSnapshotsDirIsSane 基础健壮性检查。
func TestLoadCreatesDefaultAndSnapshotsDirIsSane(t *testing.T) {
	dir := t.TempDir()
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SOUL.md")); err != nil {
		t.Fatalf("SOUL.md not created: %v", err)
	}
	// Load 不应产生快照（没有覆盖已有内容）。
	if snaps := sm.ListSnapshots(); len(snaps) != 0 {
		t.Fatalf("unexpected snapshots after fresh load: %d", len(snaps))
	}
}
