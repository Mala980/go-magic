package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Status represents goal status
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusAbandoned Status = "abandoned"
)

// Goal represents a user goal
type Goal struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      Status     `json:"status"`
	Progress    int        `json:"progress"` // 0-100
	SessionIDs  []string   `json:"session_ids"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Manager manages goals
type Manager struct {
	mu    sync.RWMutex
	goals map[string]*Goal
	dir   string
}

// NewManager creates a new goal manager
func NewManager(dir string) (*Manager, error) {
	m := &Manager{
		goals: make(map[string]*Goal),
		dir:   dir,
	}

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	// Load existing goals
	if err := m.load(); err != nil {
		return nil, err
	}

	return m, nil
}

// Create creates a new goal
func (m *Manager) Create(ctx context.Context, title, description string) (*Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// CreatedAt / UpdatedAt 共用同一个戳：两者若分别取时间，在低分辨率时钟上
	// 可能出现 UpdatedAt < CreatedAt，破坏"更新时间不早于创建时间"的直觉，
	// 也让下游 (UpdatedAt, CreatedAt) 的排序失去意义。
	now := nextUpdatedAt(time.Time{})
	goal := &Goal{
		ID:          generateID(),
		Title:       title,
		Description: description,
		Status:      StatusActive,
		Progress:    0,
		SessionIDs:  []string{},
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	m.goals[goal.ID] = goal
	if err := m.save(); err != nil {
		delete(m.goals, goal.ID)
		return nil, err
	}

	return goal, nil
}

// Get gets a goal by ID
func (m *Manager) Get(ctx context.Context, id string) (*Goal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	goal, ok := m.goals[id]
	if !ok {
		return nil, fmt.Errorf("goal not found: %s", id)
	}

	return goal, nil
}

// List lists all goals
func (m *Manager) List(ctx context.Context, status Status) ([]*Goal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Goal
	for _, g := range m.goals {
		if status == "" || g.Status == status {
			result = append(result, g)
		}
	}

	// Sort by updated_at desc。时间戳相同时按 ID 降序兜底：map 遍历顺序是随机的，
	// 只比时间会让同刻更新的目标每次都换位置（列表跳动，也让调用方的"第一条"
	// 不可预期）。generateID 到纳秒，ID 降序≈创建先后，符合直觉。
	sort.SliceStable(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.After(result[j].UpdatedAt)
		}
		return result[i].ID > result[j].ID
	})

	return result, nil
}

// Update updates a goal
func (m *Manager) Update(ctx context.Context, id string, updates map[string]interface{}) (*Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	goal, ok := m.goals[id]
	if !ok {
		return nil, fmt.Errorf("goal not found: %s", id)
	}

	if title, ok := updates["title"].(string); ok {
		goal.Title = title
	}
	if desc, ok := updates["description"].(string); ok {
		goal.Description = desc
	}
	if status, ok := updates["status"].(string); ok {
		goal.Status = Status(status)
		if goal.Status == StatusCompleted {
			stamp := nextUpdatedAt(goal.UpdatedAt)
			goal.CompletedAt = &stamp
			goal.Progress = 100
		}
	}
	if progress, ok := updates["progress"].(float64); ok {
		goal.Progress = int(progress)
		if goal.Progress > 100 {
			goal.Progress = 100
		}
		if goal.Progress < 0 {
			goal.Progress = 0
		}
		// Auto-complete when progress reaches 100%
		if goal.Progress == 100 && goal.Status != StatusCompleted {
			goal.Status = StatusCompleted
			stamp := nextUpdatedAt(goal.UpdatedAt)
			goal.CompletedAt = &stamp
		}
	}

	goal.UpdatedAt = nextUpdatedAt(goal.UpdatedAt)

	if err := m.save(); err != nil {
		return nil, err
	}

	return goal, nil
}

// Delete deletes a goal
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.goals[id]; !ok {
		return fmt.Errorf("goal not found: %s", id)
	}

	delete(m.goals, id)
	return m.save()
}

// LinkSession links a session to a goal
func (m *Manager) LinkSession(ctx context.Context, goalID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	goal, ok := m.goals[goalID]
	if !ok {
		return fmt.Errorf("goal not found: %s", goalID)
	}

	// Check if already linked
	for _, id := range goal.SessionIDs {
		if id == sessionID {
			return nil
		}
	}

	goal.SessionIDs = append(goal.SessionIDs, sessionID)
	goal.UpdatedAt = nextUpdatedAt(goal.UpdatedAt)

	return m.save()
}

// UnlinkSession unlinks a session from a goal
func (m *Manager) UnlinkSession(ctx context.Context, goalID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	goal, ok := m.goals[goalID]
	if !ok {
		return fmt.Errorf("goal not found: %s", goalID)
	}

	var newIDs []string
	for _, id := range goal.SessionIDs {
		if id != sessionID {
			newIDs = append(newIDs, id)
		}
	}
	goal.SessionIDs = newIDs
	goal.UpdatedAt = nextUpdatedAt(goal.UpdatedAt)

	return m.save()
}

// GetGoalsBySession gets all goals linked to a session
func (m *Manager) GetGoalsBySession(ctx context.Context, sessionID string) ([]*Goal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Goal
	for _, g := range m.goals {
		for _, id := range g.SessionIDs {
			if id == sessionID {
				result = append(result, g)
				break
			}
		}
	}
	return result, nil
}

// save saves goals to disk
func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.goals, "", "  ")
	if err != nil {
		return err
	}

	// Ensure goals subdirectory exists
	goalsDir := filepath.Join(m.dir, "goals")
	if err := os.MkdirAll(goalsDir, 0755); err != nil {
		return err
	}

	file := filepath.Join(goalsDir, "goals.json")
	return os.WriteFile(file, data, 0644)
}

// load loads goals from disk
func (m *Manager) load() error {
	// Try new location first (goals/goals.json)
	goalsDir := filepath.Join(m.dir, "goals")
	file := filepath.Join(goalsDir, "goals.json")
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			// Try legacy location (goals.json in root dir) for migration
			legacyFile := filepath.Join(m.dir, "goals.json")
			legacyData, legacyErr := os.ReadFile(legacyFile)
			if legacyErr != nil {
				if os.IsNotExist(legacyErr) {
					return nil
				}
				return legacyErr
			}
			// Migrate to new location
			if err := os.MkdirAll(goalsDir, 0755); err != nil {
				return err
			}
			if err := os.WriteFile(file, legacyData, 0644); err != nil {
				return err
			}
			// Remove legacy file
			_ = os.Remove(legacyFile)
			data = legacyData
		} else {
			return err
		}
	}

	return json.Unmarshal(data, &m.goals)
}

func generateID() string {
	return fmt.Sprintf("goal_%d", time.Now().UnixNano())
}

// nowStamp 是 goal 模块唯一的时间来源，默认就是 time.Now。
//
// 做成变量是为了让测试能把"当前时刻"钉住：这个模块对"谁更晚更新"是全序敏感的，
// 而真实时钟在 Windows 上只有 ~15ms 粒度、连续调用可能取到同一个值。生产行为
// 由下面的单调兜底保证，测试则用固定时钟直接复现/验证同刻场景。
var nowStamp = time.Now

// nextUpdatedAt 返回"下一次更新"的时间戳，保证**同一进程内严格单调递增**。
//
// 为什么不直接用 time.Now()：Windows 上其分辨率约 15ms，连续两次 Update 会拿到
// 完全相同的时间戳。此时"哪个目标最近更新过"就失去依据，挑选/排序退化成看
// map 遍历顺序——表现为结果时对时错（goalContextBlock 偶发挑错目标）。
//
// 单调性靠包级水位 lastStamp 保证，而不是只看 prev：调用方传入的 prev 可能比
// 本次 time.Now() 还新（时钟回拨、或对象来自反序列化的旧数据），只比 prev 会在
// 跨目标交替更新时翻转顺序。
var (
	stampMu   sync.Mutex
	lastStamp time.Time
)

// ClockForTest 用于在测试里替换时间来源。
//
// Now 为 nil / 未调用 NewClockForTest 时一律使用 time.Now，生产路径零开销可控。
// Reset 为 true 表示同时清空单调水位——测试之间必须清，否则上一个用例把水位
// 推高后，本用例的固定时钟会被水位顶到未来。
type ClockForTest struct {
	Now   func() time.Time
	Reset bool
}

// SetClockForTest 替换/恢复 goal 模块的时钟，返回一个还原函数（测试里 defer 掉）。
func SetClockForTest(c ClockForTest) (restore func()) {
	stampMu.Lock()
	defer stampMu.Unlock()
	prevNow, prevLast := nowStamp, lastStamp
	nowStamp = time.Now
	if c.Now != nil {
		nowStamp = c.Now
	}
	if c.Reset {
		lastStamp = time.Time{}
	}
	return func() {
		stampMu.Lock()
		defer stampMu.Unlock()
		nowStamp, lastStamp = prevNow, prevLast
	}
}

func nextUpdatedAt(prev time.Time) time.Time {
	stampMu.Lock()
	defer stampMu.Unlock()

	now := nowStamp()
	base := now
	if lastStamp.After(base) {
		base = lastStamp
	}
	if !base.After(prev) {
		base = prev
	}
	// 时钟没往前走（或恰好相等）就手动推进 1ns：语义仍是"更新时间"，但可依赖。
	// 用循环而不是 if：极端情况下 prev 与 lastStamp 相差很远时也能收敛到严格大于。
	for !base.After(prev) || !base.After(lastStamp) {
		base = base.Add(time.Nanosecond)
	}
	lastStamp = base
	return base
}
