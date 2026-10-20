package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/magicwubiao/go-magic/pkg/log"
)

// autoSkillStateStatus 把状态目录名映射为加载时赋予的 SkillStatus。
//
// 键必须与 autoSkillStateDirs 完全一致（TestAutoSkillStateStatusCoversDirs 兜底），
// 避免"写入某个状态目录、加载侧却认不出"这类两侧口径漂移。
var autoSkillStateStatus = map[string]SkillStatus{
	"pending":  SkillStatusPending,
	"approved": SkillStatusApproved,
	"archived": SkillStatusArchived,
	"rejected": SkillStatusRejected,
}

// autoSkillMeta 是自动技能目录下 meta.json 中加载所需的字段子集。
type autoSkillMeta struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	PatternTools []string `json:"pattern_tools"`
	CreatedAt    string   `json:"created_at"`
}

// readAutoSkillMeta 读取 meta.json；缺失或损坏时返回 nil（调用方退化到 SKILL.md）。
func readAutoSkillMeta(dir string) *autoSkillMeta {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil
	}
	var meta autoSkillMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return &meta
}

// loadAutoSkillsInto 按状态目录把自动技能加载进 target，返回实际新增条数。
//
// 为什么需要它：自动技能实际落在 <autoSkillsDir>/<state>/<id>/SKILL.md，而
// loadSkillsInto 只扫 searchDirs 的**顶层**（auto_skills 恰好在下面一层）。
// 此前唯一的加载入口是 server 包里的未导出函数，由此产生两个后果：
//
//   - 所有不经过 server 的进程（CLI / agent / chat / gateway / bot 都走
//     skills.NewManager）从头到尾看不到任何自动技能；
//   - 每次 Reload()（install / upload / rollback 都会触发）都用一张不含自动技能
//     的表整体替换内存表 —— 已批准的自动技能表现为"重启即丢"，有时甚至不用重启。
//
// 调用方负责加锁与原子替换；本函数只做 IO 与解析，不碰 m.mu。
func (m *Manager) loadAutoSkillsInto(target map[string]*Skill, autoDir string) int {
	if autoDir == "" || target == nil {
		return 0
	}
	loaded := 0

	for _, state := range autoSkillStateDirs {
		status, ok := autoSkillStateStatus[state]
		if !ok {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(autoDir, state))
		if err != nil {
			continue // 状态目录可能尚未创建
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "auto-") {
				continue
			}
			if m.loadAutoSkillDir(target, filepath.Join(autoDir, state, entry.Name()), status) {
				loaded++
			}
		}
	}

	// 兼容历史数据：顶层 auto-* 目录没有状态子目录，按 pending 处理。
	// 四个状态目录名都不以 "auto-" 开头，不会在这里被重复加载。
	if entries, err := os.ReadDir(autoDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "auto-") {
				continue
			}
			if m.loadAutoSkillDir(target, filepath.Join(autoDir, entry.Name()), SkillStatusPending) {
				loaded++
			}
		}
	}

	return loaded
}

// loadAutoSkillDir 加载单个自动技能目录，成功登记返回 true。
//
// 元数据以 SKILL.md 的 frontmatter 为准（generateSkillMarkdown 自「第四轮补记」起
// 会写全 name/description/version/tags/tools），meta.json 只在字段缺失时兜底 ——
// 文件是技能内容的权威来源，两处冲突时不反过来覆盖文件。
func (m *Manager) loadAutoSkillDir(target map[string]*Skill, dir string, status SkillStatus) bool {
	skillMd := filepath.Join(dir, "SKILL.md")
	meta := readAutoSkillMeta(dir)

	var skill *Skill
	if _, err := os.Stat(skillMd); err == nil {
		skill = m.loadSkillFromFile(skillMd)
	}
	if skill == nil {
		if meta == nil {
			return false // 既没有可解析的 SKILL.md，也没有 meta.json
		}
		skill = &Skill{}
		if data, err := os.ReadFile(skillMd); err == nil {
			skill.Content = string(data)
		}
	}

	// SKILL.md 没有 frontmatter 时（"第四轮补记"之前生成的历史技能），
	// loadSkillFromFile 会退化用**目录名**当技能名、用 "Markdown skill" 当描述。
	// 此时 meta.json 才是唯一可信来源，必须覆盖这两个退化值。
	degraded := skill.Name == filepath.Base(dir)

	if meta != nil {
		if degraded && meta.Name != "" {
			skill.Name = meta.Name
		}
		if skill.Name == "" {
			skill.Name = meta.Name
		}
		if (degraded || skill.Description == "") && meta.Description != "" {
			skill.Description = meta.Description
		}
		if len(skill.Tools) == 0 {
			skill.Tools = meta.PatternTools
		}
		if skill.Author == "" {
			skill.Author = "cortex-auto"
		}
		if meta.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil {
				skill.InstalledAt = t
			}
		}
	}
	if skill.Name == "" {
		skill.Name = filepath.Base(dir) // 最后兜底：目录名
	}
	if skill.Version == "" {
		skill.Version = "1.0.0"
	}

	// 冲突处理：同名技能已存在且**非自动来源**时保留后者。
	// 自动沉淀是兜底，不应盖掉用户手写 / 内置 / registry 安装的技能。
	if existing, ok := target[skill.Name]; ok {
		if existing.Source != SkillSourceAuto {
			log.Warnf("auto skill %q (%s) is shadowed by an existing %s skill of the same name; keeping the latter",
				skill.Name, dir, existing.Source)
		}
		// 同一技能同时出现在两个状态目录属历史脏数据：先加载者优先（状态目录顺序即优先级）。
		return false
	}

	skill.Source = SkillSourceAuto
	skill.Status = status
	skill.Dir = dir
	target[skill.Name] = skill
	return true
}

// LoadAutoSkillsFrom 从指定目录增量登记自动技能（不触碰其它来源的技能）。
//
// 供两类调用方使用：
//   - 历史上写在别处的 auto_skills 目录（server 的兼容扫描）；
//   - 运行期切换 autoSkillsDir 之后补登记。
func (m *Manager) LoadAutoSkillsFrom(dir string) int {
	if dir == "" {
		return 0
	}

	// 只把非自动技能作为冲突基线：同名自动技能应被新目录的内容刷新，
	// 而不是因为"内存里已有一份"就被跳过。
	m.mu.RLock()
	baseline := make(map[string]*Skill, len(m.skills))
	for name, sk := range m.skills {
		if sk.Source != SkillSourceAuto {
			baseline[name] = sk
		}
	}
	m.mu.RUnlock()

	loaded := m.loadAutoSkillsInto(baseline, dir)

	m.mu.Lock()
	for name, sk := range baseline {
		if sk.Source == SkillSourceAuto {
			m.skills[name] = sk
		}
	}
	m.mu.Unlock()
	return loaded
}
