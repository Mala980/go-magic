package skills

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/magicwubiao/go-magic/pkg/config"
)

// Pattern represents a detected usage pattern
type Pattern struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Tools        []string `json:"tools"`         // Tools used in order
	ToolSequence []string `json:"tool_sequence"` // Ordered sequence of tool calls
	Frequency    int      `json:"frequency"`     // How many times observed
	Confidence   float64  `json:"confidence"`    // Pattern quality score
	ExampleTasks []string `json:"example_tasks"` // Where this pattern was seen
}

// EnhancedAutoCreator builds on the basic auto-creator with pattern mining
// This is the core of System 6: Skill Self-Evolution
type EnhancedAutoCreator struct {
	baseDir      string
	patterns     []Pattern
	skillCount   atomic.Int64
	minFrequency int      // Minimum occurrences to generate skill
	manager      *Manager // optional: register generated skills
	mu           sync.Mutex
}

// DefaultAutoSkillMinFrequency 是"模式出现多少次才允许自动建技能"的默认阈值。
//
// P1-3 数据一致化：此前该阈值散落在三处且默认值互相矛盾 ——
// 构造器硬编码 5，cortex.IntegrationConfig / server / pkg/config 都默认 3，
// 而 cortex 只在 `SkillMinPatternFreq > 0` 时才下推（见 integration.go），
// 于是"配置说 3、实际生效 5"。这里指定唯一默认值，所有默认来源引用它。
const DefaultAutoSkillMinFrequency = config.DefaultAutoSkillMinFrequency

// MinPatternFreqOrDefault 把 0/负数（未配置）归一为默认阈值。
func MinPatternFreqOrDefault(freq int) int {
	if freq <= 0 {
		return DefaultAutoSkillMinFrequency
	}
	return freq
}

// NewEnhancedAutoCreator creates an enhanced skill auto-creator
func NewEnhancedAutoCreator(baseDir string) *EnhancedAutoCreator {
	skillsDir := filepath.Join(baseDir, "auto_skills")
	os.MkdirAll(skillsDir, 0755)
	// 四态子目录：pending / approved / archived / rejected（口径见 autoSkillStateDirs）
	for _, state := range autoSkillStateDirs {
		os.MkdirAll(filepath.Join(skillsDir, state), 0755)
	}

	patternsFile := filepath.Join(skillsDir, "patterns.json")
	var patterns []Pattern
	if data, err := os.ReadFile(patternsFile); err == nil {
		json.Unmarshal(data, &patterns)
	}
	// 清理历史遗留的低质量模式：丢弃只含单一工具的平凡序列，并对示例任务
	// 去重、封顶，避免 patterns.json 长期累积重复噪声数据。
	patterns = prunePatterns(patterns)

	return &EnhancedAutoCreator{
		baseDir:  skillsDir,
		patterns: patterns,
		// P1-3：默认阈值统一走 DefaultAutoSkillMinFrequency（此前硬编码 5 与
		// 配置默认 3 冲突，见该常量注释）。
		minFrequency: DefaultAutoSkillMinFrequency,
	}
}

// SetManager binds a skills Manager so generated skills get registered
func (e *EnhancedAutoCreator) SetManager(mgr *Manager) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.manager = mgr
}

// SetBaseDir 更新自动技能根目录（供 cortex 绑定 Manager 时同步路径，
// 避免 SkillCreator 与 Manager 各自维护不同的 auto_skills 路径导致重启后丢失已批准技能）。
// 会确保 pending/approved/archived 子目录存在，并迁移已有的 patterns.json。
func (e *EnhancedAutoCreator) SetBaseDir(dir string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	oldBase := e.baseDir
	e.baseDir = dir
	// 确保四态子目录存在（口径见 autoSkillStateDirs）
	for _, state := range autoSkillStateDirs {
		os.MkdirAll(filepath.Join(dir, state), 0755)
	}
	// 若新目录下没有 patterns.json 但旧目录有，则迁移过去
	newPatternsFile := filepath.Join(dir, "patterns.json")
	oldPatternsFile := filepath.Join(oldBase, "patterns.json")
	if _, err := os.Stat(newPatternsFile); os.IsNotExist(err) {
		if data, err := os.ReadFile(oldPatternsFile); err == nil {
			os.WriteFile(newPatternsFile, data, 0644)
		}
	}
}

