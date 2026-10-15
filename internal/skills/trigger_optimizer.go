package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/magicwubiao/go-magic/pkg/config"
	"github.com/magicwubiao/go-magic/pkg/log"
)

// TriggerCondition 触发条件
type TriggerCondition struct {
	Keywords      []string `json:"keywords"`       // 关键词触发
	Patterns      []string `json:"patterns"`       // 正则模式触发
	TaskTypes     []string `json:"task_types"`     // 任务类型触发
	MinConfidence float64  `json:"min_confidence"` // 最小置信度
	Priority      int      `json:"priority"`       // 优先级
}

// TriggerStats 触发统计
type TriggerStats struct {
	SkillName       string         `json:"skill_name"`
	TriggerHits     map[string]int `json:"trigger_hits"`     // 触发条件命中次数
	FalsePositives  map[string]int `json:"false_positives"`  // 误触发次数
	MissedTriggers  []string       `json:"missed_triggers"`  // 漏触发案例
	OptimalKeywords []string       `json:"optimal_keywords"` // 优化后的关键词
	LastOptimized   time.Time      `json:"last_optimized"`
}

// TriggerOptimizer 触发优化器
type TriggerOptimizer struct {
	stats   map[string]*TriggerStats
	effMgr  *EffectivenessManager
	statsDB string
	mu      sync.RWMutex
}

// NewTriggerOptimizer 创建触发优化器
func NewTriggerOptimizer(effMgr *EffectivenessManager, baseDir string) *TriggerOptimizer {
	if baseDir == "" {
		baseDir = config.GetMagicHome()
	}

	skillsDir := filepath.Join(baseDir, "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		// 如果创建失败，使用临时目录
		skillsDir = os.TempDir()
	}

	to := &TriggerOptimizer{
		stats:   make(map[string]*TriggerStats),
		effMgr:  effMgr,
		statsDB: filepath.Join(skillsDir, "trigger_stats.json"),
	}

	// 加载已有数据
	if err := to.loadStats(); err != nil && !os.IsNotExist(err) {
		// 忽略加载错误
	}

	return to
}

// RecordTriggerHit 记录触发事件
// skillName: 技能名称
// triggerType: 触发类型 (keyword/pattern/task_type)
// keyword: 具体触发的关键词或模式
// wasSuccessful: 是否成功触发（不是误触发）
func (to *TriggerOptimizer) RecordTriggerHit(skillName, triggerType, keyword string, wasSuccessful bool) {
	to.mu.Lock()
	defer to.mu.Unlock()

	stats, exists := to.stats[skillName]
	if !exists {
		stats = &TriggerStats{
			SkillName:      skillName,
			TriggerHits:    make(map[string]int),
			FalsePositives: make(map[string]int),
			MissedTriggers: []string{},
		}
		to.stats[skillName] = stats
	}

	triggerKey := fmt.Sprintf("%s:%s", triggerType, keyword)
	if wasSuccessful {
		stats.TriggerHits[triggerKey]++
	} else {
		stats.FalsePositives[triggerKey]++
	}

	// 保存（已持有写锁，必须用不解锁的版本；且同步写，避免 goroutine
	// 与后续持锁者抢锁、以及进程退出时丢数据）。
	if err := to.saveStatsLocked(); err != nil {
		log.Warnf("[skills] failed to save trigger stats: %v", err)
	}
}

// RecordMissedTrigger 记录漏触发案例
func (to *TriggerOptimizer) RecordMissedTrigger(skillName, input string) {
	to.mu.Lock()
	defer to.mu.Unlock()

	stats, exists := to.stats[skillName]
	if !exists {
		stats = &TriggerStats{
			SkillName:      skillName,
			TriggerHits:    make(map[string]int),
			FalsePositives: make(map[string]int),
			MissedTriggers: []string{},
		}
		to.stats[skillName] = stats
	}

	// 限制存储的漏触发案例数量
	if len(stats.MissedTriggers) >= 100 {
		stats.MissedTriggers = stats.MissedTriggers[1:]
	}
	stats.MissedTriggers = append(stats.MissedTriggers, input)

	// 保存（已持有写锁，见 RecordTriggerHit 中的说明）。
	if err := to.saveStatsLocked(); err != nil {
		log.Warnf("[skills] failed to save trigger stats: %v", err)
	}
}

