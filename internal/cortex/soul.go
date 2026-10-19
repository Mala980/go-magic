package cortex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/magicwubiao/go-magic/pkg/log"
)

// ErrSoulCompactionHeaderGuard 表示 SOUL 压缩被安全闸拦下（继续压缩会丢人格头）。
//
// 与"没有可回收区块"（CompactSoul 返回 0, nil）是两回事：前者需要人处理
// （SOUL 已逼近上限却挤不出空间），后者是正常结果。调用方据此给出不同的提示。
var ErrSoulCompactionHeaderGuard = errors.New("soul compaction aborted: personality header would be lost")

// SoulManager manages the system personality (SOUL.md)
type SoulManager struct {
	baseDir     string
	soulPath    string
	content     string
	mu          sync.RWMutex
	defaultSoul string

	// stats 记录裁剪/拒绝/快照等运维可见的计数（见 SoulStats）。
	// 这些数字是"人格文件是否正在被破坏"的唯一早期信号，必须对外暴露。
	stats SoulStats
}

// DefaultSoul is the default personality when no SOUL.md exists
const DefaultSoul = `You are Magic, a helpful AI assistant powered by go-magic framework.

Your core traits:
- Helpful and knowledgeable
- Precise and practical
- Growth-oriented, learning from interactions
- Transparent about your capabilities and limitations

Your capabilities:
- File operations (read, write, edit)
- Web search and content extraction
- Code execution and debugging
- Task planning and decomposition
- Memory and learning from past interactions

Guidelines:
- Be concise and actionable
- Ask clarifying questions when needed
- Admit when you don't know something
- Learn from user preferences and feedback
`

const (
	// maxSoulSize 是 SOUL.md 的硬上限（字节）。
	maxSoulSize = 64 * 1024

	// learnedPreferencesMarker 标记"机器可管理区"。所有自动生成内容
	// （用户反馈偏好、GEPA 注入）都必须落在以该标题开头的区块内，
	// 裁剪只允许发生在这里面。
	//
	// 历史教训：此前裁剪逻辑在找不到该标记时从**文件头部**截断，
	// 而 GEPA 走 applyAdd 追加 `## <Section>`、从不写该标记，
	// 于是任何一次写入都会静默删掉 SOUL.md 的人格头（"You are Magic…"）。
	// 因此现在：GEPA 写入统一改为 ApplyManagedSection（带标记），
	// 且找不到标记时**拒绝写入**，绝不碰头部。
	learnedPreferencesMarker = "## Learned Preferences"

	// soulSnapshotPrefix / maxSoulSnapshots 控制世代快照环形保留。
	soulSnapshotPrefix = "SOUL_"
	maxSoulSnapshots   = 8

	// soulCompactTriggerBytes 是 MaybeCompact 的触发字节数（= 64KB * 0.9）。
	// 写成常量而不是每次做浮点乘法，避免常量转换问题。
	soulCompactTriggerBytes = 58982
)

// SoulStats 记录 SOUL 写入侧的运维指标。
type SoulStats struct {
	Writes          int       `json:"writes"`            // 成功写入次数
	TrimsApplied    int       `json:"trims_applied"`     // 成功裁剪（淘汰机器区块）次数
	SectionsDropped int       `json:"sections_dropped"`  // 累计被淘汰的机器区块数
	WritesRefused   int       `json:"writes_refused"`    // 因超限且无可淘汰区块而拒绝写入次数
	HeadCutsAvoided int       `json:"head_cuts_avoided"` // 被阻止的头部截断次数（= WritesRefused）
	Snapshots       int       `json:"snapshots"`         // 已生成快照数
	CompactRuns     int       `json:"compact_runs"`      // 自动压缩执行次数
	LastTrimAt      time.Time `json:"last_trim_at,omitempty"`
	LastRefusalAt   time.Time `json:"last_refusal_at,omitempty"`
}