// SavePatterns persists patterns to disk
func (e *EnhancedAutoCreator) SavePatterns() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.savePatternsLocked()
}

// savePatternsLocked 持锁保存 patterns（调用者需持有 e.mu）
func (e *EnhancedAutoCreator) savePatternsLocked() error {
	if len(e.patterns) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(e.patterns, "", "  ")
	if err != nil {
		return err
	}
	patternsFile := filepath.Join(e.baseDir, "patterns.json")
	return os.WriteFile(patternsFile, data, 0644)
}

// AnalyzeToolSequence analyzes a sequence of tool calls for patterns
func (e *EnhancedAutoCreator) AnalyzeToolSequence(task string, tools []string) {
	if len(tools) < 3 {
		return // Need at least 3 tools to form a meaningful pattern
	}

	e.mu.Lock()

	// Look for subsequences that repeat across tasks
	for i := 0; i < len(tools)-2; i++ {
		subsequence := tools[i : i+3]

		// 跳过平凡子序列：模式必须涉及至少 2 种不同工具才具备可复用的技能
		// 价值。形如 "execute_command → execute_command → execute_command"
		// 的序列只是“连续执行了几条命令”，几乎匹配任何多步任务，记录下来
		// 只会产生噪声模式。
		if !hasAtLeastTwoDistinctTools(subsequence) {
			continue
		}

		// Check if we've seen this pattern before
		patternKey := strings.Join(subsequence, " → ")
		found := false

		for pIdx := range e.patterns {
			existingKey := strings.Join(e.patterns[pIdx].ToolSequence, " → ")
			if existingKey == patternKey {
				e.patterns[pIdx].Frequency++
				// Update confidence based on frequency
				e.patterns[pIdx].Confidence = 0.5 + float64(e.patterns[pIdx].Frequency)*0.1
				if e.patterns[pIdx].Confidence > 0.95 {
					e.patterns[pIdx].Confidence = 0.95
				}
				addExampleTask(&e.patterns[pIdx], task)
				found = true
				break
			}
		}

		if !found {
			// New pattern detected
			e.patterns = append(e.patterns, Pattern{
				Name:         fmt.Sprintf("Pattern-%d", len(e.patterns)+1),
				Description:  fmt.Sprintf("Detected sequence: %s", patternKey),
				Tools:        subsequence,
				ToolSequence: subsequence,
				Frequency:    1,
				Confidence:   0.5,
				ExampleTasks: []string{task},
			})
		}
	}

	// 持锁保存 patterns 到磁盘
	if err := e.savePatternsLocked(); err != nil {
		log.Printf("[SkillCreator] failed to save patterns: %v", err)
	}

	// 拷贝一份 patterns 快照用于后续生成检查，避免长时间持锁做 IO
	snapshot := make([]Pattern, len(e.patterns))
	copy(snapshot, e.patterns)
	e.mu.Unlock()

	// Check if any pattern is ready for skill generation（基于快照，不持锁）
	e.checkAndGenerateSkillsFromSnapshot(snapshot)
}

// maxExampleTasksPerPattern 限制每个模式保存的示例任务数量，避免示例列表
// 随着调用次数无限增长而充斥重复描述。
const maxExampleTasksPerPattern = 10

// hasAtLeastTwoDistinctTools 判断工具序列是否涉及至少 2 种不同工具。只含单一
// 工具的平凡序列不构成可复用技能，应在模式检测时跳过。
func hasAtLeastTwoDistinctTools(seq []string) bool {
	seen := make(map[string]struct{}, len(seq))
	for _, t := range seq {
		seen[t] = struct{}{}
	}
	return len(seen) >= 2
}