// AnalyzeTriggerEffectiveness 分析触发效果
func (to *TriggerOptimizer) AnalyzeTriggerEffectiveness(skillName string) *TriggerStats {
	to.mu.RLock()
	defer to.mu.RUnlock()

	stats, exists := to.stats[skillName]
	if !exists {
		return nil
	}

	// 创建副本
	statsCopy := &TriggerStats{
		SkillName:       stats.SkillName,
		TriggerHits:     make(map[string]int),
		FalsePositives:  make(map[string]int),
		MissedTriggers:  append([]string{}, stats.MissedTriggers...),
		OptimalKeywords: append([]string{}, stats.OptimalKeywords...),
		LastOptimized:   stats.LastOptimized,
	}

	for k, v := range stats.TriggerHits {
		statsCopy.TriggerHits[k] = v
	}
	for k, v := range stats.FalsePositives {
		statsCopy.FalsePositives[k] = v
	}

	return statsCopy
}

// OptimizeTriggers 优化触发条件
func (to *TriggerOptimizer) OptimizeTriggers(skillName string) (*TriggerCondition, error) {
	to.mu.Lock()
	defer to.mu.Unlock()

	stats, exists := to.stats[skillName]
	if !exists {
		return nil, fmt.Errorf("no trigger stats found for skill: %s", skillName)
	}

	// 获取技能效果数据
	var successRate float64
	if to.effMgr != nil {
		skillStats := to.effMgr.GetSkillStatistics(skillName)
		if skillStats != nil {
			successRate = skillStats.SuccessRate
		}
	}

	// 分析关键词效果
	keywordStats := make(map[string]*struct {
		hits           int
		falsePositives int
		successRate    float64
	})

	for triggerKey, hits := range stats.TriggerHits {
		parts := strings.SplitN(triggerKey, ":", 2)
		if len(parts) != 2 {
			continue
		}
		triggerType, keyword := parts[0], parts[1]
		if triggerType != "keyword" {
			continue
		}

		fp := stats.FalsePositives[triggerKey]
		successRate := float64(hits) / float64(hits+fp)
		if hits+fp == 0 {
			successRate = 0
		}

		keywordStats[keyword] = &struct {
			hits           int
			falsePositives int
			successRate    float64
		}{
			hits:           hits,
			falsePositives: fp,
			successRate:    successRate,
		}
	}

	// 构建优化后的关键词列表
	var optimalKeywords []string
	var highPriorityKeywords []string
	var normalPriorityKeywords []string

	for keyword, ks := range keywordStats {
		// 移除误触发率 > 30% 的关键词
		falsePositiveRate := float64(ks.falsePositives) / float64(ks.hits+ks.falsePositives)
		if ks.hits+ks.falsePositives > 0 && falsePositiveRate > 0.3 {
			continue
		}

		if ks.successRate >= 0.8 && ks.hits >= 3 {
			highPriorityKeywords = append(highPriorityKeywords, keyword)
		} else if ks.successRate >= 0.5 {
			normalPriorityKeywords = append(normalPriorityKeywords, keyword)
		}
	}

	// 从漏触发案例中提取高频关键词
	missedKeywords := to.extractKeywordsFromMissedTriggers(stats.MissedTriggers)
	for _, kw := range missedKeywords {
		// 避免重复
		found := false
		for _, existing := range optimalKeywords {
			if existing == kw {
				found = true
				break
			}
		}
		if !found {
			normalPriorityKeywords = append(normalPriorityKeywords, kw)
		}
	}

	// 合并关键词列表（高优先级在前）
	optimalKeywords = append(highPriorityKeywords, normalPriorityKeywords...)

	// 限制关键词数量
	if len(optimalKeywords) > 20 {
		optimalKeywords = optimalKeywords[:20]
	}

	// 确定优先级
	priority := 5 // 默认优先级
	if successRate >= 80 {
		priority = 8
	} else if successRate >= 60 {
		priority = 6
	} else if successRate < 40 {
		priority = 3
	}

	// 确定最小置信度
	minConfidence := 0.6
	if successRate >= 80 {
		minConfidence = 0.7
	} else if successRate < 40 {
		minConfidence = 0.5
	}

	condition := &TriggerCondition{
		Keywords:      optimalKeywords,
		MinConfidence: minConfidence,
		Priority:      priority,
	}

	// 更新统计
	stats.OptimalKeywords = optimalKeywords
	stats.LastOptimized = time.Now()

	// 保存（本函数全程持有写锁，必须用 saveStatsLocked）
	if err := to.saveStatsLocked(); err != nil {
		return nil, fmt.Errorf("failed to save stats: %w", err)
	}

	return condition, nil
}

