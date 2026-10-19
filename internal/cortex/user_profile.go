package cortex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/magicwubiao/go-magic/pkg/fslock"
)

// UserPreference represents a single user preference
type UserPreference struct {
	Key        string    `json:"key"`
	Value      string    `json:"value"`
	Context    string    `json:"context,omitempty"`
	Source     string    `json:"source"`     // "explicit", "learned", "feedback"
	Confidence float64   `json:"confidence"` // 0.0-1.0
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UserProfile manages the USER.md with structured preferences
type UserProfile struct {
	mu          sync.RWMutex
	baseDir     string
	userPath    string
	jsonPath    string // JSON 权威存储路径（无损持久化所有偏好键）
	lockPath    string // 跨进程锁文件：gateway / CLI / 面板写同一份 USER.json 时互斥
	preferences map[string]*UserPreference
	interests   []string
	workStyle   string
	techStack   []string
	content     string

	// diskModTime / diskSize 是上次读到/写下 USER.json 时的 mtime 与大小。
	// 读路径靠它判断"别的进程（CLI / gateway）改过磁盘"，避免本进程一直用
	// 启动时那份内存副本回答，导致跨进程改的偏好在当前进程里永远看不到。
	// 同时比较两者：有些文件系统 mtime 精度只有秒，仅比时间会漏掉同秒内的修改。
	diskModTime time.Time
	diskSize    int64

	// 本进程自上次成功落盘以来改动过的东西。落盘前要先与磁盘上的最新状态
	// 合并：只覆盖自己改过的键，别的进程新学到的偏好保留下来。没有这份
	// 变更集，就只能整份覆盖（后写赢），并发时必然丢更新。
	dirtyKeys   map[string]struct{}
	deletedKeys map[string]struct{}
	listsDirty  bool // techStack / interests 被本进程改过
	replaceAll  bool // Reset / Import：语义就是整份替换，不做合并
}

// DefaultUserProfile is the default USER.md template
const DefaultUserProfile = `# User Profile

## About
- Name: [Not set]
- Role: [Not set]

## Preferences
- Communication style: [Not set]
- Code style: [Not set]

## Tech Stack
- Languages: [Not set]
- Frameworks: [Not set]

## Interests
- [Not set]

## Notes
[Any notes about the user]
`

// NewUserProfile creates a new user profile manager
func NewUserProfile(baseDir string) *UserProfile {
	return &UserProfile{
		baseDir:     baseDir,
		userPath:    filepath.Join(baseDir, "USER.md"),
		jsonPath:    filepath.Join(baseDir, "USER.json"),
		lockPath:    filepath.Join(baseDir, "USER.json.lock"),
		preferences: make(map[string]*UserPreference),
		interests:   make([]string, 0),
		techStack:   make([]string, 0),
		content:     DefaultUserProfile,
		dirtyKeys:   make(map[string]struct{}),
		deletedKeys: make(map[string]struct{}),
	}
}

// Load loads the user profile from disk
// 优先从 JSON 权威存储加载（无损保留所有偏好键），不存在或失败时回退到解析 USER.md（向后兼容）
func (up *UserProfile) Load() error {
	up.mu.Lock()
	defer up.mu.Unlock()

	// 优先从 JSON 权威存储加载（无损持久化）
	if data, err := os.ReadFile(up.jsonPath); err == nil {
		if err := up.loadFromJSON(data); err == nil {
			// 同步刷新 markdown 人类可读视图
			up.updateContent()
			_ = writeFileAtomic(up.userPath, []byte(up.content), 0644)
			up.clearChangeSet()
			up.rememberDiskModTime()
			return nil
		}
		// JSON 加载失败则回退到 markdown 解析
	}

	// 回退：从 USER.md 解析（向后兼容已有文件）
	data, err := os.ReadFile(up.userPath)
	if err != nil {
		if os.IsNotExist(err) {
			up.content = DefaultUserProfile
			return up.save()
		}
		return err
	}

	up.content = string(data)
	up.parseContent()
	up.clearChangeSet()
	up.rememberDiskModTime()

	return nil
}

// LoadExisting 与 Load 相同，但**不创建**任何文件。
//
// 给"只读地看一眼某个分身"的场景用（面板、诊断）：Load 在文件缺失时会
// 落一份默认模板，读接口不该有写副作用。文件不存在时返回 os.ErrNotExist。
func (up *UserProfile) LoadExisting() error {
	up.mu.Lock()
	defer up.mu.Unlock()

	if data, err := os.ReadFile(up.jsonPath); err == nil {
		if err := up.loadFromJSON(data); err != nil {
			return err
		}
		up.updateContent()
		up.rememberDiskModTime()
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	data, err := os.ReadFile(up.userPath)
	if err != nil {
		return err
	}
	up.content = string(data)
	up.parseContent()
	up.clearChangeSet()
	up.rememberDiskModTime()
	return nil
}

// rememberDiskModTime 记录当前 USER.json 的 mtime 与大小（调用方需持有 mu）。
func (up *UserProfile) rememberDiskModTime() {
	if info, err := os.Stat(up.jsonPath); err == nil {
		up.diskModTime = info.ModTime()
		up.diskSize = info.Size()
	} else {
		up.diskModTime = time.Time{}
		up.diskSize = 0
	}
}

// refreshFromDiskLocked 在磁盘上的画像被其它进程改过时重新合并（调用方需持有 mu）。
//
// 这不是为了省事而读盘：gateway 常驻，CLI / 面板改完 USER.json 之后，如果
// gateway 不主动看一眼磁盘，它会一直用启动时的内存副本拼 system prompt，
// 用户看到的现象就是"改了偏好但对话没反应"。用 mtime 比较把常态开销压到
// 一次 stat。
func (up *UserProfile) refreshFromDiskLocked() {
	info, err := os.Stat(up.jsonPath)
	if err != nil {
		return
	}
	if info.ModTime().Equal(up.diskModTime) && info.Size() == up.diskSize {
		return
	}
	up.mergeDiskState()
	up.updateContent()
	up.diskModTime = info.ModTime()
	up.diskSize = info.Size()
}

// Dir 返回画像所在目录（SOUL.md / USER.json / USER.md 同目录）。
func (up *UserProfile) Dir() string {
	return up.baseDir
}

// loadFromJSON 从 JSON 数据加载偏好（调用方需持有锁）
func (up *UserProfile) loadFromJSON(data []byte) error {
	var importData struct {
		Preferences map[string]*UserPreference `json:"preferences"`
		Interests   []string                   `json:"interests"`
		TechStack   []string                   `json:"tech_stack"`
	}
	if err := json.Unmarshal(data, &importData); err != nil {
		return err
	}
	up.preferences = importData.Preferences
	if up.preferences == nil {
		up.preferences = make(map[string]*UserPreference)
	}
	up.interests = importData.Interests
	if up.interests == nil {
		up.interests = make([]string, 0)
	}
	up.techStack = importData.TechStack
	if up.techStack == nil {
		up.techStack = make([]string, 0)
	}
	return nil
}

// save saves the user profile to disk
// 同时写入 JSON 权威存储（无损）与 markdown 人类可读视图
//
// 落盘全程持有跨进程锁，并在写之前把磁盘上的最新 JSON 合并进来：本进程只
// 覆盖自己改过的键（dirtyKeys / deletedKeys / listsDirty），其它进程新学到
// 的偏好原样保留。此前没有锁也没有合并，"gateway 学习偏好" 与 "CLI 设置偏好"
// 并发时就是整份覆盖，后写的那次把对方的结果全部抹掉。
func (up *UserProfile) save() error {
	if err := os.MkdirAll(up.baseDir, 0755); err != nil {
		return err
	}

	lk, err := fslock.Acquire(up.lockPath, saveLockTimeout)
	if err != nil {
		return fmt.Errorf("lock user profile: %w", err)
	}
	defer lk.Release()

	if !up.replaceAll {
		up.mergeDiskState()
	}

	up.updateContent()
	if err := up.saveJSON(); err != nil {
		return err
	}
	// 写入 markdown 人类可读视图
	if err := writeFileAtomic(up.userPath, []byte(up.content), 0644); err != nil {
		return err
	}

	up.clearChangeSet()
	up.rememberDiskModTime()
	return nil
}

// saveLockTimeout 是等待跨进程锁的上限。临界区只有一次小文件读写，正常情况
// 立即拿到；超时说明有进程卡住了，宁可报错也不要无限阻塞调用方（可能是一次
// HTTP 请求或一轮对话）。
const saveLockTimeout = 5 * time.Second

// mergeDiskState 把磁盘上比自己更新的偏好并入内存（调用方需持有 mu 与文件锁）。
//
// 合并规则：
//   - 本进程删掉的键：不复活；
//   - 本进程改过的键：以内存为准；
//   - 其余键：磁盘上有更新版本（UpdatedAt 更晚）或内存里压根没有，就采用磁盘的；
//   - techStack / interests 只有在本进程改过时才覆盖磁盘。
func (up *UserProfile) mergeDiskState() {
	data, err := os.ReadFile(up.jsonPath)
	if err != nil {
		return
	}
	var disk struct {
		Preferences map[string]*UserPreference `json:"preferences"`
		Interests   []string                   `json:"interests"`
		TechStack   []string                   `json:"tech_stack"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		// 磁盘 JSON 已损坏：不要拿它做合并（否则会把垃圾并进内存），
		// 直接用自己的状态覆盖，至少能恢复成一份可读的画像。
		return
	}

	for k, dp := range disk.Preferences {
		if dp == nil {
			continue
		}
		if _, deleted := up.deletedKeys[k]; deleted {
			continue
		}
		if _, dirty := up.dirtyKeys[k]; dirty {
			continue
		}
		cur, ok := up.preferences[k]
		if !ok || cur == nil || cur.UpdatedAt.Before(dp.UpdatedAt) {
			cp := *dp
			up.preferences[k] = &cp
		}
	}

	// 反向对齐：USER.json 每次都是整份快照，所以"内存有、磁盘没有"且本进程
	// 没改过的键，只能是别的进程删掉的。不删掉它，本进程下一次 save 就会把
	// 用户刚删的偏好重新写回去（面板删除被 gateway 内存复活）。
	for k := range up.preferences {
		if _, deleted := up.deletedKeys[k]; deleted {
			continue
		}
		if _, dirty := up.dirtyKeys[k]; dirty {
			continue
		}
		if _, ok := disk.Preferences[k]; !ok {
			delete(up.preferences, k)
		}
	}

	if !up.listsDirty {
		if disk.TechStack != nil {
			up.techStack = disk.TechStack
		}
		if disk.Interests != nil {
			up.interests = disk.Interests
		}
	}
}

// markDirty / markDeleted / clearChangeSet 维护落盘变更集（调用方需持有 mu）。
func (up *UserProfile) markDirty(key string) {
	if up.dirtyKeys == nil {
		up.dirtyKeys = make(map[string]struct{})
	}
	up.dirtyKeys[key] = struct{}{}
	delete(up.deletedKeys, key)
}

func (up *UserProfile) markDeleted(key string) {
	if up.deletedKeys == nil {
		up.deletedKeys = make(map[string]struct{})
	}
	up.deletedKeys[key] = struct{}{}
	delete(up.dirtyKeys, key)
}

func (up *UserProfile) clearChangeSet() {
	up.dirtyKeys = make(map[string]struct{})
	up.deletedKeys = make(map[string]struct{})
	up.listsDirty = false
	up.replaceAll = false
}

// saveJSON 将结构化偏好写入 JSON 权威存储（调用方需持有锁）
func (up *UserProfile) saveJSON() error {
	export := struct {
		Preferences map[string]*UserPreference `json:"preferences"`
		Interests   []string                   `json:"interests"`
		TechStack   []string                   `json:"tech_stack"`
	}{
		Preferences: up.preferences,
		Interests:   up.interests,
		TechStack:   up.techStack,
	}
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(up.baseDir, 0755); err != nil {
		return err
	}
	return writeFileAtomic(up.jsonPath, data, 0644)
}

// parseContent parses USER.md into structured data
func (up *UserProfile) parseContent() {
	// 重新解析前先清空：旧实现只增不删，同一份内容解析两次会残留已删除的键。
	up.preferences = make(map[string]*UserPreference)

	lines := strings.Split(up.content, "\n")
	section := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 跟踪 "## xxx" 小节。updateContent 产出的 "## All Preferences" 每行
		// 形如 "- K: V (source: ..., confidence: ...)"，那是**展示用**副本，
		// 不能当偏好解析 —— 否则 JSON 丢失后的回退路径会把
		// " (source: ...)" 尾巴拼进值里，一次回退就永久污染。
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		if section == "all preferences" || section == "notes" {
			continue
		}

		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") {
			line = strings.TrimPrefix(line, "-")
			line = strings.TrimPrefix(line, "*")
			line = strings.TrimSpace(line)

			// Parse key-value pairs
			if idx := strings.Index(line, ":"); idx > 0 {
				key := strings.TrimSpace(line[:idx])
				value := strings.TrimSpace(line[idx+1:])

				if key != "" && value != "" && value != "[Not set]" {
					up.preferences[key] = &UserPreference{
						Key:        key,
						Value:      value,
						Source:     "parsed",
						Confidence: 0.8,
						CreatedAt:  time.Now(),
						UpdatedAt:  time.Now(),
					}
				}
			}
		}
	}
}

// updateContent updates the markdown content from structured data
// 遍历所有 preferences 输出，避免 SetPreference 设置的非固定键在持久化时丢失
func (up *UserProfile) updateContent() {
	var lines []string

	lines = append(lines, "# User Profile")
	lines = append(lines, "")
	lines = append(lines, "## About")

	if pref, ok := up.preferences["Name"]; ok {
		lines = append(lines, "- Name: "+pref.Value)
	} else {
		lines = append(lines, "- Name: [Not set]")
	}

	if pref, ok := up.preferences["Role"]; ok {
		lines = append(lines, "- Role: "+pref.Value)
	} else {
		lines = append(lines, "- Role: [Not set]")
	}

	lines = append(lines, "")
	lines = append(lines, "## Preferences")

	if pref, ok := up.preferences["Communication style"]; ok {
		lines = append(lines, "- Communication style: "+pref.Value)
	} else {
		lines = append(lines, "- Communication style: [Not set]")
	}

	if pref, ok := up.preferences["Code style"]; ok {
		lines = append(lines, "- Code style: "+pref.Value)
	} else {
		lines = append(lines, "- Code style: [Not set]")
	}

	lines = append(lines, "")
	lines = append(lines, "## Tech Stack")

	if len(up.techStack) > 0 {
		for _, tech := range up.techStack {
			lines = append(lines, "- "+tech)
		}
	} else {
		lines = append(lines, "- Languages: [Not set]")
		lines = append(lines, "- Frameworks: [Not set]")
	}

	lines = append(lines, "")
	lines = append(lines, "## Interests")

	if len(up.interests) > 0 {
		for _, interest := range up.interests {
			lines = append(lines, "- "+interest)
		}
	} else {
		lines = append(lines, "- [Not set]")
	}

	// 输出所有偏好键（含 explicit/learned/parsed 等），确保 SetPreference 设置的
	// 任意键都不会在持久化时丢失。JSON 权威存储已保留全部，此处为人类可读视图。
	lines = append(lines, "")
	lines = append(lines, "## All Preferences")
	if len(up.preferences) == 0 {
		lines = append(lines, "- [Not set]")
	} else {
		// 按 key 排序：map 遍历顺序随机，不排序会让每次 SetPreference 都
		// 在 USER.md 里产生一堆无意义 diff（而且同一份画像每次落盘内容不同）。
		keys := make([]string, 0, len(up.preferences))
		for k := range up.preferences {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pref := up.preferences[k]
			if pref == nil {
				continue
			}
			lines = append(lines, "- "+pref.Key+": "+pref.Value+
				" (source: "+pref.Source+", confidence: "+formatConfidence(pref.Confidence)+")")
		}
	}

	lines = append(lines, "")
	lines = append(lines, "## Notes")
	lines = append(lines, "[Auto-managed by go-magic]")

	up.content = strings.Join(lines, "\n")
}

// SetPreference sets a user preference explicitly
func (up *UserProfile) SetPreference(key, value string) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	up.preferences[key] = &UserPreference{
		Key:        key,
		Value:      value,
		Source:     "explicit",
		Confidence: 1.0,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	up.markDirty(key)

	return up.save()
}

// LearnPreference learns a preference from interaction
func (up *UserProfile) LearnPreference(key, value, context string) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	if existing, ok := up.preferences[key]; ok {
		// 更新已存在偏好，多次强化置信度最高可达 0.95（与 explicit 的 1.0 仍有差距）
		existing.Value = value
		existing.Context = context
		existing.Source = "learned"
		existing.Confidence = min(existing.Confidence+0.1, 0.95) // 不再封顶 0.8
		existing.UpdatedAt = time.Now()
	} else {
		up.preferences[key] = &UserPreference{
			Key:        key,
			Value:      value,
			Context:    context,
			Source:     "learned",
			Confidence: 0.5,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
	}
	up.markDirty(key)

	return up.save()
}

// AddInterest adds an interest
func (up *UserProfile) AddInterest(interest string) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	// Check if already exists
	for _, existing := range up.interests {
		if strings.EqualFold(existing, interest) {
			return nil
		}
	}

	up.interests = append(up.interests, interest)
	up.listsDirty = true
	return up.save()
}

// AddTech adds a technology to the tech stack
func (up *UserProfile) AddTech(tech string) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	// Check if already exists
	for _, existing := range up.techStack {
		if strings.EqualFold(existing, tech) {
			return nil
		}
	}

	up.techStack = append(up.techStack, tech)
	up.listsDirty = true
	return up.save()
}

// GetPreference returns a preference by key.
//
// 返回**副本**：直接把内部指针交出去，调用方读字段时就与 LearnPreference
// 的原地更新构成数据竞态（锁已经释放了）。
func (up *UserProfile) GetPreference(key string) *UserPreference {
	up.mu.RLock()
	defer up.mu.RUnlock()
	p, ok := up.preferences[key]
	if !ok || p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// GetAllPreferences returns all preferences (copies), sorted by key.
func (up *UserProfile) GetAllPreferences() []*UserPreference {
	up.mu.Lock()
	defer up.mu.Unlock()
	// 面板读的是"当前真实状态"：别的进程刚写过的偏好要能被看到。
	up.refreshFromDiskLocked()

	keys := make([]string, 0, len(up.preferences))
	for k := range up.preferences {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	prefs := make([]*UserPreference, 0, len(keys))
	for _, k := range keys {
		p := up.preferences[k]
		if p == nil {
			continue
		}
		cp := *p
		prefs = append(prefs, &cp)
	}
	return prefs
}

// GetPreferencesBySource returns preferences filtered by source (copies).
func (up *UserProfile) GetPreferencesBySource(source string) []*UserPreference {
	up.mu.RLock()
	defer up.mu.RUnlock()

	var prefs []*UserPreference
	for _, p := range up.preferences {
		if p != nil && p.Source == source {
			cp := *p
			prefs = append(prefs, &cp)
		}
	}
	return prefs
}

// GetHighConfidence returns preferences with high confidence (copies).
func (up *UserProfile) GetHighConfidence(minConfidence float64) []*UserPreference {
	up.mu.RLock()
	defer up.mu.RUnlock()

	var prefs []*UserPreference
	for _, p := range up.preferences {
		if p != nil && p.Confidence >= minConfidence {
			cp := *p
			prefs = append(prefs, &cp)
		}
	}
	return prefs
}

// UserProfileInfo 是面板"用户画像"表单提交的结构化资料。
type UserProfileInfo struct {
	Name               string
	Role               string
	CommunicationStyle string
	CodeStyle          string
	TechStack          []string
	Interests          []string
}

// UpdateProfileInfo 用面板表单的值整体覆盖画像，并且只落盘一次。
//
// 之前面板写的是 profiles/<p>/user.md，而运行时读的是 cortex/USER.json，
// 两套事实源导致"保存成功但对话没变化"。现在表单值直接进入权威存储，
// USER.md 由 save() 重新生成，读接口看到的就是同一份。
//
// 语义是**表单字段整体覆盖**：调用方要提交整份表单，字段为空串（或 "[Not set]"）
// 表示清空该项。只改某一项时，请先读出当前值再整体提交 —— 否则其余字段
// 会被当成"用户想清空"。表单之外的偏好键（运行时学到的）不受影响：
// 落盘时只覆盖这四个键 + 两个列表，其余键与磁盘合并保留。
func (up *UserProfile) UpdateProfileInfo(info UserProfileInfo) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	setOrDelete := func(key, value string) {
		if value = strings.TrimSpace(value); value == "" || value == "[Not set]" {
			delete(up.preferences, key)
			up.markDeleted(key)
			return
		}
		now := time.Now()
		up.markDirty(key)
		if existing, ok := up.preferences[key]; ok && existing != nil {
			existing.Value = value
			existing.Source = "explicit"
			existing.Confidence = 1.0
			existing.UpdatedAt = now
			return
		}
		up.preferences[key] = &UserPreference{
			Key:        key,
			Value:      value,
			Source:     "explicit",
			Confidence: 1.0,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
	}

	setOrDelete("Name", info.Name)
	setOrDelete("Role", info.Role)
	setOrDelete("Communication style", info.CommunicationStyle)
	setOrDelete("Code style", info.CodeStyle)

	up.techStack = normalizeList(info.TechStack)
	up.interests = normalizeList(info.Interests)
	up.listsDirty = true

	return up.save()
}

// ApplyFeedback 应用用户对某条偏好的"准 / 不准"反馈。
//
// 这不是日志：false 会把置信度压到 0.5 以下，GetForPrompt 的注入阈值是
// 0.5，也就意味着被用户否掉的偏好会立刻停止影响 system prompt；true 则
// 把它提到 1.0（等价于显式确认）。
func (up *UserProfile) ApplyFeedback(key string, accurate bool) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	pref, ok := up.preferences[key]
	if !ok || pref == nil {
		return fmt.Errorf("preference not found: %s", key)
	}

	if accurate {
		pref.Confidence = min(pref.Confidence+0.2, 1.0)
	} else {
		pref.Confidence = max(pref.Confidence-0.3, 0)
	}
	pref.Source = "feedback"
	pref.UpdatedAt = time.Now()
	up.markDirty(key)

	return up.save()
}

// normalizeList 去空白、去重（忽略大小写），保持原顺序。
func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || v == "[Not set]" {
			continue
		}
		k := strings.ToLower(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// GetForPrompt returns the user profile formatted for system prompt.
//
// 空画像返回 ""（不注入）。此前无条件返回 "## User Profile\n\n" 的空壳，
// 等于每轮往 system prompt 里塞一段零信息的样板。
// 偏好按 key 排序输出：map 遍历顺序随机，同一份画像每次拼出的文本必须逐字节
// 一致，否则 prompt 前缀缓存永远命中不了。
func (up *UserProfile) GetForPrompt() string {
	up.mu.Lock()
	defer up.mu.Unlock()
	// 先看一眼磁盘：CLI / 面板改完偏好后，常驻的 gateway 也必须在下一次拼
	// prompt 时用上，而不是等它自己写一次才更新。
	up.refreshFromDiskLocked()

	var lines []string

	// 高置信度偏好（阈值从 0.6 降到 0.5，让强化后的 learned 偏好可显示）
	keys := make([]string, 0, len(up.preferences))
	for k, p := range up.preferences {
		if p != nil && p.Confidence >= 0.5 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, "- "+k+": "+up.preferences[k].Value)
	}

	// Add tech stack
	if len(up.techStack) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Tech stack: "+strings.Join(up.techStack, ", "))
	}

	// Add interests
	if len(up.interests) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Interests: "+strings.Join(up.interests, ", "))
	}

	if len(lines) == 0 {
		return ""
	}
	return "## User Profile\n\n" + strings.Join(lines, "\n")
}

// Reset resets the user profile to default
func (up *UserProfile) Reset() error {
	up.mu.Lock()
	defer up.mu.Unlock()

	up.preferences = make(map[string]*UserPreference)
	up.interests = make([]string, 0)
	up.techStack = make([]string, 0)
	up.content = DefaultUserProfile
	up.replaceAll = true // 重置的语义就是整份替换，不做合并

	return up.save()
}

// Export exports the profile as JSON
func (up *UserProfile) Export() ([]byte, error) {
	up.mu.RLock()
	defer up.mu.RUnlock()

	export := struct {
		Preferences map[string]*UserPreference `json:"preferences"`
		Interests   []string                   `json:"interests"`
		TechStack   []string                   `json:"tech_stack"`
	}{
		Preferences: up.preferences,
		Interests:   up.interests,
		TechStack:   up.techStack,
	}

	return json.MarshalIndent(export, "", "  ")
}

// Import imports a profile from JSON
func (up *UserProfile) Import(data []byte) error {
	up.mu.Lock()
	defer up.mu.Unlock()

	if err := up.loadFromJSON(data); err != nil {
		return err
	}
	up.replaceAll = true // 导入即整份替换

	return up.save()
}

// Helper function
func formatConfidence(c float64) string {
	s := fmt.Sprintf("%.2f", c)
	// Convert to percentage format
	return s
}

// writeFileAtomic 先写同目录临时文件再 rename：崩溃/断电不会留下半截 JSON
// （半截 JSON 会被上层当成"文件损坏"进而静默回退，等于丢偏好）。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后是空操作

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