// addExampleTask 向模式的示例任务列表追加一条任务，自动跳过重复项，并在达到
// 上限时丢弃最旧的示例，使列表成为近期不同任务的滚动窗口。
func addExampleTask(p *Pattern, task string) {
	if task == "" {
		return
	}
	for _, existing := range p.ExampleTasks {
		if existing == task {
			return // 已记录过相同任务，跳过
		}
	}
	if len(p.ExampleTasks) >= maxExampleTasksPerPattern {
		p.ExampleTasks = p.ExampleTasks[1:]
	}
	p.ExampleTasks = append(p.ExampleTasks, task)
}

// prunePatterns 清理已加载的模式列表：丢弃只含单一工具的平凡序列，并对示例
// 任务去重、封顶，用于在加载历史 patterns.json 时回收之前累积的噪声数据。
func prunePatterns(patterns []Pattern) []Pattern {
	pruned := make([]Pattern, 0, len(patterns))
	for _, p := range patterns {
		if !hasAtLeastTwoDistinctTools(p.ToolSequence) {
			continue // 丢弃平凡单工具模式
		}
		p.ExampleTasks = dedupeExampleTasks(p.ExampleTasks)
		pruned = append(pruned, p)
	}
	return pruned
}

// dedupeExampleTasks 对示例任务去重并封顶到 maxExampleTasksPerPattern 条，
// 保留最近出现的示例。
func dedupeExampleTasks(tasks []string) []string {
	seen := make(map[string]struct{}, len(tasks))
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) > maxExampleTasksPerPattern {
		out = out[len(out)-maxExampleTasksPerPattern:]
	}
	return out
}

// CheckAndGenerateSkills checks patterns and generates skills for those meeting criteria
func (e *EnhancedAutoCreator) CheckAndGenerateSkills() {
	e.mu.Lock()
	snapshot := make([]Pattern, len(e.patterns))
	copy(snapshot, e.patterns)
	e.mu.Unlock()
	e.checkAndGenerateSkillsFromSnapshot(snapshot)
}

// checkAndGenerateSkillsFromSnapshot 基于快照检查并生成技能（不持锁，可安全做 IO）
func (e *EnhancedAutoCreator) checkAndGenerateSkillsFromSnapshot(patterns []Pattern) {
	for _, pattern := range patterns {
		// 提高置信度阈值也提高到 0.8，避免低质量的自动生成
		if pattern.Frequency >= e.minFrequency && pattern.Confidence >= 0.8 {
			// Check if skill already generated for this pattern
			// (look in pending, approved, archived, rejected)
			exists := false
			for _, subdir := range []string{"pending", "approved", "archived", "rejected"} {
				entries, _ := os.ReadDir(filepath.Join(e.baseDir, subdir))
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), fmt.Sprintf("auto-%s-", pattern.Name)) && entry.IsDir() {
						exists = true
						break
					}
				}
				if exists {
					break
				}
			}
			if !exists {
				log.Printf("[SkillCreator] Pattern '%s' meets criteria (freq=%d, conf=%.2f), generating skill...",
					pattern.Name, pattern.Frequency, pattern.Confidence)
				e.GenerateSkillFromPattern(pattern)
			}
		}
	}
}

// AnalyzeFullSession analyzes a complete session for skill generation
func (e *EnhancedAutoCreator) AnalyzeFullSession(sessionData map[string]interface{}) {
	// Extract task description
	taskDesc, _ := sessionData["task"].(string)

	// Extract tool sequence
	var toolSequence []string
	if tools, ok := sessionData["tool_sequence"].([]string); ok {
		toolSequence = tools
	}

	// Analyze for patterns - this will also check and generate skills
	e.AnalyzeToolSequence(taskDesc, toolSequence)
}