// GetOptimizedTriggers 获取优化后的触发条件
func (to *TriggerOptimizer) GetOptimizedTriggers(skillName string) *TriggerCondition {
	to.mu.RLock()
	defer to.mu.RUnlock()

	stats, exists := to.stats[skillName]
	if !exists || len(stats.OptimalKeywords) == 0 {
		return nil
	}

	// 获取技能效果数据以确定优先级
	var successRate float64
	if to.effMgr != nil {
		skillStats := to.effMgr.GetSkillStatistics(skillName)
		if skillStats != nil {
			successRate = skillStats.SuccessRate
		}
	}

	priority := 5
	if successRate >= 80 {
		priority = 8
	} else if successRate >= 60 {
		priority = 6
	} else if successRate < 40 {
		priority = 3
	}

	minConfidence := 0.6
	if successRate >= 80 {
		minConfidence = 0.7
	} else if successRate < 40 {
		minConfidence = 0.5
	}

	return &TriggerCondition{
		Keywords:      append([]string{}, stats.OptimalKeywords...),
		MinConfidence: minConfidence,
		Priority:      priority,
	}
}

// RunAutoOptimization 自动优化所有技能
func (to *TriggerOptimizer) RunAutoOptimization() error {
	to.mu.RLock()
	skillNames := make([]string, 0, len(to.stats))
	for skillName := range to.stats {
		skillNames = append(skillNames, skillName)
	}
	to.mu.RUnlock()

	var errors []string
	for _, skillName := range skillNames {
		_, err := to.OptimizeTriggers(skillName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", skillName, err))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("optimization errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetAllTriggerStats 获取所有触发统计
func (to *TriggerOptimizer) GetAllTriggerStats() map[string]*TriggerStats {
	to.mu.RLock()
	defer to.mu.RUnlock()

	result := make(map[string]*TriggerStats)
	for skillName, stats := range to.stats {
		statsCopy := &TriggerStats{
			SkillName:       stats.SkillName,
			TriggerHits:     make(map[string]int),
			FalsePositives:  make(map[string]int),
			MissedTriggers:  append([]string{}, stats.MissedTriggers...),
			OptimalKeywords: append([]string{}, stats.OptimalKeywords...),
			LastOptimized:   stats.LastOptimized,
		}
		for k, v := range stats.TriggerHits {
			statsCopy.TriggerHits[k] = v
		}
		for k, v := range stats.FalsePositives {
			statsCopy.FalsePositives[k] = v
		}
		result[skillName] = statsCopy
	}

	return result
}

// ClearStats 清空统计
func (to *TriggerOptimizer) ClearStats(skillName string) error {
	to.mu.Lock()
	defer to.mu.Unlock()

	if skillName == "" {
		// 清空所有
		to.stats = make(map[string]*TriggerStats)
	} else {
		delete(to.stats, skillName)
	}

	// 保存（本函数全程持有写锁，必须用 saveStatsLocked）
	return to.saveStatsLocked()
}

// extractKeywordsFromMissedTriggers 从漏触发案例中提取高频关键词
func (to *TriggerOptimizer) extractKeywordsFromMissedTriggers(triggers []string) []string {
	if len(triggers) == 0 {
		return nil
	}

	// 简单的关键词提取：统计词频
	wordFreq := make(map[string]int)
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"and": true, "or": true, "but": true, "in": true, "on": true,
		"at": true, "to": true, "for": true, "of": true, "with": true,
		"by": true, "from": true, "up": true, "about": true, "into": true,
		"through": true, "during": true, "before": true, "after": true,
		"above": true, "below": true, "between": true, "among": true,
		"我": true, "你": true, "他": true, "她": true, "它": true,
		"的": true, "了": true, "在": true, "是": true, "有": true,
		"和": true, "与": true, "或": true, "就": true, "都": true,
		"要": true, "会": true, "能": true, "可以": true, "这": true,
		"那": true, "个": true, "为": true, "之": true, "也": true,
	}

	for _, trigger := range triggers {
		// 分词（CJK 按 2-gram，拉丁按空格；见 tokenize）
		words := to.tokenize(trigger)
		for _, word := range words {
			word = strings.ToLower(strings.TrimSpace(word))
			// 按 rune 数过滤：CJK 一个字占 3 字节，len() 会放过单字噪声
			if utf8.RuneCountInString(word) < 2 {
				continue
			}
			if stopWords[word] {
				continue
			}
			wordFreq[word]++
		}
	}

	// 按频率排序
	type wordCount struct {
		word  string
		count int
	}
	var counts []wordCount
	for word, count := range wordFreq {
		if count >= 2 { // 至少出现2次
			counts = append(counts, wordCount{word, count})
		}
	}

	sort.Slice(counts, func(i, j int) bool {
		return counts[i].count > counts[j].count
	})

	// 返回前10个高频词
	var result []string
	for i, wc := range counts {
		if i >= 10 {
			break
		}
		result = append(result, wc.word)
	}

	return result
}

// tokenize 简单的分词。
//
// 纯空格分词只对拉丁文本有效：中日韩文本没有词间空格，整句会变成一个 token，
// 于是"从漏触发案例学到的关键词"要求整句原样复现才能命中（applyOptimizedTriggers
// 用 strings.Contains(input, keyword) 匹配）。这里对 CJK 连续段按 2-gram 切分，
// 拉丁词保持原样 —— 2-gram 是无依赖方案下最稳的召回单位，且能被 Contains 以
// 子串方式自然命中；单字不成 gram（信息量过低，且多为停用字）。
func (to *TriggerOptimizer) tokenize(text string) []string {
	// 移除标点符号（CJK 汉字属于 \p{L}，不受影响）
	re := regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
	text = re.ReplaceAllString(text, " ")

	words := make([]string, 0, 16)
	for _, field := range strings.Fields(text) {
		runes := []rune(field)
		for i := 0; i < len(runes); {
			if isCJK(runes[i]) {
				j := i
				for j < len(runes) && isCJK(runes[j]) {
					j++
				}
				// CJK 连续段切 2-gram；相邻 gram 共享一个字（"登录超时"→登录/录超/超时），
				// 这样跨词边界（"录超"）会成为噪声，由词频统计与误触发过滤兜底。
				for k := i; k+1 < j; k++ {
					words = append(words, string(runes[k:k+2]))
				}
				i = j
			} else {
				j := i
				for j < len(runes) && !isCJK(runes[j]) {
					j++
				}
				words = append(words, string(runes[i:j]))
				i = j
			}
		}
	}
	return words
}

// isCJK 报告 r 是否属于中日韩表意文字/假名/谚文。
// 2-gram 切分只对这些无空格书写系统有意义，拉丁/西里尔等继续按空格分词。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0x3040 && r <= 0x30FF: // 平假名 + 片假名
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // 谚文音节
		return true
	}
	return false
}