// SoulSnapshot 是一次 SOUL.md 的历史快照（写入前自动留存）。
type SoulSnapshot struct {
	Name    string    `json:"name"`
	Size    int       `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// NewSoulManager creates a new soul manager
func NewSoulManager(baseDir string) *SoulManager {
	return &SoulManager{
		baseDir:     baseDir,
		soulPath:    filepath.Join(baseDir, "SOUL.md"),
		content:     DefaultSoul,
		defaultSoul: DefaultSoul,
	}
}

// Load loads the SOUL.md file from disk
func (m *SoulManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.soulPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default SOUL.md
			m.content = m.defaultSoul
			return m.save()
		}
		return err
	}

	m.content = string(data)
	return nil
}

// save saves the soul content to disk
func (m *SoulManager) save() error {
	// 非 default 分身的人设目录可能还没建出来（ProfileDir = profiles/<name>），
	// 这里补一次 MkdirAll，避免首次写入直接失败。
	if err := os.MkdirAll(filepath.Dir(m.soulPath), 0755); err != nil {
		return err
	}
	return writeFileAtomic(m.soulPath, []byte(m.content), 0644)
}

// GetSoul returns the current soul/personality
func (m *SoulManager) GetSoul() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.content
}

// GetStats 返回写入侧运维指标（裁剪/拒绝/快照）。
func (m *SoulManager) GetStats() SoulStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.stats
	st.HeadCutsAvoided = m.stats.WritesRefused
	return st
}

// SetSoul updates the soul/personality.
//
// 大小约束语义（P0-1 修复）：
//   - 超限时**只**淘汰 `## Learned Preferences` 机器区块（最旧的先删）；
//   - 若没有可淘汰的机器区块，则**拒绝写入并告警**，保留原内容——
//     绝不从头部截断（旧实现会砍掉 "You are Magic…" 人格头且不可恢复）；
//   - 任何成功写入前先留一份世代快照（P0-1b）。
func (m *SoulManager) SetSoul(content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setSoulLocked(content)
}

// setSoulLocked 是 SetSoul 的持锁实现。
func (m *SoulManager) setSoulLocked(content string) error {
	if content == m.content {
		return nil
	}

	trimmed, dropped, refused := enforceSoulLimit(content, maxSoulSize)
	if dropped > 0 {
		m.stats.TrimsApplied++
		m.stats.SectionsDropped += dropped
		m.stats.LastTrimAt = time.Now()
		log.Warnf("[Cortex] SOUL.md over limit (%d bytes > %d): dropped %d auto-generated section(s) from the managed region",
			len(content), maxSoulSize, dropped)
	}
	if refused {
		m.stats.WritesRefused++
		m.stats.LastRefusalAt = time.Now()
		log.Warnf("[Cortex] SOUL.md write REFUSED: content is %d bytes > %d limit and there is no `%s` region to trim; "+
			"keeping previous content instead of truncating the personality header (run `magic cortex soul compact` to reclaim space)",
			len(content), maxSoulSize, learnedPreferencesMarker)
		return fmt.Errorf("soul size %d exceeds limit %d and no auto-generated region is available to trim", len(content), maxSoulSize)
	}

	if len(trimmed) > maxSoulSize {
		// 双重保险：绝不允许写入超限内容。
		m.stats.WritesRefused++
		m.stats.LastRefusalAt = time.Now()
		return fmt.Errorf("soul size %d still exceeds limit %d after trim", len(trimmed), maxSoulSize)
	}

	m.snapshotLocked()
	m.content = trimmed
	m.stats.Writes++
	return m.save()
}

// ApplyManagedSection 把一段自动生成内容写入 SOUL 的机器可管理区。
//
// 语义：以 `### <name>` 作为区块标题，统一挂在 `## Learned Preferences` 之下。
// 同名区块会被就地更新（不重复追加），从而让 SOUL 里的自动生成内容
// 可枚举、可淘汰、可回滚——这是阻止"注入只增不减"的关键。
func (m *SoulManager) ApplyManagedSection(name, body string) error {
	name = strings.TrimSpace(name)
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if name == "" {
		name = "general"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if strings.Contains(m.content, "\n### "+name+"\n") &&
		strings.Contains(m.content, body) {
		// 已存在同名同内容，幂等返回，避免无意义写入与快照。
		return nil
	}

	updated := upsertManagedSection(m.content, name, body)
	if err := m.setSoulLocked(updated); err != nil {
		return err
	}
	return nil
}

// upsertManagedSection 在机器可管理区内插入/更新一个命名区块。
// 若无管理区，则在文件尾部创建；管理区总是位于文件尾部之外不改动既有内容。
func upsertManagedSection(content, name, body string) string {
	block := "### " + name + "\n" + body

	regionIdx := strings.Index(content, learnedPreferencesMarker)
	if regionIdx == -1 {
		// 尚无管理区：创建在文件尾部。
		return strings.TrimRight(content, "\n") + "\n\n" + learnedPreferencesMarker + "\n\n" + block + "\n"
	}

	// 管理区从 marker 起，到下一个顶层 `## ` 标题（或 EOF）止。
	regionStart := regionIdx + len(learnedPreferencesMarker)
	regionEnd := len(content)
	if next := strings.Index(content[regionStart:], "\n## "); next != -1 {
		regionEnd = regionStart + next
	}
	header := content[:regionIdx]
	region := strings.TrimLeft(content[regionStart:regionEnd], "\n")
	tail := content[regionEnd:]

	// 定位同名子区块：`### name` 到下一个 `### ` 或区块末尾。
	//
	// 用"行首匹配"而非 `strings.Index(region, "\n### name\n")`：region 的首行
	// 就是子区块标题时它前面没有 "\n"，锚定写法会漏掉第一个区块，导致每次
	// 更新都退化成"再追加一份"，同名区块无限重复。
	heading := "### " + name
	subStart := -1
	for offset := 0; offset < len(region); {
		rel := strings.Index(region[offset:], heading)
		if rel == -1 {
			break
		}
		pos := offset + rel
		atLineStart := pos == 0 || region[pos-1] == '\n'
		followedByEOL := pos+len(heading) < len(region) && region[pos+len(heading)] == '\n'
		if atLineStart && followedByEOL {
			subStart = pos
			break
		}
		offset = pos + len(heading)
	}

	if subStart != -1 {
		subEnd := len(region)
		if next := strings.Index(region[subStart+len(heading):], "\n### "); next != -1 {
			subEnd = subStart + len(heading) + next + 1
		}
		region = region[:subStart] + block + "\n" + region[subEnd:]
	} else {
		region = strings.TrimRight(region, "\n") + "\n\n" + block + "\n"
	}

	// 重建时只补**一个**换行：marker 与管理区正文之间恰好一个空行。
	// 多补一个就会在每次写入时长出一条空行（线上已累积到 48 行）。
	return header + learnedPreferencesMarker + "\n\n" + region + tail
}

// enforceSoulLimit 在**不触碰人格头部**的前提下把内容压到 maxSize 以内。
//
// 返回：压缩后的内容、被淘汰的机器区块数、是否因无法压缩而必须拒绝写入。
func enforceSoulLimit(content string, maxSize int) (string, int, bool) {
	if len(content) <= maxSize {
		return content, 0, false
	}

	dropped := 0

	// 逐个淘汰最旧的机器区块，直到满足上限或没有可淘汰区块。
	for len(content) > maxSize {
		trimmed, ok := evictOldestManagedSection(content)
		if !ok || len(trimmed) >= len(content) {
			// 没有可淘汰区块（或淘汰不再收敛）→ 拒绝写入，绝不从头部截断。
			return content, dropped, true
		}
		content = trimmed
		dropped++
	}

	// 收敛到合法 UTF-8 边界（理论上不会命中，兜底用）。
	for len(content) > 0 && !utf8.RuneStart(content[0]) {
		content = content[1:]
	}

	return content, dropped, false
}

// evictOldestManagedSection 淘汰机器可管理区里**最旧的一段**。
//
// 关键点：管理区是一个整体，但内部由若干 `### <name>` 子区块组成。
// 旧实现把"从标记到下一个顶层标题"整段删掉，于是一次超限写就会抹掉全部
// 累积的注入内容、连管理区标记本身一起消失（后续注入只能从头部截断）。
// 这里优先只删最旧的单个子区块，管理区与标记都保留；仅当管理区里已经没有
// 子区块时才整段移除（最后手段）。
func evictOldestManagedSection(content string) (string, bool) {
	idx := strings.Index(content, learnedPreferencesMarker)
	if idx == -1 {
		return content, false
	}

	regionStart := idx + len(learnedPreferencesMarker)
	regionEnd := len(content)
	if next := strings.Index(content[regionStart:], "\n## "); next != -1 {
		regionEnd = regionStart + next
	}

	header := content[:idx]
	region := content[regionStart:regionEnd]
	tail := content[regionEnd:]

	// 优先删最旧的 `### 子区块`（只损失一段注入内容）。
	if sub := strings.Index(region, "\n### "); sub != -1 {
		subStart := sub + 1 // 从 `### ` 起删，保留其前面的换行
		subEnd := len(region)
		if nextSub := strings.Index(region[subStart+len("### "):], "\n### "); nextSub != -1 {
			subEnd = subStart + len("### ") + nextSub
		}
		return header + learnedPreferencesMarker + region[:sub] + region[subEnd:] + tail, true
	}

	// 管理区里没有子区块（空区或裸文本）：整段移除，这是最后手段。
	if tail == "" && strings.TrimSpace(region) == "" {
		// 空的尾部管理区：删掉连标记一起，避免留下空标题占位。
		return strings.TrimRight(header, "\n") + "\n", true
	}
	return header + tail, true
}

// CompactSoul 删除 SOUL 中包含已知自动生成载荷的顶层区块，回收被注入内容占用的空间。
//
// payloads 通常来自 GEPA 世代记录里出现过的 new_content。命中的区块会被整段删除
// （连同标题），未命中的原始人格内容不受影响。执行前自动留快照。
// 返回删除的区块数。
func (m *SoulManager) CompactSoul(payloads []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(payloads) == 0 {
		return 0, nil
	}

	// 只保留足够长的载荷用于匹配，避免短字符串误伤。
	needles := make([]string, 0, len(payloads))
	for _, p := range payloads {
		p = strings.TrimSpace(p)
		if len(p) >= 60 {
			needles = append(needles, p)
		}
	}
	if len(needles) == 0 {
		return 0, nil
	}

	sections := splitTopLevelSections(m.content)
	if len(sections) <= 1 {
		return 0, nil
	}

	var kept []string
	removed := 0
	for i, sec := range sections {
		if i == 0 {
			kept = append(kept, sec) // 序言（人格头）永不删除
			continue
		}
		if sectionIsAutoGenerated(sec, needles) {
			removed++
			continue
		}
		kept = append(kept, sec)
	}

	if removed == 0 {
		return 0, nil
	}

	compacted := strings.Join(kept, "")
	if compactionLosesHeader(sections, compacted) {
		// 安全闸：压缩后必须仍保留人格头，否则放弃。
		// 返回**可判定**的哨兵错误而不是静默返回 0：否则调用方（CLI / 运维）
		// 看到的是"压缩执行了但什么都没删"，无从区分"本就没有可回收区块"与
		// "被安全闸拦下"，前者正常、后者需要人处理。
		log.Warnf("[Cortex] SOUL compaction aborted: personality header would be lost")
		return 0, ErrSoulCompactionHeaderGuard
	}

	m.snapshotLocked()
	m.content = compacted
	m.stats.Writes++
	m.stats.CompactRuns++
	m.stats.SectionsDropped += removed
	if err := m.save(); err != nil {
		return 0, err
	}
	log.Infof("[Cortex] SOUL compaction removed %d auto-generated section(s), %d bytes -> %d bytes",
		removed, len(strings.Join(sections, "")), len(compacted))
	return removed, nil
}

// splitTopLevelSections 按顶层 `## ` 标题切分，第 0 项是序言（人格头）。
func splitTopLevelSections(content string) []string {
	var sections []string
	first := strings.Index(content, "## ")
	if first == -1 {
		return []string{content}
	}
	sections = append(sections, content[:first])

	rest := content[first:]
	for rest != "" {
		next := strings.Index(rest[1:], "\n## ")
		if next == -1 {
			sections = append(sections, rest)
			break
		}
		sections = append(sections, rest[:next+1])
		rest = rest[next+1:]
	}
	return sections
}

// sectionIsAutoGenerated 判断某区块是否由自动生成流程写入。
func sectionIsAutoGenerated(section string, needles []string) bool {
	if strings.HasPrefix(section, learnedPreferencesMarker) {
		return true
	}
	if strings.Contains(section, "(Auto-generated from interactions)") {
		return true
	}
	for _, n := range needles {
		if strings.Contains(section, n) {
			return true
		}
	}
	return false
}

// compactionLosesHeader 判定压缩是否会弄丢人格头。
//
// 头部（第 0 个区块 = 序言）永不被删，所以真正的危险只有两种：
//  1. 结果为空 —— 整份内容都只有自动生成区块，人格头本来就不存在；
//  2. 原文件含标准人格头（"You are Magic"）而结果里已丢失。
//
// 此前是无条件要求结果必须含 "You are Magic"，对**自定义过 SOUL 头**的实例
// （很常见）表现为"压缩命令执行了但什么都没删"，与真实故障无法区分。
func compactionLosesHeader(sections []string, compacted string) bool {
	if strings.TrimSpace(compacted) == "" {
		return true
	}
	original := strings.Join(sections, "")
	if strings.Contains(original, "You are Magic") && !strings.Contains(compacted, "You are Magic") {
		return true
	}
	return false
}

// MaybeCompact 在 SOUL 逼近上限时自动执行一次压缩（先快照）。
// payloads 为已知的自动生成载荷集合（由调用方从 GEPA 世代记录收集）。
func (m *SoulManager) MaybeCompact(payloads []string) (int, error) {
	if len(m.GetSoul()) < soulCompactTriggerBytes {
		return 0, nil
	}
	return m.CompactSoul(payloads)
}

// ---------- P0-1b：世代快照与回滚 ----------

// snapshotDir 返回快照目录。
func (m *SoulManager) snapshotDir() string {
	return filepath.Join(m.baseDir, "soul_snapshots")
}

// snapshotLocked 把当前磁盘上的 SOUL.md 留一份有界快照（调用方需持有 m.mu）。
func (m *SoulManager) snapshotLocked() {
	data, err := os.ReadFile(m.soulPath)
	if err != nil || len(data) == 0 {
		return
	}
	dir := m.snapshotDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Warnf("[Cortex] failed to create soul snapshot dir %s: %v", dir, err)
		return
	}

	name := fmt.Sprintf("%s%d.md", soulSnapshotPrefix, time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		log.Warnf("[Cortex] failed to write soul snapshot: %v", err)
		return
	}
	m.stats.Snapshots++
	m.pruneSnapshotsLocked()
}

// pruneSnapshotsLocked 只保留最近 maxSoulSnapshots 份快照。
func (m *SoulManager) pruneSnapshotsLocked() {
	snaps := m.listSnapshotsLocked()
	if len(snaps) <= maxSoulSnapshots {
		return
	}
	for _, s := range snaps[maxSoulSnapshots:] {
		if err := os.Remove(filepath.Join(m.snapshotDir(), s.Name)); err != nil {
			log.Warnf("[Cortex] failed to prune soul snapshot %s: %v", s.Name, err)
		}
	}
}

// listSnapshotsLocked 按时间倒序列出快照（最新在前）。
func (m *SoulManager) listSnapshotsLocked() []SoulSnapshot {
	entries, err := os.ReadDir(m.snapshotDir())
	if err != nil {
		return nil
	}
	var snaps []SoulSnapshot
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), soulSnapshotPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snaps = append(snaps, SoulSnapshot{Name: e.Name(), Size: int(info.Size()), ModTime: info.ModTime()})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Name > snaps[j].Name })
	return snaps
}