// GenerateSkillFromPattern generates a skill file from a detected pattern
// Newly generated skills go to auto_skills/pending/ and won't be used by the Agent
// until the user approves them (via CLI or Web UI).
func (e *EnhancedAutoCreator) GenerateSkillFromPattern(pattern Pattern) error {
	skillID := fmt.Sprintf("auto-%s-%d", pattern.Name, time.Now().Unix())
	skillName := fmt.Sprintf("Automated %s", pattern.Name)
	// 四态管理：新生成的技能放入 pending 目录
	skillDir := filepath.Join(e.baseDir, "pending", skillID)
	os.MkdirAll(skillDir, 0755)

	// Generate skill metadata
	skillMeta := map[string]interface{}{
		"id":            skillID,
		"name":          skillName,
		"description":   pattern.Description,
		"author":        "cortex-auto",
		"created_at":    time.Now().Format(time.RFC3339),
		"status":        string(SkillStatusPending),
		"pattern_tools": pattern.Tools,
		"frequency":     pattern.Frequency,
		"examples":      pattern.ExampleTasks,
	}

	metaJSON, _ := json.MarshalIndent(skillMeta, "", "  ")
	os.WriteFile(filepath.Join(skillDir, "meta.json"), metaJSON, 0644)

	// Generate SKILL.md - the main skill file using progressive disclosure
	skillMD := e.generateSkillMarkdown(pattern, skillMeta)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0644)

	// 原子自增：GenerateSkillFromPattern 的调用链（checkAndGenerateSkillsFromSnapshot）
	// 已经**出了 e.mu**，此前这里的 `e.skillCount++` 与 GetStats 的读取构成真实数据竞态。
	e.skillCount.Add(1)

	// Register with skills manager so it shows up in /api/skills with pending status
	// Note: pending status skills are NOT injected into Agent context until approved
	if e.manager != nil {
		e.manager.RegisterSkill(&Skill{
			SkillMeta: SkillMeta{
				Name:        skillName,
				Description: pattern.Description,
				Version:     "1.0.0",
				Author:      "cortex-auto",
				Tags:        []string{"auto-generated", "pending"},
				Source:      SkillSourceAuto,
				Status:      SkillStatusPending,
				InstalledAt: time.Now(),
			},
			Tools:   pattern.Tools,
			Content: skillMD,
			Dir:     skillDir,
		})
	}

	// Log skill generation
	logContent := fmt.Sprintf("[%s] Generated pending skill '%s' from pattern seen %d times (dir: %s)\n",
		time.Now().Format(time.RFC3339), pattern.Name, pattern.Frequency, skillDir)
	os.WriteFile(filepath.Join(e.baseDir, "generation.log"), []byte(logContent), 0644)

	return nil
}

// generateSkillMarkdown generates progressive disclosure markdown
func (e *EnhancedAutoCreator) generateSkillMarkdown(pattern Pattern, meta map[string]interface{}) string {
	var sb strings.Builder

	// Level 0: Header + brief description (disclosed first)
	sb.WriteString(fmt.Sprintf("# %s\n\n", meta["name"].(string)))
	sb.WriteString(fmt.Sprintf("**Auto-generated by Cortex Agent** | Pattern seen %d times\n\n", pattern.Frequency))
	sb.WriteString(fmt.Sprintf("## What this skill does\n\n%s\n\n", pattern.Description))

	// Level 1: Full instructions (disclosed when user actually uses the skill)
	sb.WriteString("---\n\n")
	sb.WriteString("## Level 1: Complete Usage Guide\n\n")
	sb.WriteString("### Tool Sequence\n\n")
	sb.WriteString("This skill uses the following tool sequence:\n\n")
	for i, tool := range pattern.ToolSequence {
		sb.WriteString(fmt.Sprintf("%d. `%s`\n", i+1, tool))
	}

	sb.WriteString("\n### How to Use\n\n")
	sb.WriteString("When you need to perform this task:\n")
	sb.WriteString("1. Call the first tool with appropriate parameters\n")
	sb.WriteString("2. Use results from previous step as input to next tool\n")
	sb.WriteString("3. Continue until the sequence is complete\n\n")

	sb.WriteString("### Example Tasks\n\n")
	for _, example := range pattern.ExampleTasks {
		sb.WriteString(fmt.Sprintf("- %s\n", example))
	}

	// Level 2: Reference material (disclosed for complex tasks)
	sb.WriteString("\n---\n\n")
	sb.WriteString("## Level 2: Reference and Tips\n\n")
	sb.WriteString("### Common Pitfalls\n\n")
	sb.WriteString("- Make sure each tool call completes successfully before proceeding\n")
	sb.WriteString("- Verify intermediate results before moving to complex operations\n")
	sb.WriteString("- If a step fails, consider alternative approaches or ask for clarification\n\n")

	sb.WriteString("### Optimization Tips\n\n")
	sb.WriteString("- Consider parallel execution for independent sub-tasks\n")
	sb.WriteString("- Cache intermediate results when appropriate\n")
	sb.WriteString("- Document any deviations from the standard pattern\n\n")

	sb.WriteString("\n---\n\n")
	sb.WriteString("*This skill was auto-generated by Cortex Agent's pattern recognition system.*\n")
	sb.WriteString("*It will be improved and refined as the pattern is used more frequently.*\n")

	return sb.String()
}

