package cortex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realGenPayloads 从**已落盘的世代文件**中收集自动注入文本。
// 这些正是历史上被 GEPA 追加进 SOUL.md 的内容（无管理区标记）。
func realGenPayloads(t *testing.T) []string {
	t.Helper()
	dir := "/root/.magic/cortex/gepa/generations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("generation history not present: %v", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var gen Generation
		if err := json.Unmarshal(data, &gen); err != nil || gen.BestStrategy == nil {
			continue
		}
		for _, c := range gen.BestStrategy.Changes {
			body := strings.TrimSpace(c.NewContent)
			if len(body) >= 60 && !seen[body] {
				seen[body] = true
				out = append(out, body)
			}
		}
	}
	return out
}

// TestRealSOULCompactionReclaimsInjectedContent 验证对**线上真实 SOUL.md** 的压缩：
// 用世代史里的真实载荷，应该能回收大量被注入内容占用的空间，同时保住人格头。
//
// 这是"降价不是进化"那个问题的止血手段：85.9% 的 SOUL 是自动注入，而压缩此前
// 的标记条件永远不成立（GEPA 从不写 `## Learned Preferences`），因此从不生效。
func TestRealSOULCompactionReclaimsInjectedContent(t *testing.T) {
	const realPath = "/root/.magic/cortex/SOUL.md"
	raw, err := os.ReadFile(realPath)
	if err != nil {
		t.Skipf("real SOUL.md not present: %v", err)
	}
	payloads := realGenPayloads(t)
	if len(payloads) == 0 {
		t.Skip("no generation payloads available to compact against")
	}
	t.Logf("real SOUL.md=%d bytes, %d distinct generation payloads", len(raw), len(payloads))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), raw, 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sm := NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	before := len(sm.GetSoul())
	dropped, err := sm.CompactSoul(payloads)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	after := sm.GetSoul()

	t.Logf("compaction: %d -> %d bytes (-%d, %.1f%%), %d section(s) dropped",
		before, len(after), before-len(after),
		100*float64(before-len(after))/float64(before), dropped)

	if dropped == 0 {
		t.Fatal("compaction reclaimed nothing — the pre-existing injections are still unreachable")
	}
	if !strings.Contains(after, "You are Magic") {
		t.Fatal("personality header lost during compaction")
	}
	if !strings.Contains(after, "Your core traits") {
		t.Fatal("personality body lost during compaction")
	}
	if len(after) >= before {
		t.Fatalf("compaction did not shrink the file: %d -> %d", before, len(after))
	}
	// 压缩后应重新获得可写入余量。
	if len(after) > maxSoulSize {
		t.Fatalf("still over limit after compaction: %d", len(after))
	}
	t.Logf("headroom recovered: %d bytes", maxSoulSize-len(after))
}

// TestEvictPrefersOldestSubsection 验证超限淘汰只损失**最旧的一段**，
// 而不是把整个管理区（连同标记）一起抹掉。
//
// 旧实现按"标记→下一个顶层标题"整段删除，一次超限写就会丢掉全部累积注入，
// 并让管理区标记消失 —— 之后所有注入又只能回到"从头部截断"的旧路径。
func TestEvictPrefersOldestSubsection(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(DefaultSoul)
	sb.WriteString("\n\n" + learnedPreferencesMarker + "\n\n")
	sb.WriteString("### oldest\n" + strings.Repeat("a", 3000) + "\n\n")
	sb.WriteString("### middle\n" + strings.Repeat("b", 3000) + "\n\n")
	sb.WriteString("### newest\n" + strings.Repeat("c", 3000))

	content := sb.String()
	out, dropped, refused := enforceSoulLimit(content, len(content)-500)
	if refused {
		t.Fatal("should be trimmable")
	}
	if dropped != 1 {
		t.Fatalf("expected exactly 1 section evicted, got %d", dropped)
	}
	if strings.Contains(out, "### oldest") {
		t.Fatal("oldest section should have been evicted first")
	}
	if !strings.Contains(out, "### middle") || !strings.Contains(out, "### newest") {
		t.Fatal("newer sections must survive")
	}
	if !strings.Contains(out, learnedPreferencesMarker) {
		t.Fatal("managed region marker must survive eviction")
	}
	if !strings.Contains(out, "You are Magic") {
		t.Fatal("personality header must survive eviction")
	}
	if len(out) > len(content)-500 {
		t.Fatalf("still over limit: %d", len(out))
	}
}

// TestEvictKeepsRegionWhenNoSubsections 验证管理区没有子区块时的兜底行为。
func TestEvictKeepsRegionWhenNoSubsections(t *testing.T) {
	content := DefaultSoul + "\n\n" + learnedPreferencesMarker + "\n\n" + strings.Repeat("z", 4000)
	out, dropped, refused := enforceSoulLimit(content, len(content)-100)
	if refused {
		t.Fatal("bare managed region should still be trimmable")
	}
	if dropped == 0 {
		t.Fatal("expected something to be dropped")
	}
	if !strings.Contains(out, "You are Magic") {
		t.Fatal("header must survive")
	}
	if len(out) > len(content)-100 {
		t.Fatalf("still over limit: %d", len(out))
	}
}
