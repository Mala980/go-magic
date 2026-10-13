package cortex

import (
	"context"
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
	g.lastEvolvedTrajectoryKey = store.LatestTrajectoryKey()
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

// TestNewSignalSurvivesTrajectoryPoolCap 回归 P0：高水位必须能越过
// GetTrajectories 的 limit 封顶。
//
// 回归点：此前 hasNewSignal / evolve 用 `len(GetTrajectories(100))` 当高水位，
// 而该长度被 limit 截断 —— 轨迹池一旦攒够 100 条（池上限 1000），长度恒为 100，
// 于是 `len <= lastCount` 恒真：GEPA 在 LLM 调用之前就永久返回
// errNoNewTrajectories，"进化"跑满 100 条轨迹后静默停摆，收敛闩锁再也解不开。
// 旧测试只造了 3~4 条轨迹（远低于封顶线），所以完全没抓到。
func TestNewSignalSurvivesTrajectoryPoolCap(t *testing.T) {
	store := newTestStore(t)

	// 攒到超过封顶线：130 条。
	for i := 0; i < 130; i++ {
		if err := store.RecordTrajectory(&Trajectory{
			ID:        fmt.Sprintf("p%d", i),
			Task:      "t",
			Success:   true,
			StartTime: time.Now(),
			EndTime:   time.Now(),
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	// 封顶前提成立：长度恒为 100，而位点仍能区分新旧。
	if got := len(store.GetTrajectories(100)); got != 100 {
		t.Fatalf("precondition broken: GetTrajectories(100) = %d, want the capped 100", got)
	}

	g := NewGEPAEngine(t.TempDir(), nil, store)
	g.mu.Lock()
	g.lastEvolvedTrajectoryKey = store.LatestTrajectoryKey()
	g.mu.Unlock()

	if g.hasNewSignal() {
		t.Fatal("no new trajectories → must not report new signal")
	}

	// 再来 30 条：封顶长度**不变**（仍是 100），但位点变了。
	for i := 0; i < 30; i++ {
		if err := store.RecordTrajectory(&Trajectory{
			ID: fmt.Sprintf("q%d", i), Task: "t2", Success: true,
		}); err != nil {
			t.Fatalf("record extra %d: %v", i, err)
		}
	}
	if got := len(store.GetTrajectories(100)); got != 100 {
		t.Fatalf("pool cap must keep length at 100, got %d", got)
	}
	if !g.hasNewSignal() {
		t.Fatal("30 new trajectories beyond the cap must still be detected as new signal")
	}
}

// TestEvolveGatePassesBeyondPoolCap 回归 P0 的第二半：evolve 的门槛也必须放行。
// 这里只验门槛（不真正调 LLM）：把高水位对齐到当前位点后，evolve 走
// errNoNewTrajectories；新增轨迹后必须**不再**返回该错误（即越过门槛、进入
// evaluator/LLM 阶段）。
func TestEvolveGatePassesBeyondPoolCap(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 120; i++ {
		if err := store.RecordTrajectory(&Trajectory{
			ID: fmt.Sprintf("g%d", i), Task: "t", Success: true,
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	g := NewGEPAEngine(t.TempDir(), nil, store)
	// provider 为 nil：generator 会失败，因此 evolve 的返回必然是"生成失败"
	// 而不是 errNoNewTrajectories —— 正是我们要区分的两种结果。
	aligned := store.LatestTrajectoryKey()
	g.mu.Lock()
	g.lastEvolvedTrajectoryKey = aligned
	g.mu.Unlock()

	if err := g.evolve(context.Background()); err != errNoNewTrajectories {
		t.Fatalf("aligned high-water mark must yield errNoNewTrajectories, got %v", err)
	}

	if err := store.RecordTrajectory(&Trajectory{ID: "fresh", Task: "t3", Success: true}); err != nil {
		t.Fatalf("record fresh: %v", err)
	}
	if err := g.evolve(context.Background()); err == errNoNewTrajectories {
		t.Fatal("new trajectory beyond the pool cap must pass the evolve gate (was permanently frozen)")
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
	hwmKey := fresh.lastEvolvedTrajectoryKey
	gen := fresh.currentGen
	fresh.mu.RUnlock()

	if gen != 5 {
		t.Fatalf("currentGen = %d, want 5", gen)
	}
	if hwmKey == "" {
		t.Fatal("high-water mark must be aligned after loading generations (else duplicate evolution)")
	}
	if hwmKey != store.LatestTrajectoryKey() {
		t.Fatalf("high-water key = %q, want the latest trajectory key %q", hwmKey, store.LatestTrajectoryKey())
	}
	if fresh.hasNewSignal() {
		t.Fatal("no new trajectories after restart → must not report new signal")
	}
}

// TestGEPAStatePersistsAcrossProcesses 回归 P2：收敛/计数状态必须落盘，
// 否则 `magic cortex gepa`（用 nil provider 构造 + LoadHistory）恒报
// is_converged=false、health=disabled —— 恰是"进化是不是停了"的关键信号读不到。
func TestGEPAStatePersistsAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t)
	for i := 0; i < 5; i++ {
		if err := store.RecordTrajectory(&Trajectory{ID: fmt.Sprintf("s%d", i), Task: "t", Success: true}); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	seed := NewGEPAEngine(dir, nil, store)
	seed.mu.Lock()
	seed.isConverged = true
	seed.convergenceLatched = true
	seed.evolveRuns = 11
	seed.consecutiveFail = 0
	seed.lastImprovement = 0.0042
	seed.lastEvolvedTrajectoryKey = store.LatestTrajectoryKey()
	seed.saveStateLocked()
	seed.mu.Unlock()

	// 新进程（模拟 CLI）：只 LoadHistory，provider 仍为 nil。
	fresh := NewGEPAEngine(dir, nil, store)
	fresh.LoadHistory()

	stats := fresh.GetStats()
	if stats["is_converged"] != true {
		t.Fatalf("is_converged not restored from disk: %v", stats["is_converged"])
	}
	if stats["convergence_latched"] != true {
		t.Fatalf("convergence_latched not restored from disk: %v", stats["convergence_latched"])
	}
	if stats["evolve_runs"] != 11 {
		t.Fatalf("evolve_runs not restored: %v", stats["evolve_runs"])
	}
	if stats["state_from_disk"] != true {
		t.Fatal("state_from_disk must be reported so callers know the numbers came from a real run")
	}
	if stats["provider_available"] != false {
		t.Fatal("provider_available must report this process has no LLM")
	}
	// 关键：没有 provider 也不能把"真实运行过的引擎"报成 disabled。
	if stats["health"] != "converged" {
		t.Fatalf("health = %v, want converged (persisted state must win over nil provider)", stats["health"])
	}
}
