package cortex

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestStore 建一个落盘到临时目录的轨迹池。
func newTestStore(t *testing.T) *TrajectoryStore {
	t.Helper()
	ts, err := NewTrajectoryStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	return ts
}

// readGenFiles 返回世代目录下 gen_<id>.json 的 id 集合（供轮转测试断言）。
func readGenFiles(dir string) (map[int]bool, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "gepa", "generations"))
	if err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "gen_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "gen_"), ".json"))
		if err != nil {
			continue
		}
		out[id] = true
	}
	return out, nil
}

// TestStrategyFingerprintStable 验证指纹与展示名/ID 无关，只看内容。
func TestStrategyFingerprintStable(t *testing.T) {
	a := []PromptChange{
		{Type: "add", Section: "s1", NewContent: "alpha"},
		{Type: "add", Section: "s2", NewContent: "beta"},
	}
	b := []PromptChange{
		{Type: "add", Section: "s2", NewContent: "beta"},
		{Type: "add", Section: "s1", NewContent: "alpha"},
	}
	if strategyFingerprint(a) != strategyFingerprint(b) {
		t.Fatal("fingerprint must be order-independent")
	}

	c := []PromptChange{
		{Type: "add", Section: "s1", NewContent: "alpha"},
		{Type: "add", Section: "s2", NewContent: "GAMMA"},
	}
	if strategyFingerprint(a) == strategyFingerprint(c) {
		t.Fatal("fingerprint must differ for different content")
	}

	if strategyFingerprint(nil) != "" {
		t.Fatal("empty changes must fingerprint to empty string")
	}
}

// TestChangesEqualIgnoresNameAndOrder 验证内容级等价判定。
func TestChangesEqualIgnoresNameAndOrder(t *testing.T) {
	a := []PromptChange{{Type: "add", Section: "x", NewContent: "same"}}
	b := []PromptChange{{Type: "add", Section: "x", NewContent: "same"}}
	if !changesEqual(a, b) {
		t.Fatal("identical content must compare equal")
	}
	if changesEqual(a, nil) {
		t.Fatal("nil must not compare equal to non-empty")
	}
}

// TestDedupeStrategiesDropsDuplicates 锁死 P0-2 的种群去重：
// 线上连续 7 个世代的最优策略逐字节相同，根因就是种群内没有被去重。
func TestDedupeStrategiesDropsDuplicates(t *testing.T) {
	changes := []PromptChange{{Type: "add", Section: "a", NewContent: "payload"}}

	strategies := []OptimizationStrategy{
		{ID: "1", Name: "A", Changes: changes},
		{ID: "2", Name: "B", Changes: changes}, // 与 1 重复
		{ID: "3", Name: "C", Changes: changes}, // 与 1 重复
		{ID: "4", Name: "D", Changes: []PromptChange{{Type: "add", Section: "b", NewContent: "other"}}},
		{ID: "5", Name: "E", Changes: nil}, // 空变更集应被丢弃
	}

	out := dedupeStrategies(strategies, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 unique strategies, got %d", len(out))
	}
	if out[0].ID != "1" || out[1].ID != "4" {
		t.Fatalf("unexpected survivors: %s, %s", out[0].ID, out[1].ID)
	}
}

// TestAllSameFingerprint 验证"全部与上一代相同"的判定（退化空转识别）。
func TestAllSameFingerprint(t *testing.T) {
	changes := []PromptChange{{Type: "add", Section: "a", NewContent: "payload"}}
	fp := strategyFingerprint(changes)

	same := []OptimizationStrategy{
		{ID: "1", Changes: changes},
		{ID: "2", Changes: changes},
	}
	if !allSameFingerprint(same, fp) {
		t.Fatal("expected all-same detection")
	}

	mixed := []OptimizationStrategy{
		{ID: "1", Changes: changes},
		{ID: "2", Changes: []PromptChange{{Type: "add", Section: "b", NewContent: "diff"}}},
	}
	if allSameFingerprint(mixed, fp) {
		t.Fatal("mixed set must not be reported as all-same")
	}
}

