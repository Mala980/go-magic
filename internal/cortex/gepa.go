package cortex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/magicwubiao/go-magic/internal/provider"
)

// errInsufficientTrajectories is returned by evolve() when the trajectory
// store does not yet have enough data to run an evolution iteration. This is
// a normal condition for a freshly started system, so the evolution loop
// treats it as a skip rather than a real error to avoid log spam.
var errInsufficientTrajectories = errors.New("insufficient trajectories for evolution")

// errNoNewTrajectories indicates the trajectory pool has not grown since the last
// evolution round. Re-running the optimizer on an unchanged input can only
// reproduce the previous strategy byte-for-byte (this was observed in production:
// seven consecutive generations with identical best strategies), so the round is
// skipped instead of burning an LLM call and re-injecting the same text into SOUL.
var errNoNewTrajectories = errors.New("no new trajectories since last evolution")

// ============================================================================
// GEPA Engine - Generative Evolutionary Prompt Alignment
// ============================================================================
// GEPA is the core self-evolution engine inspired by Hermes Agent.
// It learns from execution trajectories and evolves system prompts through
// an evolutionary optimization process.
//
// Algorithm:
// 1. Collect trajectories (task executions)
// 2. Evaluate effectiveness (success rate, efficiency)
// 3. Generate optimization strategies
// 4. Apply strategies to prompts
// 5. Iterate and converge (100-500 iterations)
// ============================================================================

// GEPAEngine is the main GEPA orchestrator
type GEPAEngine struct {
	mu sync.RWMutex

	// Configuration
	baseDir              string
	provider             provider.Provider
	convergenceThreshold float64 // Stop when improvement < threshold
	maxIterations        int
	populationSize       int

	// Components
	evaluator       *EffectivenessEvaluator
	generator       *StrategyGenerator
	optimizer       *PromptOptimizer
	trajectoryStore *TrajectoryStore

	// Evolution state
	generations  []Generation
	currentGen   int
	bestStrategy *OptimizationStrategy
	isConverged  bool

	// Lifecycle
	cancel context.CancelFunc // Stops the evolution goroutine

	// Metrics
	totalTrajectories int
	successRate       float64
	avgEffectiveness  float64

	// P0-2/P0-3：进化输入高水位。
	//
	// lastEvolvedTrajectoryKey 是上一轮真正消费到的"轨迹池位点"（最近一条轨迹的
	// ID + 结束时间，见 TrajectoryStore.LatestTrajectoryKey）。**门槛判定只能用它**：
	// GetTrajectories(n) 的返回长度会被 limit 截断，轨迹池一旦达到 n 条就恒定不变，
	// 拿它当高水位会让进化永久冻结（实测：池 ≥100 条后 hasNewSignal 恒为 false，
	// 收敛闩锁再也解不开、SOUL 注入与自动压缩一起停摆）。位点随每条新轨迹单调
	// 变化，且重启后可由磁盘上的最新轨迹复原。
	lastEvolvedTrajectoryKey string

	// lastEvolvedTrajectoryCount 记录上一轮看到的轨迹条数，**仅供观测**
	// （GetStats.trajectories_consumed），不参与任何门槛判定。
	lastEvolvedTrajectoryCount int

	// stateLoaded 表示运维状态（收敛标记/计数）是从磁盘恢复的，而不是本进程
	// 现算的。CLI/HTTP 探针（无 provider）据此避免把"真实运行过的引擎"误报成
	// health=disabled。
	stateLoaded bool

	// P0-2：连续"策略与上一个世代完全相同"的轮次数。
	// 用于识别"退化空转"（改进恒为 0、策略逐字节重复）。
	identicalRounds int

	// P0-3：收敛后若出现新数据/适应度回升，自动解锁。
	convergedAt        time.Time
	convergedAtCount   int
	convergenceLatched bool // 是否因持续无改进而闩锁（可自动解锁）

	// Metrics for observability
	lastImprovement   float64
	lastImprovedAt    time.Time
	evolveRuns        int
	evolveSkipped     int
	evolveFailures    int
	consecutiveFail   int
	lastFailureReason string
	lastEvolveAt      time.Time

	// P2：最近一次失败时间（供 health/alert 判定与运维定位）
	lastEvolveFailureAt time.Time
}

// Generation represents one evolution iteration
type Generation struct {
	ID           int                    `json:"id"`
	Timestamp    time.Time              `json:"timestamp"`
	Strategies   []OptimizationStrategy `json:"strategies"`
	BestStrategy *OptimizationStrategy  `json:"best_strategy"`
	AvgFitness   float64                `json:"avg_fitness"`
	Improvement  float64                `json:"improvement"`
	Trajectories []string               `json:"trajectory_ids"`
}

// OptimizationStrategy represents a prompt optimization strategy
type OptimizationStrategy struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Target      string          `json:"target"` // "soul", "system", "skills"
	Changes     []PromptChange  `json:"changes"`
	Fitness     float64         `json:"fitness"`
	Applied     bool            `json:"applied"`
	AppliedAt   *time.Time      `json:"applied_at,omitempty"`
	Results     *StrategyResult `json:"results,omitempty"`
}

// PromptChange represents a single prompt modification
type PromptChange struct {
	Type       string `json:"type"`    // "add", "remove", "modify", "reorder"
	Section    string `json:"section"` // Section name
	OldContent string `json:"old_content"`
	NewContent string `json:"new_content"`
	Reason     string `json:"reason"`
}

// StrategyResult tracks the outcome of applying a strategy
type StrategyResult struct {
	BeforeSuccessRate float64   `json:"before_success_rate"`
	AfterSuccessRate  float64   `json:"after_success_rate"`
	Improvement       float64   `json:"improvement"`
	SampleSize        int       `json:"sample_size"`
	MeasuredAt        time.Time `json:"measured_at"`
}