// ListSnapshots 列出可用快照（最新在前）。
func (m *SoulManager) ListSnapshots() []SoulSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.listSnapshotsLocked()
}

// RollbackSoul 回滚到指定快照；name 为空或 "latest" 时取最近一份。
// 回滚本身也会先留快照，因此回滚可逆。
func (m *SoulManager) RollbackSoul(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	snaps := m.listSnapshotsLocked()
	if len(snaps) == 0 {
		return fmt.Errorf("no soul snapshot available")
	}

	target := snaps[0]
	if name != "" && name != "latest" {
		found := false
		for _, s := range snaps {
			if s.Name == name {
				target = s
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("soul snapshot not found: %s", name)
		}
	}

	data, err := os.ReadFile(filepath.Join(m.snapshotDir(), target.Name))
	if err != nil {
		return fmt.Errorf("failed to read soul snapshot %s: %w", target.Name, err)
	}

	m.snapshotLocked()
	m.content = string(data)
	m.stats.Writes++
	if err := m.save(); err != nil {
		return fmt.Errorf("failed to restore soul snapshot %s: %w", target.Name, err)
	}
	log.Infof("[Cortex] SOUL.md rolled back to snapshot %s (%d bytes)", target.Name, len(data))
	return nil
}

// UpdateFromFeedback updates the soul based on user feedback
func (m *SoulManager) UpdateFromFeedback(feedback string) error {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return nil
	}

	// 仅在反馈中包含明确的偏好信号时才更新 SOUL，
	// 排除 "I would like"/"It always depends" 等非偏好句式，降低误报率。
	preference := extractPreference(feedback)
	if preference == "" {
		return nil
	}

	// 走统一入口：偏好写入机器可管理区，受同一套裁剪/拒绝/快照保护。
	// （旧实现自带一份"从头部裁"的逻辑，是同一处 P0-1 缺陷的另一条路径。）
	return m.ApplyManagedSection("preference", preference+"\n(Auto-generated from interactions)")
}