// saveStats 保存统计到文件。自行加读锁，供未持锁的调用方使用。
//
// 注意：若调用方已持有 to.mu（读锁或写锁），必须改用 saveStatsLocked()。
func (to *TriggerOptimizer) saveStats() error {
	to.mu.RLock()
	defer to.mu.RUnlock()
	return to.saveStatsLocked()
}

// saveStatsLocked 保存统计到文件，不做任何加锁。调用方必须已持有 to.mu。
//
// 抽出这个函数是为了修掉一类真实的死锁：此前 saveStats 内部无条件
// to.mu.RLock()，而它的四个调用点里有三个是在**已持锁**的情况下调用的：
//   - RecordTriggerHit / RecordMissedTrigger：持 Lock 时 `go to.saveStats()`
//   - OptimizeTriggers：持 Lock 时直接调用 → 立刻自死锁
//   - ClearStats：持 Lock 时直接调用 → 立刻自死锁
//
// 由于这两条路径此前从未被任何代码触发过（TriggerOptimizer 从未被构造），
// 死锁一直潜伏在代码里没有被发现。
func (to *TriggerOptimizer) saveStatsLocked() error {
	data, err := json.MarshalIndent(to.stats, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal stats: %w", err)
	}

	if err := os.WriteFile(to.statsDB, data, 0644); err != nil {
		return fmt.Errorf("failed to write stats file: %w", err)
	}

	return nil
}

// loadStats 从文件加载统计
func (to *TriggerOptimizer) loadStats() error {
	data, err := os.ReadFile(to.statsDB)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, &to.stats); err != nil {
		return fmt.Errorf("failed to unmarshal stats: %w", err)
	}

	// 确保 map 不为 nil
	for _, stats := range to.stats {
		if stats.TriggerHits == nil {
			stats.TriggerHits = make(map[string]int)
		}
		if stats.FalsePositives == nil {
			stats.FalsePositives = make(map[string]int)
		}
	}

	return nil
}