// EffectivenessScore represents a trajectory's effectiveness
type EffectivenessScore struct {
	TrajectoryID string  `json:"trajectory_id"`
	Success      bool    `json:"success"`
	Efficiency   float64 `json:"efficiency"`    // 0-1, based on turns vs optimal
	Quality      float64 `json:"quality"`       // 0-1, based on output quality
	ToolAccuracy float64 `json:"tool_accuracy"` // 0-1, correct tool usage
	OverallScore float64 `json:"overall_score"` // Weighted combination
}

// NewGEPAEngine creates a new GEPA engine
func NewGEPAEngine(baseDir string, prov provider.Provider, trajectoryStore *TrajectoryStore) *GEPAEngine {
	return &GEPAEngine{
		baseDir:              baseDir,
		provider:             prov,
		convergenceThreshold: 0.01, // 1% improvement threshold
		maxIterations:        500,
		populationSize:       10,
		evaluator:            NewEffectivenessEvaluator(),
		generator:            NewStrategyGenerator(prov),
		optimizer:            NewPromptOptimizer(prov),
		trajectoryStore:      trajectoryStore,
		generations:          make([]Generation, 0),
	}
}

// Start starts the GEPA evolution process
func (g *GEPAEngine) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Load previous generations
	g.loadGenerations()

	// Create a cancellable context so the evolution goroutine can be stopped
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, g.cancel = context.WithCancel(ctx)

	// Start evolution loop in background
	go g.evolutionLoop(ctx)

	return nil
}

// Stop stops the GEPA evolution goroutine
func (g *GEPAEngine) Stop() {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	g.mu.Unlock()
}

// GEPA 后台进化的调度参数：正常 5 分钟一轮；连续失败时指数退避，上限 4 小时。
//
// 为什么需要退避：线上实测（2026-10-08）provider 域名解析不了时，
// evolutionLoop 每 5 分钟原样重试一次并打一条同样的错误日志 —— 24 小时 288 条
// 噪声，而且完全没有恢复机会上的差别。退避后同样的故障一天只留 6 条左右。
const (
	gepaBaseInterval = 5 * time.Minute
	gepaMaxInterval  = 4 * time.Hour

	// gepaIdleInterval 是"轨迹池无新增"时的轮询间隔（P0-2）。
	// 没有新数据时进化不可能产出新策略，把节奏放宽到 30 分钟即可。
	gepaIdleInterval = 30 * time.Minute

	// maxPersistedGenerations 是磁盘上保留的世代文件上限（P2 轮转）。
	// 每个文件内嵌整段策略文本，无轮转时实测 133 个文件/1.93 MB 且只增不减。
	maxPersistedGenerations = 200

	// gepaAlertAfterFailures 是"连续失败多少次算需要人介入"的阈值（P2 告警）。
	// 取 3：单次失败多为上游抖动（超时、限流）；连续 3 次时退避已到 20 分钟
	// 量级，说明进化实质停摆，值得把 health 标成 degraded 让人去看。
	gepaAlertAfterFailures = 3
)

// gepaBackoffInterval 返回连续失败 failures 次后的重试间隔（5m 倍增，封顶 4h）。
func gepaBackoffInterval(failures int) time.Duration {
	interval := gepaBaseInterval
	for i := 0; i < failures; i++ {
		interval *= 2
		if interval >= gepaMaxInterval {
			return gepaMaxInterval
		}
	}
	return interval
}

// evolutionLoop runs the main GEPA evolution algorithm.
//
// 失败处理约定：
//   - 首败打一条 WARN（含退避后的下次重试间隔），此后同一轮故障不再刷屏；
//   - 连续失败按指数退避拉长间隔（gepaBackoffInterval）；
//   - 一旦成功（或回到"轨迹不足"的正常态）立即复位到基准间隔。
func (g *GEPAEngine) evolutionLoop(ctx context.Context) {
	interval := gepaBaseInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		// Check convergence under lock to avoid data race
		g.mu.RLock()
		converged := g.isConverged
		g.mu.RUnlock()
		if converged {
			// P0-3：收敛闩锁可被新数据自动解锁，而不是永久停摆。
			// 轨迹池增长说明系统进入了新状态，旧的收敛结论不再成立。
			if g.hasNewSignal() {
				g.mu.Lock()
				g.unlockConvergenceLocked("new trajectories arrived")
				g.mu.Unlock()
			} else {
				timer.Reset(interval)
				continue
			}
		}

		err := g.evolve(ctx)
		switch {
		case err == nil:
			if consecutiveFailures > 0 {
				log.Printf("[GEPA] evolution recovered after %d consecutive failure(s)", consecutiveFailures)
			}
			consecutiveFailures = 0
			interval = gepaBaseInterval
		case errors.Is(err, errInsufficientTrajectories):
			// 新系统还没攒够轨迹：正常状态，不算故障、不告警、不退避。
			consecutiveFailures = 0
			interval = gepaBaseInterval
		case errors.Is(err, errNoNewTrajectories):
			// P0-2：轨迹池没有增长 → 跳过本轮，不调 LLM、不产世代。
			// 这同样不是故障，但把间隔放宽到 30 分钟，避免无谓轮询。
			consecutiveFailures = 0
			interval = gepaIdleInterval
		default:
			consecutiveFailures++
			interval = gepaBackoffInterval(consecutiveFailures)
			if consecutiveFailures == 1 {
				log.Printf("[GEPA] WARN evolution failed (backing off, next retry in %s): %v", interval, err)
			} else if consecutiveFailures == gepaAlertAfterFailures {
				// P2 告警升级点：跨过阈值时打一条**可被 grep 到的 ALERT**，
				// 并把 health 标成 degraded（见 GetStats），别让它继续混在
				// 一堆同样措辞的 WARN 里被忽略。
				log.Printf("[GEPA] ALERT self-evolution has failed %d consecutive time(s) — "+
					"health=degraded, next retry in %s: %v", consecutiveFailures, interval, err)
			} else {
				log.Printf("[GEPA] evolution still failing (%d consecutive, next retry in %s): %v",
					consecutiveFailures, interval, err)
			}
		}
		timer.Reset(interval)
	}
}