// TestHasNewSignal 验证 P0-2 的"新数据门槛"判定。
func TestHasNewSignal(t *testing.T) {
	store := newTestStore(t)
	g := NewGEPAEngine(t.TempDir(), nil, store)

	// 高水位为 0（首次启动）→ 视为有新信号。
	if !g.hasNewSignal() {
		t.Fatal("zero high-water mark should count as new signal")
	}

	for i := 0; i < 3; i++ {
		if err := store.RecordTrajectory(&Trajectory{
			ID:        fmt.Sprintf("t%d", i),
			Task:      "task",
			Success:   true,
			StartTime: time.Now(),
			EndTime:   time.Now(),
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	g.mu.Lock()
	g.lastEvolvedTrajectoryCount = len(store.GetTrajectories(100))
	g.mu.Unlock()

	if g.hasNewSignal() {
		t.Fatal("no new trajectories → must not report new signal")
	}

	if err := store.RecordTrajectory(&Trajectory{ID: "t4", Task: "task2", Success: true}); err != nil {
		t.Fatalf("record t4: %v", err)
	}
	if !g.hasNewSignal() {
		t.Fatal("new trajectory must be detected")
	}
}

// TestConvergenceLatchAndUnlock 锁死 P0-3：收敛必须可自动解锁。
func TestConvergenceLatchAndUnlock(t *testing.T) {
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))

	g.mu.Lock()
	g.generations = make([]Generation, 60) // 满足 len > 50
	g.identicalRounds = 3
	g.maybeLatchConvergenceLocked()
	g.mu.Unlock()

	if !g.isConverged {
		t.Fatal("expected convergence latch after repeated identical rounds")
	}
	if !g.convergenceLatched {
		t.Fatal("expected convergenceLatched flag")
	}

	// 自动解锁。
	g.mu.Lock()
	g.unlockConvergenceLocked("test")
	g.mu.Unlock()
	if g.isConverged {
		t.Fatal("convergence must be unlockable")
	}
	if g.convergenceLatched {
		t.Fatal("latched flag must be cleared on unlock")
	}

	// 适应度回升也应解锁。
	g.mu.Lock()
	g.isConverged = true
	g.convergenceLatched = true
	g.unlockConvergenceLocked("fitness rebound")
	g.mu.Unlock()
	if g.isConverged {
		t.Fatal("fitness rebound must unlock convergence")
	}
}

// TestGetStatsExposesObservability P0-3：指标必须能回答"进化还在不在动"。
func TestGetStatsExposesObservability(t *testing.T) {
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))

	g.mu.Lock()
	g.isConverged = true
	g.convergenceLatched = true
	g.evolveRuns = 7
	g.evolveSkipped = 3
	g.evolveFailures = 1
	g.consecutiveFail = 1
	g.lastImprovement = 0.0123
	g.identicalRounds = 4
	g.lastEvolveAt = time.Now()
	g.mu.Unlock()

	stats := g.GetStats()
	for _, key := range []string{
		"is_converged", "convergence_latched", "evolve_runs", "evolve_skipped",
		"evolve_failures", "consecutive_failures", "last_improvement",
		"identical_rounds", "trajectories_consumed", "last_evolve_at",
		"best_strategy_applied", "generations_on_disk",
	} {
		if _, ok := stats[key]; !ok {
			t.Errorf("stats missing key %q (observability gap)", key)
		}
	}
	if stats["is_converged"] != true {
		t.Error("is_converged not reported")
	}
	if stats["convergence_latched"] != true {
		t.Error("convergence_latched not reported")
	}
	if stats["evolve_runs"] != 7 {
		t.Errorf("evolve_runs = %v, want 7", stats["evolve_runs"])
	}
}