// GetPatterns returns all detected patterns（返回拷贝，避免外部修改影响内部状态）
func (e *EnhancedAutoCreator) GetPatterns() []Pattern {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Pattern, len(e.patterns))
	copy(out, e.patterns)
	return out
}

// GetGeneratedSkills returns all auto-generated skills.
//
// P1-3 数据一致化：技能实际写入 `<baseDir>/<状态>/auto-*`（四态子目录），
// 而此前这里只扫 baseDir 顶层。顶层只有 pending/approved/archived/rejected
// 四个目录，没有任何以 "auto-" 开头的条目 → 永远返回空。
// 后果：cortex 每轮会话末尾的 "Generated N new skills" 从不打印，
// 上游也无从得知自动建技能是否真的发生。
func (e *EnhancedAutoCreator) GetGeneratedSkills() []string {
	e.mu.Lock()
	baseDir := e.baseDir
	e.mu.Unlock()

	skills := []string{}
	for _, state := range autoSkillStateDirs {
		entries, err := os.ReadDir(filepath.Join(baseDir, state))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), "auto-") {
				skills = append(skills, entry.Name())
			}
		}
	}
	return skills
}

// autoSkillStateDirs 是四态目录名，唯一来源。NewEnhancedAutoCreator /
// SetBaseDir / GetGeneratedSkills 全部引用它，避免再出现"写 pending/、读顶层"
// 这类两侧目录口径不一致。
var autoSkillStateDirs = []string{"pending", "approved", "archived", "rejected"}

// GetStats returns statistics about pattern detection and skills.
//
// P1-3 数据一致化：`skills_generated` 改为以磁盘四态目录中的实际技能数为准，
// 而不是 e.skillCount。计数器的语义是"本进程内生成过几次"，进程重启即归零；
// 而 patterns.json / 技能目录是持久化的。两者混用会让面板在重启后显示
// "patterns_detected=2, skills_generated=0"，与磁盘上真实存在的技能自相矛盾。
func (e *EnhancedAutoCreator) GetStats() map[string]interface{} {
	e.mu.Lock()
	patternsCount := len(e.patterns)
	pendingCount := 0
	for _, p := range e.patterns {
		if p.Frequency < e.minFrequency {
			pendingCount++
		}
	}
	minFreq := e.minFrequency
	e.mu.Unlock()

	return map[string]interface{}{
		"patterns_detected": patternsCount,
		"skills_generated":  len(e.GetGeneratedSkills()),
		"min_frequency":     minFreq,
		"pending_patterns":  pendingCount,
		// 本进程内新建的技能数（易失，重启归零），保留供排查用。
		// 转成 int 输出：JSON 消费方与既有断言都按 int 口径比较。
		"skills_generated_this_run": int(e.skillCount.Load()),
	}
}

// SetMinFrequency sets the minimum frequency threshold for skill generation.
//
// P1-3：0/负数会归一为默认阈值。此前直接赋值，一旦上游传入 0（如配置缺省
// 被解析为 0），阈值就变成 0 —— 条件 `Frequency >= 0` 恒真，任何模式第一次
// 被观测到就会立刻生成技能，自动建技能直接退化成噪声生产线。
func (e *EnhancedAutoCreator) SetMinFrequency(freq int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.minFrequency = MinPatternFreqOrDefault(freq)
}

// GetMinFrequency 返回当前生效的最小模式频率阈值。
func (e *EnhancedAutoCreator) GetMinFrequency() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.minFrequency
}

// ExportPatterns exports all patterns for analysis
func (e *EnhancedAutoCreator) ExportPatterns(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	data, err := json.MarshalIndent(e.patterns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