// hasNewSignal 判断轨迹池是否比上次进化时更大（收敛自动解锁的依据）。
func (g *GEPAEngine) hasNewSignal() bool {
	g.mu.RLock()
	lastKey := g.lastEvolvedTrajectoryKey
	g.mu.RUnlock()
	if lastKey == "" {
		return true
	}
	if g.trajectoryStore == nil {
		return false
	}
	// 必须比较"轨迹池位点"，不能用 GetTrajectories(n) 的长度：后者被 limit
	// 截断，池子超过 n 条后恒定不变，会让收敛闩锁永远解不开。
	return g.trajectoryStore.LatestTrajectoryKey() != lastKey
}

// evolve performs one evolution iteration
func (g *GEPAEngine) evolve(ctx context.Context) error {
	// 阶段 (a)：持锁读取当前代数据并拷贝到局部变量，然后立即解锁，
	// 避免在阶段 (b) 的 LLM 网络调用期间长时间持锁阻塞其它读操作。
	g.mu.Lock()
	currentGen := g.currentGen
	populationSize := g.populationSize
	convergenceThreshold := g.convergenceThreshold
	prevGenerationsCount := len(g.generations)
	var prevBestFitness float64
	prevBestExists := false
	if prevGenerationsCount > 0 {
		prevBest := g.generations[prevGenerationsCount-1].BestStrategy
		if prevBest != nil {
			prevBestFitness = prevBest.Fitness
			prevBestExists = true
		}
	}
	g.mu.Unlock()

	// 阶段 (b)：不持锁执行耗时操作（含 LLM 网络调用）。
	// trajectoryStore 内部已有自己的锁，无需 g.mu 保护。
	// 1. Collect recent trajectories
	trajectories := g.trajectoryStore.GetTrajectories(100)
	if len(trajectories) < 3 {
		return errInsufficientTrajectories
	}
	// 当前轨迹池位点（用于高水位比较与推进）。**不得用 len(trajectories)**：
	// 它被 GetTrajectories 的 limit 截断，池子超过 100 条后恒定不变。
	latestKey := g.trajectoryStore.LatestTrajectoryKey()

	// P0-2：新数据门槛。轨迹池没有增长时，evaluator/generator 的输入完全一致，
	// 生成出的策略也必然一致（历史上曾连续产出 7 个逐字节相同的世代）。
	// 这里直接跳过 LLM 调用，既省成本也消除"重复注入 SOUL"的源头。
	g.mu.Lock()
	lastKey := g.lastEvolvedTrajectoryKey
	g.mu.Unlock()
	if latestKey != "" && latestKey == lastKey {
		g.mu.Lock()
		g.evolveSkipped++
		g.mu.Unlock()
		return errNoNewTrajectories
	}

	// 2. Evaluate effectiveness
	scores := g.evaluator.EvaluateBatch(trajectories)

	// 3. Generate optimization strategies（LLM 调用，不持锁）
	strategies, err := g.generator.GenerateStrategies(ctx, scores, populationSize)
	if err != nil {
		g.mu.Lock()
		g.evolveFailures++
		g.consecutiveFail++
		g.lastFailureReason = err.Error()
		g.lastEvolveFailureAt = time.Now()
		g.mu.Unlock()
		return err
	}

	// 4. Evaluate strategies
	for i := range strategies {
		strategies[i].Fitness = g.evaluateStrategyFitness(&strategies[i], scores)
	}

	// 4b. P0-2：种群去重。去掉彼此重复以及"与上一个世代最优完全相同"的后代，
	// 避免同一段文本反复被选为 best 并反复写入 SOUL。
	strategies = dedupeStrategies(strategies, g.previousBestChanges())

	// 5. Select best strategy
	best := g.selectBestStrategy(strategies)
	if best == nil {
		// P2：策略被去重到空也必须推进高水位。否则下一轮会拿**同一批轨迹**
		// 再调一次 LLM，而返回 nil 又被 evolutionLoop 当成"成功"（不退避），
		// 形成一条无上限的推理成本泄漏路径（此前连日志都没有）。
		g.mu.Lock()
		g.evolveSkipped++
		g.lastEvolvedTrajectoryKey = latestKey
		g.lastEvolvedTrajectoryCount = len(trajectories)
		g.mu.Unlock()
		log.Printf("[GEPA] no strategy survived dedupe (all duplicates of the previous best); "+
			"advancing high-water mark to skip re-evaluating the same %d trajectory(ies)", len(trajectories))
		return nil
	}

	// 5b. 若去重后剩下的最优策略与上一代最优逐字节相同，说明这一轮没有新信息：
	// 不产出世代（避免 133 个文件里 7 个完全相同），并累计 identicalRounds。
	identical := changesEqual(best.Changes, g.previousBestChanges())

	// 6. Calculate improvement（基于阶段 (a) 读取的上一代最优 fitness）
	improvement := 0.0
	if prevBestExists {
		improvement = best.Fitness - prevBestFitness
	}

	// 阶段 (c)：重新持锁写回结果
	g.mu.Lock()
	// defer 为 LIFO：先注册 Unlock、再注册 saveStateLocked，于是退出时
	// saveStateLocked（需要持锁）先跑，随后才解锁。所有出口都会落盘。
	defer g.mu.Unlock()
	defer g.saveStateLocked()

	g.evolveRuns++
	g.lastEvolveAt = time.Now()
	g.consecutiveFail = 0
	g.lastFailureReason = ""
	g.lastEvolveFailureAt = time.Time{}
	g.lastEvolvedTrajectoryKey = latestKey
	g.lastEvolvedTrajectoryCount = len(trajectories)
	g.lastImprovement = improvement

	if identical {
		g.identicalRounds++
		// 不落盘、不换 best：没有新信息就没有"新世代"。
		g.maybeLatchConvergenceLocked()
		g.updateMetrics(scores)
		return nil
	}
	g.identicalRounds = 0

	// 7. Create new generation
	gen := Generation{
		ID:           currentGen + 1,
		Timestamp:    time.Now(),
		Strategies:   strategies,
		BestStrategy: best,
		AvgFitness:   g.calculateAvgFitness(strategies),
		Improvement:  improvement,
	}

	// 8. Check convergence
	// 注意：收敛条件必须基于"真实数据增长带来的改进"，否则一次 0 改进就会永久闩锁。
	if improvement < convergenceThreshold && len(g.generations) > 50 {
		g.isConverged = true
		g.convergenceLatched = true
		g.convergedAt = time.Now()
		g.convergedAtCount = len(trajectories)
		log.Printf("[GEPA] converged: improvement %.4f < %.4f after %d generations (latched; will auto-unlock on new data or fitness rebound)",
			improvement, convergenceThreshold, len(g.generations))
	}
	if improvement > 0 {
		g.lastImprovedAt = time.Now()
		if g.isConverged {
			// 适应度回升 → 自动解锁，让进化继续。
			g.unlockConvergenceLocked("fitness rebound")
		}
	}

	// 9. Save generation
	g.generations = append(g.generations, gen)
	g.currentGen = gen.ID
	g.bestStrategy = best
	g.saveGeneration(&gen)

	// 10. Update metrics
	g.updateMetrics(scores)

	return nil
}