// TestApplyBestStrategyManagedWritesToManagedRegion 锁死 P0-1 的 GEPA 侧修复：
// 策略写入必须落在机器可管理区，且跨进程幂等（不重复注入）。
func TestApplyBestStrategyManagedWritesToManagedRegion(t *testing.T) {
	sm := NewSoulManager(t.TempDir())
	if err := sm.Load(); err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat("optimized-content-", 6)
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))
	g.mu.Lock()
	g.bestStrategy = &OptimizationStrategy{
		ID:   "s1",
		Name: "test",
		Changes: []PromptChange{
			{Type: "add", Section: "quality", NewContent: body},
		},
	}
	g.mu.Unlock()

	applied, err := g.ApplyBestStrategyManaged(sm)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected 1 applied section, got %d", applied)
	}
	got := sm.GetSoul()
	if !strings.Contains(got, learnedPreferencesMarker) {
		t.Fatal("content must land inside the managed region")
	}
	if !strings.Contains(got, "### quality") {
		t.Fatal("named section missing")
	}
	if !strings.Contains(got, "You are Magic") {
		t.Fatal("header must be intact")
	}

	// 第二次调用：内容级去重必须让它变成 no-op（跨进程幂等）。
	applied2, err := g.ApplyBestStrategyManaged(sm)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if applied2 != 0 {
		t.Fatalf("second apply must be idempotent, got %d", applied2)
	}
	if strings.Count(sm.GetSoul(), body) != 1 {
		t.Fatal("content duplicated on second apply")
	}
}

// TestManagedPayloadsCollectsFromGenerations 验证压缩所需的载荷枚举。
func TestManagedPayloadsCollectsFromGenerations(t *testing.T) {
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))
	long := strings.Repeat("p", 80)
	short := "tiny"

	g.mu.Lock()
	g.generations = []Generation{
		{ID: 1, BestStrategy: &OptimizationStrategy{Changes: []PromptChange{
			{Type: "add", NewContent: long},
			{Type: "add", NewContent: short}, // 太短，应被过滤
		}}},
		{ID: 2, BestStrategy: &OptimizationStrategy{Changes: []PromptChange{
			{Type: "add", NewContent: long}, // 重复，应被去重
		}}},
	}
	g.mu.Unlock()

	payloads := g.ManagedPayloads()
	if len(payloads) != 1 {
		t.Fatalf("expected 1 distinct long payload, got %d", len(payloads))
	}
	if payloads[0] != long {
		t.Fatal("unexpected payload content")
	}
}

// TestGenerationRotationPrunes 验证 P2 的世代史轮转。
func TestGenerationRotationPrunes(t *testing.T) {
	dir := t.TempDir()
	g := NewGEPAEngine(dir, nil, newTestStore(t))

	total := maxPersistedGenerations + 25
	for i := 1; i <= total; i++ {
		g.saveGeneration(&Generation{ID: i, Timestamp: time.Now()})
	}

	got, err := readGenFiles(dir)
	if err != nil {
		t.Fatalf("read gen dir: %v", err)
	}
	if len(got) > maxPersistedGenerations {
		t.Fatalf("generation files not rotated: %d > %d", len(got), maxPersistedGenerations)
	}
	if !got[total] {
		t.Fatalf("newest generation %d was pruned", total)
	}
	if got[1] {
		t.Fatal("oldest generation should have been rotated away")
	}
}

// TestLoadGenerationsAlignsHighWaterMark 验证重启不会把旧轨迹再"进化"一遍。
func TestLoadGenerationsAlignsHighWaterMark(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t)
	for i := 0; i < 2; i++ {
		if err := store.RecordTrajectory(&Trajectory{ID: fmt.Sprintf("x%d", i), Task: "t", Success: true}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	// 先写一个世代文件，模拟"上一进程已进化过"。
	seed := NewGEPAEngine(dir, nil, store)
	seed.saveGeneration(&Generation{ID: 5, Timestamp: time.Now()})

	// 新进程启动。
	fresh := NewGEPAEngine(dir, nil, store)
	fresh.loadGenerations()

	fresh.mu.RLock()
	hwm := fresh.lastEvolvedTrajectoryCount
	gen := fresh.currentGen
	fresh.mu.RUnlock()

	if gen != 5 {
		t.Fatalf("currentGen = %d, want 5", gen)
	}
	if hwm == 0 {
		t.Fatal("high-water mark must be aligned after loading generations (else duplicate evolution)")
	}
	if fresh.hasNewSignal() {
		t.Fatal("no new trajectories after restart → must not report new signal")
	}
}