// extractPreference 从反馈文本中抽取明确的用户偏好。
// 要求带具体动词的偏好信号（如 "I prefer X"/"用户喜欢 X"/"always use X"），
// 排除 "I would like"/"It always depends" 等非偏好句式。
func extractPreference(feedback string) string {
	lower := strings.ToLower(feedback)

	// 明确的非偏好句式：命中即跳过，即使同时包含偏好关键词也不灌入 SOUL
	nonPreferencePatterns := []string{
		"i would like", "i'd like", "it always depends", "it depends on",
		"i would prefer not", "let me think", "i'm not sure", "maybe later",
	}
	for _, p := range nonPreferencePatterns {
		if strings.Contains(lower, p) {
			return ""
		}
	}

	// 要求明确的偏好信号（带具体动词）
	preferenceSignals := []string{
		"i prefer", "i like", "i love", "i always use", "i always prefer",
		"please always", "always use", "always prefer", "always respond",
		"i want you to always", "from now on",
		"用户喜欢", "用户偏好", "用户希望", "用户总是",
		"我喜欢", "我偏好", "我希望", "我总是",
		"总是使用", "请总是", "以后总是", "从现在开始",
	}
	for _, signal := range preferenceSignals {
		if strings.Contains(lower, signal) {
			return feedback
		}
	}

	return ""
}