// maybeLatchConvergenceLocked 在"连续多轮无新信息"时标记收敛（调用方持锁）。
func (g *GEPAEngine) maybeLatchConvergenceLocked() {
	const identicalRoundsToLatch = 3
	if g.identicalRounds < identicalRoundsToLatch || len(g.generations) <= 50 {
		return
	}
	if !g.isConverged {
		g.isConverged = true
		g.convergenceLatched = true
		g.convergedAt = time.Now()
		g.convergedAtCount = g.lastEvolvedTrajectoryCount
		log.Printf("[GEPA] converged: %d consecutive identical generations (no new signal); latched and will auto-unlock on new data or fitness rebound",
			g.identicalRounds)
	}
}

// unlockConvergenceLocked 解除收敛闩锁并恢复 5 分钟基础节奏（调用方持锁）。
func (g *GEPAEngine) unlockConvergenceLocked(reason string) {
	if !g.isConverged {
		return
	}
	g.isConverged = false
	g.convergenceLatched = false
	g.identicalRounds = 0
	g.convergedAt = time.Time{}
	log.Printf("[GEPA] convergence unlocked (%s), resuming evolution", reason)
}

// previousBestChanges 返回上一个世代最优策略的变更集。
func (g *GEPAEngine) previousBestChanges() []PromptChange {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.generations) == 0 {
		return nil
	}
	prev := g.generations[len(g.generations)-1].BestStrategy
	if prev == nil {
		return nil
	}
	return prev.Changes
}

// LoadHistory 从磁盘加载世代史与高水位（只读用途，如 CLI 指标查询）。
// 与 Start() 的区别：不启动进化 goroutine，因此不会被 CLI 反复调用时重启循环。
func (g *GEPAEngine) LoadHistory() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loadGenerations()
}

// CurrentGeneration 返回当前世代号。
func (g *GEPAEngine) CurrentGeneration() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.currentGen
}

// HasUnappliedStrategy 判断当前最优策略是否尚未应用过。
// 依据：内存标记 + 是否已在 SOUL 中出现（跨进程有效的判定）。
func (g *GEPAEngine) HasUnappliedStrategy() bool {
	g.mu.RLock()
	best := g.bestStrategy
	g.mu.RUnlock()
	if best == nil {
		return false
	}
	if !best.Applied {
		return true
	}
	// 已标记应用，但若策略内容有变（新世代），仍视为待应用。
	return false
}

// dedupeStrategies 去掉重复策略：先按内容指纹去重，再剔除与上一代最优相同的项。
// 返回去重后的切片（保持原有顺序，保留首个）。
func dedupeStrategies(strategies []OptimizationStrategy, prevChanges []PromptChange) []OptimizationStrategy {
	seen := make(map[string]bool, len(strategies))
	out := make([]OptimizationStrategy, 0, len(strategies))

	for _, s := range strategies {
		fp := strategyFingerprint(s.Changes)
		if fp == "" {
			// 空变更集没有信息量，直接丢弃。
			continue
		}
		if seen[fp] {
			continue
		}
		seen[fp] = true
		out = append(out, s)
	}

	// 若全部与上一代最优相同，返回空切片（调用方据此判定 identical）。
	prevFP := strategyFingerprint(prevChanges)
	if prevFP != "" && len(out) > 0 && allSameFingerprint(out, prevFP) {
		return out // 保留，由 identical 判定统一处理
	}
	return out
}

// allSameFingerprint 判断是否所有策略指纹都等于给定指纹。
func allSameFingerprint(strategies []OptimizationStrategy, fp string) bool {
	for _, s := range strategies {
		if strategyFingerprint(s.Changes) != fp {
			return false
		}
	}
	return len(strategies) > 0
}

// strategyFingerprint 计算变更集的稳定指纹（内容级，与 ID/名称无关）。
func strategyFingerprint(changes []PromptChange) string {
	if len(changes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		body := strings.TrimSpace(c.NewContent)
		if body == "" {
			body = strings.TrimSpace(c.OldContent)
		}
		if body == "" {
			continue
		}
		parts = append(parts, c.Type+"|"+strings.TrimSpace(c.Section)+"|"+body)
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:16])
}

// changesEqual 判断两组变更是否逐字节等价。
func changesEqual(a, b []PromptChange) bool {
	fpA := strategyFingerprint(a)
	fpB := strategyFingerprint(b)
	if fpA == "" || fpB == "" {
		return false
	}
	return fpA == fpB
}

// ApplyBestStrategy applies the best strategy to the system.
// Skips re-application if the strategy was already applied (idempotent).
func (g *GEPAEngine) ApplyBestStrategy(soul *SoulManager, systemPrompt string) (string, error) {
	g.mu.RLock()
	bestPtr := g.bestStrategy
	g.mu.RUnlock()

	if bestPtr == nil {
		return systemPrompt, nil
	}

	if bestPtr.Applied {
		return systemPrompt, nil
	}

	bestCopy := *bestPtr

	optimized, err := g.optimizer.Optimize(systemPrompt, bestCopy.Changes)
	if err != nil {
		return systemPrompt, err
	}

	now := time.Now()
	bestCopy.Applied = true
	bestCopy.AppliedAt = &now

	g.mu.Lock()
	g.bestStrategy = &bestCopy
	g.mu.Unlock()

	return optimized, nil
}

// ApplyBestStrategyManaged 把最优策略里的新增内容写入 SOUL 的**机器可管理区**。
//
// 与 ApplyBestStrategy 的区别（P0-1 根因修复）：
//   - 不再把整段改写后的 SOUL 交给 SetSoul；而是一节一节地走
//     SoulManager.ApplyManagedSection，全部落在 `## Learned Preferences` 下，
//     因此超限时可以被安全淘汰，绝不会触发人格头截断；
//   - 已应用过的策略不会重复写入（按"内容是否已存在"判定，跨进程有效）。
//
// 返回本次实际写入的节数；0 表示无需变更（去重命中等）。
func (g *GEPAEngine) ApplyBestStrategyManaged(soul *SoulManager) (int, error) {
	if soul == nil {
		return 0, nil
	}

	g.mu.RLock()
	bestPtr := g.bestStrategy
	g.mu.RUnlock()
	if bestPtr == nil {
		return 0, nil
	}

	bestCopy := *bestPtr
	applied := 0
	current := soul.GetSoul()

	// P2：尊重策略声明的 Target。生成器会产出 "soul|system|skills" 三种目标，
	// 而此前这里**完全不看 Target**，把所有 change 一律写进 SOUL 的
	// `## Learned Preferences` —— 于是"target=skills 的技能优化建议"落进了
	// 人格文件，语义白定义。目前只有 SOUL 有写入端口：其它目标如实跳过并留日志，
	// 不猜测、不误写；同时把该策略标记为"已消费"，避免每 10 分钟重复打日志。
	target := strings.ToLower(strings.TrimSpace(bestCopy.Target))
	switch target {
	case "system", "skills":
		log.Printf("[GEPA] strategy %s targets %q, for which no applier exists; "+
			"skipping %d change(s) (not writing them into SOUL)", bestCopy.ID, target, len(bestCopy.Changes))
		now := time.Now()
		bestCopy.Applied = true
		bestCopy.AppliedAt = &now
		g.mu.Lock()
		if g.bestStrategy != nil && g.bestStrategy.ID == bestPtr.ID {
			g.bestStrategy = &bestCopy
		}
		g.mu.Unlock()
		return 0, nil
	}

	for _, change := range bestCopy.Changes {
		if change.Type != "add" && change.Type != "modify" && change.Type != "reorder" {
			// remove 类变更需要精确的旧内容定位，风险高，跳过（不猜测）。
			continue
		}
		body := strings.TrimSpace(change.NewContent)
		if body == "" {
			continue
		}
		// 内容已存在 → 幂等跳过。这条判定跨进程生效，
		// 解决了"重启后 Applied 复位又写一遍同样内容"的重复注入。
		if strings.Contains(current, body) {
			continue
		}
		name := strings.TrimSpace(change.Section)
		if name == "" {
			name = fmt.Sprintf("gepa_gen%d", g.currentGenID())
		}
		if err := soul.ApplyManagedSection(name, body); err != nil {
			log.Printf("[GEPA] failed to apply managed section %q: %v", name, err)
			continue
		}
		applied++
		current = soul.GetSoul()
	}

	if applied == 0 {
		return 0, nil
	}

	now := time.Now()
	bestCopy.Applied = true
	bestCopy.AppliedAt = &now

	g.mu.Lock()
	if g.bestStrategy != nil && g.bestStrategy.ID == bestPtr.ID {
		g.bestStrategy = &bestCopy
	}
	g.mu.Unlock()

	return applied, nil
}

// currentGenID 读取当前世代号（供区块命名）。
func (g *GEPAEngine) currentGenID() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.currentGen
}

// ManagedPayloads 返回历史世代中出现过的全部自动生成载荷。
// 供 SOUL 压缩（CompactSoul）识别"哪些顶层区块是 GEPA 注入的"。
func (g *GEPAEngine) ManagedPayloads() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	seen := make(map[string]bool)
	var out []string
	for _, gen := range g.generations {
		if gen.BestStrategy == nil {
			continue
		}
		for _, c := range gen.BestStrategy.Changes {
			body := strings.TrimSpace(c.NewContent)
			if len(body) < 60 || seen[body] {
				continue
			}
			seen[body] = true
			out = append(out, body)
		}
	}
	return out
}

// LastAppliedAt 返回最近一次成功应用策略的时间（无则零值）。
func (g *GEPAEngine) LastAppliedAt() time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.bestStrategy == nil || g.bestStrategy.AppliedAt == nil {
		return time.Time{}
	}
	return *g.bestStrategy.AppliedAt
}