// GetSoulForPrompt returns the soul formatted for system prompt
func (m *SoulManager) GetSoulForPrompt() string {
	stable, volatile := m.SplitSoulForPrompt()
	if volatile == "" {
		return "## System Personality (SOUL.md)\n\n" + stable + "\n\n---"
	}
	// 兜底：调用方没有拆分注入能力时，仍返回完整内容（行为与拆分前一致）。
	return "## System Personality (SOUL.md)\n\n" + stable + "\n\n" + volatile + "\n\n---"
}

// SplitSoulForPrompt 把 SOUL 拆成「稳定头」与「易变注入区」两部分，供 agent 侧
// 把二者放进**不同的 system 块**：稳定头进入可缓存前缀，注入区排到最后。
//
// 动机（P2）：GEPA 每 ~10 分钟就往 `## Learned Preferences` 注入一次，而 agent
// 此前把整份 SOUL 作为**一个** system 块放在可缓存前缀里 ⇒ 每次注入都让整个
// system 前缀缓存失效，几百轮的会长任务里规则链/工作目录等稳定段被迫原价重发。
//
// 判定口径与 CompactSoul 的 sectionIsAutoGenerated 一致：以
// learnedPreferencesMarker 开头、或带自动生成标记的顶层区块算易变区。
// 找不到易变区块时 volatile 为空串，调用方退化为单块注入。
func (m *SoulManager) SplitSoulForPrompt() (stable, volatile string) {
	m.mu.RLock()
	content := m.content
	m.mu.RUnlock()

	sections := splitTopLevelSections(content)
	if len(sections) == 0 {
		return strings.TrimSpace(content), ""
	}

	var stableSB, volatileSB strings.Builder
	for i, sec := range sections {
		// 第 0 项是序言（人格头），永不归入易变区。
		if i > 0 && sectionIsAutoGenerated(sec, nil) {
			volatileSB.WriteString(sec)
			continue
		}
		stableSB.WriteString(sec)
	}

	stable = strings.TrimSpace(stableSB.String())
	if stable == "" {
		// 极端情况（整份都被判为自动生成）：退化回单块，绝不把人格头弄丢。
		return strings.TrimSpace(content), ""
	}
	return stable, strings.TrimSpace(volatileSB.String())
}

// ResetToDefault resets the soul to default personality
func (m *SoulManager) ResetToDefault() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotLocked()
	m.content = m.defaultSoul
	m.stats.Writes++
	return m.save()
}