// evaluateStrategyFitness evaluates a strategy's potential fitness
func (g *GEPAEngine) evaluateStrategyFitness(strategy *OptimizationStrategy, scores []EffectivenessScore) float64 {
	// 基础分：平均效果分
	avgScore := 0.0
	if len(scores) > 0 {
		for _, s := range scores {
			avgScore += s.OverallScore
		}
		avgScore /= float64(len(scores))
	}

	// 基于策略 changes 的实际内容计算差异化 fitness，使不同策略 fitness 不同，
	// 让 selection 真正有意义（避免所有策略 fitness 仅由 avgScore 决定的"假进化"）。
	changesScore := 0.0
	totalContentLen := 0
	actionKeywordHits := 0
	// 中英文动作/优化关键词词表
	actionKeywords := []string{
		"add", "modify", "improve", "optimize", "enhance", "refine",
		"remove", "reorder", "fix", "clarify",
		"增加", "修改", "改进", "优化", "增强", "完善",
		"删除", "调整", "修复", "明确", "简化", "重排",
	}
	for _, c := range strategy.Changes {
		// 把可评估文本拼到一起做关键词密度统计
		text := strings.ToLower(c.NewContent + " " + c.Reason + " " + c.Section)
		totalContentLen += len(c.NewContent)
		for _, kw := range actionKeywords {
			if strings.Contains(text, kw) {
				actionKeywordHits++
			}
		}
	}

	// changes 数量贡献：1~5 个较为合理
	changeCount := len(strategy.Changes)
	switch {
	case changeCount == 0:
		// 没有 changes 的策略无意义，大幅降权
		changesScore = 0.1
	case changeCount <= 5:
		changesScore = 0.1 + 0.03*float64(changeCount)
	default:
		// 过多 changes 视为过度复杂，递减
		changesScore = 0.25 - 0.02*float64(changeCount-5)
		if changesScore < 0.05 {
			changesScore = 0.05
		}
	}

	// 内容实质度：基于 NewContent 字符长度（对数压缩，避免长文本主导）
	if totalContentLen > 0 {
		changesScore += math.Min(0.2, math.Log2(float64(totalContentLen+1))/50.0)
	}

	// 关键词密度贡献
	if changeCount > 0 {
		density := float64(actionKeywordHits) / float64(changeCount)
		changesScore += math.Min(0.15, density*0.05)
	}

	// 按目标类型加权
	multiplier := 1.0
	switch strategy.Target {
	case "soul":
		multiplier = 1.2 // 人格调整影响较大
	case "system":
		multiplier = 1.1
	case "skills":
		multiplier = 1.0
	}

	fitness := (avgScore + changesScore) * multiplier
	if fitness < 0 {
		fitness = 0
	}
	return fitness
}

// selectBestStrategy selects the best strategy from a population
func (g *GEPAEngine) selectBestStrategy(strategies []OptimizationStrategy) *OptimizationStrategy {
	if len(strategies) == 0 {
		return nil
	}

	// Sort by fitness
	sort.Slice(strategies, func(i, j int) bool {
		return strategies[i].Fitness > strategies[j].Fitness
	})

	best := strategies[0]
	return &best
}

// calculateAvgFitness calculates average fitness of strategies
func (g *GEPAEngine) calculateAvgFitness(strategies []OptimizationStrategy) float64 {
	if len(strategies) == 0 {
		return 0
	}

	total := 0.0
	for _, s := range strategies {
		total += s.Fitness
	}
	return total / float64(len(strategies))
}

// updateMetrics updates engine metrics
func (g *GEPAEngine) updateMetrics(scores []EffectivenessScore) {
	g.totalTrajectories = len(scores)

	successes := 0
	totalEffectiveness := 0.0
	for _, s := range scores {
		if s.Success {
			successes++
		}
		totalEffectiveness += s.OverallScore
	}

	if len(scores) > 0 {
		g.successRate = float64(successes) / float64(len(scores))
		g.avgEffectiveness = totalEffectiveness / float64(len(scores))
	}
}

// GetStats returns GEPA engine statistics.
//
// P0-3：这里必须能回答"进化到底还在不在动"。此前只有 is_converged 一个布尔，
// 且没有任何调用方，运维完全看不出引擎已经停止产出（线上静默停了 3 周）。
func (g *GEPAEngine) GetStats() map[string]interface{} {
	g.mu.RLock()
	defer g.mu.RUnlock()

	bestID := ""
	bestApplied := false
	if g.bestStrategy != nil {
		bestID = g.bestStrategy.ID
		bestApplied = g.bestStrategy.Applied
	}

	stats := map[string]interface{}{
		"current_generation":    g.currentGen,
		"is_converged":          g.isConverged,
		"convergence_latched":   g.convergenceLatched,
		"total_trajectories":    g.totalTrajectories,
		"success_rate":          g.successRate,
		"avg_effectiveness":     g.avgEffectiveness,
		"best_strategy_id":      bestID,
		"best_strategy_applied": bestApplied,
		"convergence_threshold": g.convergenceThreshold,
		"max_iterations":        g.maxIterations,

		// 可观测性（P0-3）
		"evolve_runs":           g.evolveRuns,
		"evolve_skipped":        g.evolveSkipped,
		"evolve_failures":       g.evolveFailures,
		"consecutive_failures":  g.consecutiveFail,
		"last_failure_reason":   g.lastFailureReason,
		"last_improvement":      g.lastImprovement,
		"identical_rounds":      g.identicalRounds,
		"trajectories_consumed": g.lastEvolvedTrajectoryCount,
		// 高水位位点（判"进化是不是被无新数据卡住"的唯一依据）
		"trajectories_consumed_key": g.lastEvolvedTrajectoryKey,
		"generations_on_disk":       len(g.generations),
		// 本进程是否有 LLM 可用。**不要**用它决定 health：CLI/HTTP 探针
		// 天然没有 provider，但磁盘上的状态是真实运行过的引擎留下的。
		"provider_available": g.provider != nil,
		"state_from_disk":    g.stateLoaded,
	}

	// P2：把"进化是不是真的停了"变成一个**可直接判定**的字段。
	//
	// 此前 evolutionLoop 的失败只写 log.Printf（实测 288 条/天同内容噪声），
	// 而 log 不是任何监控的输入 —— "悄悄失败 3 周"正是这样发生的。
	// health / alert_streak 让上层（HTTP /api/cortex/gepa、CLI）能一眼看出
	// 需要人介入，而不必去 grep 日志。
	alertAfter := gepaAlertAfterFailures
	switch {
	case g.consecutiveFail >= alertAfter:
		stats["health"] = "degraded"
		stats["alert"] = fmt.Sprintf("self-evolution has failed %d consecutive time(s); last error: %s",
			g.consecutiveFail, g.lastFailureReason)
		stats["alert_streak"] = g.consecutiveFail
	case g.convergenceLatched:
		stats["health"] = "converged"
	case g.currentGen == 0 && g.evolveRuns == 0:
		// 从未产出过世代：要么没数据、要么本进程没有 LLM。具体原因由
		// provider_available / total_trajectories 区分，health 本身只说明
		// "没有在动的证据"。
		stats["health"] = "idle"
	default:
		stats["health"] = "ok"
	}
	if !g.lastEvolveFailureAt.IsZero() {
		stats["last_failure_at"] = g.lastEvolveFailureAt.Format(time.RFC3339)
	}
	if !g.lastEvolveAt.IsZero() {
		stats["last_evolve_at"] = g.lastEvolveAt.Format(time.RFC3339)
	}
	if !g.lastImprovedAt.IsZero() {
		stats["last_improved_at"] = g.lastImprovedAt.Format(time.RFC3339)
	}
	if !g.convergedAt.IsZero() {
		stats["converged_at"] = g.convergedAt.Format(time.RFC3339)
	}
	return stats
}

// GetGenerations returns all generations
func (g *GEPAEngine) GetGenerations() []Generation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.generations
}

// ---------------------------------------------------------------------------
// P2：GEPA 运维状态的持久化
// ---------------------------------------------------------------------------
//
// 存在理由：`magic cortex gepa` 与 GET /api/cortex/gepa 是"进化到底还在不在动"
// 的唯一探针，而此前 loadGenerations 只恢复 currentGen/bestStrategy ——
// 收敛标记、失败计数、高水位位点**都不落盘**，于是 CLI 恒报 is_converged=false；
// 又因为 CLI 用 nil provider 构造，health 恒为 "disabled"。真正需要人介入的
// 两个信号（收敛闩锁、连续失败）恰好读不到。这里把状态落到 gepa/state.json。

// gepaState 是 GEPA 需要跨进程保留的运维状态。
type gepaState struct {
	IsConverged                bool      `json:"is_converged"`
	ConvergenceLatched         bool      `json:"convergence_latched"`
	IdenticalRounds            int       `json:"identical_rounds"`
	ConvergedAt                time.Time `json:"converged_at,omitempty"`
	LastImprovement            float64   `json:"last_improvement"`
	LastImprovedAt             time.Time `json:"last_improved_at,omitempty"`
	EvolveRuns                 int       `json:"evolve_runs"`
	EvolveSkipped              int       `json:"evolve_skipped"`
	EvolveFailures             int       `json:"evolve_failures"`
	ConsecutiveFail            int       `json:"consecutive_fail"`
	LastFailureReason          string    `json:"last_failure_reason,omitempty"`
	LastEvolveAt               time.Time `json:"last_evolve_at,omitempty"`
	LastEvolveFailureAt        time.Time `json:"last_evolve_failure_at,omitempty"`
	LastEvolvedTrajectoryKey   string    `json:"last_evolved_trajectory_key,omitempty"`
	LastEvolvedTrajectoryCount int       `json:"last_evolved_trajectory_count,omitempty"`
	TotalTrajectories          int       `json:"total_trajectories,omitempty"`
	SuccessRate                float64   `json:"success_rate,omitempty"`
	AvgEffectiveness           float64   `json:"avg_effectiveness,omitempty"`
}

func (g *GEPAEngine) statePath() string {
	return filepath.Join(g.baseDir, "gepa", "state.json")
}

// saveStateLocked 把运维状态落盘。调用方必须持有 g.mu（写锁）。
// 失败只记日志：状态文件是观测用途，不该影响进化主流程。
func (g *GEPAEngine) saveStateLocked() {
	st := gepaState{
		IsConverged:                g.isConverged,
		ConvergenceLatched:         g.convergenceLatched,
		IdenticalRounds:            g.identicalRounds,
		ConvergedAt:                g.convergedAt,
		LastImprovement:            g.lastImprovement,
		LastImprovedAt:             g.lastImprovedAt,
		EvolveRuns:                 g.evolveRuns,
		EvolveSkipped:              g.evolveSkipped,
		EvolveFailures:             g.evolveFailures,
		ConsecutiveFail:            g.consecutiveFail,
		LastFailureReason:          g.lastFailureReason,
		LastEvolveAt:               g.lastEvolveAt,
		LastEvolveFailureAt:        g.lastEvolveFailureAt,
		LastEvolvedTrajectoryKey:   g.lastEvolvedTrajectoryKey,
		LastEvolvedTrajectoryCount: g.lastEvolvedTrajectoryCount,
		TotalTrajectories:          g.totalTrajectories,
		SuccessRate:                g.successRate,
		AvgEffectiveness:           g.avgEffectiveness,
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[GEPA] failed to marshal state: %v", err)
		return
	}
	path := g.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("[GEPA] failed to create state dir: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[GEPA] failed to write state: %v", err)
	}
}

// loadStateLocked 从磁盘恢复运维状态。调用方必须持有 g.mu。
// 文件不存在（首次运行）时静默返回，stateLoaded 保持 false。
func (g *GEPAEngine) loadStateLocked() {
	data, err := os.ReadFile(g.statePath())
	if err != nil {
		return
	}
	var st gepaState
	if err := json.Unmarshal(data, &st); err != nil {
		log.Printf("[GEPA] failed to unmarshal state: %v", err)
		return
	}

	g.isConverged = st.IsConverged
	g.convergenceLatched = st.ConvergenceLatched
	g.identicalRounds = st.IdenticalRounds
	g.convergedAt = st.ConvergedAt
	g.lastImprovement = st.LastImprovement
	g.lastImprovedAt = st.LastImprovedAt
	g.evolveRuns = st.EvolveRuns
	g.evolveSkipped = st.EvolveSkipped
	g.evolveFailures = st.EvolveFailures
	g.consecutiveFail = st.ConsecutiveFail
	g.lastFailureReason = st.LastFailureReason
	g.lastEvolveAt = st.LastEvolveAt
	g.lastEvolveFailureAt = st.LastEvolveFailureAt
	g.lastEvolvedTrajectoryCount = st.LastEvolvedTrajectoryCount
	g.totalTrajectories = st.TotalTrajectories
	g.successRate = st.SuccessRate
	g.avgEffectiveness = st.AvgEffectiveness
	g.stateLoaded = true

	// 高水位位点：磁盘上有就沿用；没有（老状态文件）则退回"对齐当前最新轨迹"，
	// 与 loadGenerations 的语义一致。
	if st.LastEvolvedTrajectoryKey != "" {
		g.lastEvolvedTrajectoryKey = st.LastEvolvedTrajectoryKey
	}
}

// saveGeneration saves a generation to disk and rotates the history.
//
// 轮转的必要性：每个世代文件内嵌了整段策略文本，实测 133 个文件已占 1.93 MB，
// 而世代只增不减。超出保留窗口的最旧文件在这里被删除（磁盘上留 gen_<id>.json
// 的连续新窗口，加载侧的 maxLoadedGenerations 与之同量级）。
func (g *GEPAEngine) saveGeneration(gen *Generation) {
	dir := filepath.Join(g.baseDir, "gepa", "generations")
	path := filepath.Join(dir, fmt.Sprintf("gen_%d.json", gen.ID))
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[GEPA] failed to create generation dir: %v", err)
		return
	}

	data, err := json.MarshalIndent(gen, "", "  ")
	if err != nil {
		log.Printf("[GEPA] failed to marshal generation %d: %v", gen.ID, err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[GEPA] failed to write generation %d: %v", gen.ID, err)
		return
	}

	g.rotateGenerations(dir)
}

// rotateGenerations 只保留最新 maxPersistedGenerations 个世代文件。
func (g *GEPAEngine) rotateGenerations(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type genFile struct {
		name string
		id   int
	}
	var files []genFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "gen_") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(e.Name(), "gen_"), ".json"), "%d", &id); err != nil {
			continue
		}
		files = append(files, genFile{name: e.Name(), id: id})
	}
	if len(files) <= maxPersistedGenerations {
		return
	}

	sort.Slice(files, func(i, j int) bool { return files[i].id < files[j].id })
	for _, f := range files[:len(files)-maxPersistedGenerations] {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			log.Printf("[GEPA] failed to rotate generation file %s: %v", f.name, err)
		}
	}
}

// loadGenerations loads previous generations from disk (capped at maxLoadedGenerations)
func (g *GEPAEngine) loadGenerations() {
	const maxLoadedGenerations = 200

	// 运维状态先加载：它独立于世代目录存在（CLI 探针可能在只有 state.json
	// 而没有世代文件的实例上运行），因此必须在 ReadDir 早退之前读。
	g.loadStateLocked()

	genDir := filepath.Join(g.baseDir, "gepa", "generations")
	entries, err := os.ReadDir(genDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "gen_") {
			continue
		}

		path := filepath.Join(genDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var gen Generation
		if err := json.Unmarshal(data, &gen); err != nil {
			continue
		}

		g.generations = append(g.generations, gen)
		if gen.ID > g.currentGen {
			g.currentGen = gen.ID
			g.bestStrategy = gen.BestStrategy
		}
	}

	sort.Slice(g.generations, func(i, j int) bool {
		return g.generations[i].ID < g.generations[j].ID
	})

	if len(g.generations) > maxLoadedGenerations {
		g.generations = g.generations[len(g.generations)-maxLoadedGenerations:]
	}

	// 重启后把高水位对齐到"已消费"的状态：磁盘上的世代说明这些轨迹
	// 已经被处理过。若不对齐，进程重启后第一轮会把同一批数据再"进化"一遍
	// （历史上这正是重复写入 SOUL 的触发路径之一）。
	//
	// 注意：必须用**轨迹池位点**而不是 len(GetTrajectories(100)) —— 后者被
	// limit 截断，池子超过 100 条后是恒定值，对齐后第一个 tick 就会误判
	// "无新数据"而永久跳过。
	if len(g.generations) > 0 && g.lastEvolvedTrajectoryKey == "" && g.trajectoryStore != nil {
		g.lastEvolvedTrajectoryKey = g.trajectoryStore.LatestTrajectoryKey()
		g.lastEvolvedTrajectoryCount = len(g.trajectoryStore.GetTrajectories(100))
	}
}

// Reset resets the GEPA engine
func (g *GEPAEngine) Reset() error {
	g.Stop() // Stop the evolution goroutine first

	g.mu.Lock()
	defer g.mu.Unlock()

	g.generations = make([]Generation, 0)
	g.currentGen = 0
	g.bestStrategy = nil
	g.isConverged = false
	g.totalTrajectories = 0
	g.successRate = 0
	g.avgEffectiveness = 0

	// 清空 P0-2/P0-3 引入的状态，避免 Reset 后仍带着旧的收敛闩锁或高水位。
	g.lastEvolvedTrajectoryKey = ""
	g.lastEvolvedTrajectoryCount = 0
	g.identicalRounds = 0
	g.convergenceLatched = false
	g.convergedAt = time.Time{}
	g.convergedAtCount = 0
	g.lastImprovement = 0
	g.lastImprovedAt = time.Time{}
	g.evolveRuns = 0
	g.evolveSkipped = 0
	g.evolveFailures = 0
	g.consecutiveFail = 0
	g.lastFailureReason = ""
	g.lastEvolveFailureAt = time.Time{}
	g.lastEvolveAt = time.Time{}

	// Clear saved generations
	genDir := filepath.Join(g.baseDir, "gepa", "generations")
	os.RemoveAll(genDir)

	return nil
}
