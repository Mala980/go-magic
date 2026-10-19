package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/magicwubiao/go-magic/internal/cortex"
	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// 分身（profile）的文件约定：
//
//	<magicHome>/cortex/{SOUL.md,USER.md,USER.json}       ← default 分身 = 正在运行的这套数据
//	<magicHome>/profiles/<name>/{SOUL.md,USER.md,USER.json}
//
// 路径一律由 appconfig.ProfileDir 裁决（见 pkg/config/profile.go），不要在
// 这里自己拼 "profiles/<name>" —— 历史 bug 就是这么来的：面板写
// profiles/<p>/user.md，运行时却读 cortex/USER.md，保存成功但对话毫无变化。

// profileRoot 返回所有分身目录的父目录。
func (s *Server) profileRoot() string {
	return filepath.Join(s.magicHome, "profiles")
}

// activeProfile 返回本次进程实际生效的分身名。
func (s *Server) activeProfile() string {
	return s.cfgSnapshot().ActiveProfile()
}

// profilePaths 解析分身的人设文件路径；名字非法时返回 error（调用方回 400）。
func (s *Server) profilePaths(name string) (dir, soul, userMD, userJSON string, err error) {
	dir = appconfig.ProfileDir(s.magicHome, name)
	if dir == "" {
		return "", "", "", "", fmt.Errorf("invalid profile name: %q", name)
	}
	return dir,
		filepath.Join(dir, "SOUL.md"),
		filepath.Join(dir, "USER.md"),
		filepath.Join(dir, "USER.json"),
		nil
}

// handleProfiles 处理 /api/profiles（列表 / 创建）。
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, map[string]interface{}{"profiles": s.scanProfiles()})
	case http.MethodPost:
		s.createProfile(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		CloneFromDefault bool   `json:"clone_from_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if !appconfig.ValidProfileName(name) {
		http.Error(w, "Invalid profile name: use letters, digits, '-', '_' or '.' (no path separators)", http.StatusBadRequest)
		return
	}
	if name == appconfig.DefaultProfileName {
		http.Error(w, `Profile "default" is built in and cannot be created`, http.StatusBadRequest)
		return
	}

	dir, _, _, _, err := s.profilePaths(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		http.Error(w, "Profile already exists: "+name, http.StatusConflict)
		return
	} else if !os.IsNotExist(statErr) {
		http.Error(w, "Failed to inspect profile dir: "+statErr.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, "Failed to create profile dir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if req.CloneFromDefault {
		if err := s.cloneProfile(s.activeProfile(), name); err != nil {
			// 目录已经建出来了，但克隆失败：保留空分身并明确报错，比假装成功好。
			http.Error(w, "Profile created but clone failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	jsonResponse(w, map[string]interface{}{"ok": true, "name": name, "path": dir})
}

// cloneProfile 把 src 分身的人设/技能/凭据复制到 dst。
//
// 源目录同样走 ProfileDir：default 的 .env 在 magicHome 下、人设在 cortex 下，
// 不能假设三者同目录。
func (s *Server) cloneProfile(src, dst string) error {
	srcDir, srcSoul, _, _, err := s.profilePaths(src)
	if err != nil {
		return err
	}
	dstDir, dstSoul, dstUserMD, dstUserJSON, err := s.profilePaths(dst)
	if err != nil {
		return err
	}

	// .env：default 的凭据在 magicHome/.env，其它分身在自己的目录里。
	envSrc := filepath.Join(srcDir, ".env")
	if src == appconfig.DefaultProfileName {
		envSrc = filepath.Join(s.magicHome, ".env")
	}
	if err := copyFileIfExists(envSrc, filepath.Join(dstDir, ".env"), 0600); err != nil {
		return err
	}

	// skills/：与 scanProfiles 的 skill_count 口径保持一致 —— default 的技能
	// 在 <magicHome>/skills，分身自己的在 <profileDir>/skills。
	skillsSrc := filepath.Join(srcDir, "skills")
	if src == appconfig.DefaultProfileName {
		skillsSrc = filepath.Join(s.magicHome, "skills")
	}
	if info, statErr := os.Stat(skillsSrc); statErr == nil && info.IsDir() {
		if err := copyDir(skillsSrc, filepath.Join(dstDir, "skills")); err != nil {
			return err
		}
	}

	// 人设三件套。旧版本曾把 SOUL.md 写成小写 soul.md，读取时一并兜住。
	if err := copyFileIfExists(readableSoulPath(srcSoul), dstSoul, 0644); err != nil {
		return err
	}
	if err := copyFileIfExists(filepath.Join(srcDir, "USER.md"), dstUserMD, 0644); err != nil {
		return err
	}
	if err := copyFileIfExists(filepath.Join(srcDir, "USER.json"), dstUserJSON, 0644); err != nil {
		return err
	}
	return nil
}

// handleProfileByName 处理 /api/profiles/<name>[...]。
func (s *Server) handleProfileByName(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	name, action, _ := strings.Cut(rel, "/")

	if !appconfig.ValidProfileName(name) {
		http.Error(w, "Invalid profile name", http.StatusBadRequest)
		return
	}

	dir, soulPath, userMDPath, _, err := s.profilePaths(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch action {
	case "setup-command":
		jsonResponse(w, map[string]string{"command": fmt.Sprintf("magic --profile %s chat", name)})
		return

	case "switch":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.switchProfile(w, name, dir)
		return

	case "soul":
		s.handleProfileSoul(w, r, name, dir, soulPath)
		return

	case "user":
		s.handleProfileUser(w, r, dir, userMDPath)
		return

	case "preferences":
		s.handleProfilePreferences(w, r, dir)
		return
	}

	if strings.HasPrefix(action, "preferences/") && strings.HasSuffix(action, "/feedback") {
		key := strings.TrimSuffix(strings.TrimPrefix(action, "preferences/"), "/feedback")
		s.handlePreferenceFeedback(w, r, name, dir, key)
		return
	}

	if action != "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.deleteProfile(w, name, dir)
	case http.MethodPatch:
		s.renameProfile(w, r, name, dir)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) switchProfile(w http.ResponseWriter, name, dir string) {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Profile not found: "+name, http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to inspect profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// 环境变量优先级高于持久化配置：此时写 config.json 也不会生效，直接说清楚，
	// 别让用户以为切换成功了。
	if pinned := strings.TrimSpace(os.Getenv(appconfig.ProfileEnvVar)); pinned != "" && pinned != name {
		http.Error(w, "Profile is pinned by "+appconfig.ProfileEnvVar+"="+pinned+"; restart without it to switch", http.StatusConflict)
		return
	}

	if err := s.persistProfileSelection(name); err != nil {
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{
		"ok":               true,
		"name":             name,
		"restart_required": true,
	})
}

// persistProfileSelection 把 cfg.Profile 落盘（与其它配置写入走同一套锁）。
func (s *Server) persistProfileSelection(name string) error {
	s.acquireServerMu("")
	defer s.releaseServerMu()
	s.lockCfgForWrite()
	defer s.unlockCfgForWrite()
	s.cfg.Profile = name
	return s.persistConfig(true)
}

func (s *Server) deleteProfile(w http.ResponseWriter, name, dir string) {
	if name == appconfig.DefaultProfileName {
		http.Error(w, `Profile "default" is built in and cannot be deleted`, http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Profile not found: "+name, http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to inspect profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 删除正在生效的分身：先把选择切回 default 再删，避免 config.json 里留下
	// 一个不存在的 profile（下次启动时 cortex 会把它当合法名字继续用）。
	wasActive := s.activeProfile() == name
	if wasActive {
		if err := s.persistProfileSelection(appconfig.DefaultProfileName); err != nil {
			http.Error(w, "Failed to reset active profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := os.RemoveAll(dir); err != nil {
		http.Error(w, "Failed to delete profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"ok": true, "name": name, "was_active": wasActive})
}

func (s *Server) renameProfile(w http.ResponseWriter, r *http.Request, name, dir string) {
	var req struct {
		Name    string `json:"name"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		newName = strings.TrimSpace(req.Name)
	}
	if !appconfig.ValidProfileName(newName) || newName == appconfig.DefaultProfileName {
		http.Error(w, "Invalid new profile name", http.StatusBadRequest)
		return
	}
	if newName == name {
		jsonResponse(w, map[string]interface{}{"ok": true, "name": newName, "path": dir})
		return
	}

	newDir, _, _, _, err := s.profilePaths(newName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, statErr := os.Stat(newDir); statErr == nil {
		http.Error(w, "Profile already exists: "+newName, http.StatusConflict)
		return
	} else if !os.IsNotExist(statErr) {
		http.Error(w, "Failed to inspect target: "+statErr.Error(), http.StatusInternalServerError)
		return
	}

	wasActive := s.activeProfile() == name
	if err := os.Rename(dir, newDir); err != nil {
		http.Error(w, "Failed to rename profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if wasActive {
		if err := s.persistProfileSelection(newName); err != nil {
			http.Error(w, "Renamed but failed to update active profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	jsonResponse(w, map[string]interface{}{"ok": true, "name": newName, "path": newDir})
}

// handleProfileSoul 读写分身的 SOUL.md。
//
// 对"正在生效"的分身必须走 live SoulManager：内存里有一份内容，直接写盘会
// 被它下一次保存覆盖（丢更新），而且绕过大小闸/快照/区块淘汰这些保护。
func (s *Server) handleProfileSoul(w http.ResponseWriter, r *http.Request, name, dir, soulPath string) {
	live := s.liveSoulManager(dir)

	switch r.Method {
	case http.MethodGet:
		if live != nil {
			jsonResponse(w, map[string]interface{}{
				"content": live.GetSoul(),
				"exists":  true,
				"source":  "cortex",
			})
			return
		}
		data, path, err := readFirstExisting(soulPath, legacyLowerName(soulPath))
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, "Failed to read soul: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"content": string(data),
			"exists":  path != "",
			"source":  path,
		})
	case http.MethodPut:
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if live != nil {
			if err := live.SetSoul(req.Content); err != nil {
				// 内容超限且没有可回收区块时 SetSoul 会拒绝写入，保留原内容。
				http.Error(w, "Failed to save soul: "+err.Error(), http.StatusBadRequest)
				return
			}
			jsonResponse(w, map[string]bool{"ok": true})
			return
		}
		if err := writeFileAtomic(soulPath, []byte(req.Content), 0644); err != nil {
			http.Error(w, "Failed to save soul: "+err.Error(), http.StatusInternalServerError)
			return
		}
		removeLegacyIfDistinct(legacyLowerName(soulPath), soulPath)
		jsonResponse(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// liveSoulManager 当 dir 就是 cortex 当前使用的人设目录时返回其 SoulManager。
func (s *Server) liveSoulManager(dir string) *cortex.SoulManager {
	if s.cortexMgr == nil || s.cortexMgr.Soul == nil {
		return nil
	}
	activeDir := appconfig.ProfileDir(s.magicHome, s.activeProfile())
	if filepath.Clean(activeDir) != filepath.Clean(dir) {
		return nil
	}
	return s.cortexMgr.Soul
}

// userJSONDoc 是 USER.json 的磁盘格式（与 cortex.UserProfile 的导出结构一致）。
type userJSONDoc struct {
	Preferences map[string]*cortex.UserPreference `json:"preferences"`
	Interests   []string                          `json:"interests"`
	TechStack   []string                          `json:"tech_stack"`
}

// readUserJSON 读取分身的 USER.json；不存在时返回 (nil, os.ErrNotExist)。
func readUserJSON(path string) (*userJSONDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc userJSONDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", filepath.Base(path), err)
	}
	return &doc, nil
}

// handleProfileUser 读写分身的用户画像。
//
// 读：USER.json（权威）优先，缺失时退回解析 USER.md。
// 写：data 走 cortex.UserProfile（同时更新 JSON 与 md），content 仅覆盖 md。
func (s *Server) handleProfileUser(w http.ResponseWriter, r *http.Request, dir, userMDPath string) {
	switch r.Method {
	case http.MethodGet:
		content, _, err := readFirstExisting(userMDPath, legacyLowerName(userMDPath))
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, "Failed to read user profile: "+err.Error(), http.StatusInternalServerError)
			return
		}

		doc, jsonErr := readUserJSON(filepath.Join(dir, "USER.json"))
		if jsonErr != nil && !os.IsNotExist(jsonErr) {
			http.Error(w, jsonErr.Error(), http.StatusInternalServerError)
			return
		}

		data := s.parseUserMD(string(content))
		if doc != nil {
			// JSON 是权威存储：面板表单以它为准，md 只是人类可读视图。
			data = userDataFromDoc(doc)
		}
		jsonResponse(w, map[string]interface{}{
			"content": string(content),
			"exists":  len(content) > 0 || doc != nil,
			"data":    data,
		})
	case http.MethodPut:
		var req struct {
			Content string                 `json:"content"`
			Data    map[string]interface{} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		if req.Data == nil {
			// 兼容旧客户端：整段覆盖 md。注意它不会更新 USER.json，
			// 因此不会影响 prompt —— 面板走的是下面 data 分支。
			if err := writeFileAtomic(userMDPath, []byte(req.Content), 0644); err != nil {
				http.Error(w, "Failed to save user profile: "+err.Error(), http.StatusInternalServerError)
				return
			}
			removeLegacyIfDistinct(legacyLowerName(userMDPath), userMDPath)
			jsonResponse(w, map[string]bool{"ok": true})
			return
		}

		up, err := s.userProfileForWrite(dir)
		if err != nil {
			http.Error(w, "Failed to load user profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := up.UpdateProfileInfo(cortex.UserProfileInfo{
			Name:               mapString(req.Data, "name"),
			Role:               mapString(req.Data, "role"),
			CommunicationStyle: mapString(req.Data, "communication_style"),
			CodeStyle:          mapString(req.Data, "code_style"),
			TechStack:          mapStringSlice(req.Data, "tech_stack"),
			Interests:          mapStringSlice(req.Data, "interests"),
		}); err != nil {
			http.Error(w, "Failed to save user profile: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleProfilePreferences 返回分身的偏好列表（真实数据，不再是 mock）。
func (s *Server) handleProfilePreferences(w http.ResponseWriter, r *http.Request, dir string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	up, err := s.userProfileForRead(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			jsonResponse(w, map[string]interface{}{"preferences": []interface{}{}})
			return
		}
		http.Error(w, "Failed to read preferences: "+err.Error(), http.StatusInternalServerError)
		return
	}

	prefs := up.GetAllPreferences()
	out := make([]map[string]interface{}, 0, len(prefs))
	for _, p := range prefs {
		out = append(out, map[string]interface{}{
			"key":        p.Key,
			"value":      p.Value,
			"context":    p.Context,
			"confidence": p.Confidence,
			"source":     p.Source,
			"updated_at": p.UpdatedAt.Unix(),
		})
	}
	jsonResponse(w, map[string]interface{}{"preferences": out})
}

// handlePreferenceFeedback 记录"准 / 不准"反馈并真正调整置信度。
func (s *Server) handlePreferenceFeedback(w http.ResponseWriter, r *http.Request, name, dir, key string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if key == "" {
		http.Error(w, "Missing preference key", http.StatusBadRequest)
		return
	}
	var req struct {
		Accurate bool `json:"accurate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Profile not found: "+name, http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to inspect profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	up, err := s.userProfileForWrite(dir)
	if err != nil {
		http.Error(w, "Failed to load user profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := up.ApplyFeedback(key, req.Accurate); err != nil {
		if strings.Contains(err.Error(), "preference not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to save feedback: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"ok": true, "key": key, "accurate": req.Accurate})
}

// userProfileForRead 返回只读的画像视图：正在生效的分身直接复用 cortex 的
// 内存实例（避免读到写了一半的磁盘状态），其它分身从磁盘只读加载
// （缺文件时不会写默认模板）。
func (s *Server) userProfileForRead(dir string) (*cortex.UserProfile, error) {
	if up := s.liveUserProfile(dir); up != nil {
		return up, nil
	}
	up := cortex.NewUserProfile(dir)
	if err := up.LoadExisting(); err != nil {
		return nil, err
	}
	return up, nil
}

// userProfileForWrite 返回可写实例；优先复用 cortex 的活动实例，否则从磁盘加载
// （缺失时生成默认模板）。
func (s *Server) userProfileForWrite(dir string) (*cortex.UserProfile, error) {
	if up := s.liveUserProfile(dir); up != nil {
		return up, nil
	}
	up := cortex.NewUserProfile(dir)
	if err := up.Load(); err != nil {
		return nil, err
	}
	return up, nil
}

// liveUserProfile 当 dir 就是 cortex 正在使用的目录时返回其实例。
func (s *Server) liveUserProfile(dir string) *cortex.UserProfile {
	if s.cortexMgr == nil || s.cortexMgr.UserProfile == nil {
		return nil
	}
	if filepath.Clean(s.cortexMgr.UserProfile.Dir()) != filepath.Clean(dir) {
		return nil
	}
	return s.cortexMgr.UserProfile
}

// userDataFromDoc 把 USER.json 映射成面板表单结构。
func userDataFromDoc(doc *userJSONDoc) map[string]interface{} {
	prefValue := func(key string) string {
		if doc == nil || doc.Preferences == nil {
			return ""
		}
		if p, ok := doc.Preferences[key]; ok && p != nil {
			return p.Value
		}
		return ""
	}
	return map[string]interface{}{
		"name":                prefValue("Name"),
		"role":                prefValue("Role"),
		"communication_style": prefValue("Communication style"),
		"code_style":          prefValue("Code style"),
		"tech_stack":          nonNilStrings(doc.TechStack),
		"interests":           nonNilStrings(doc.Interests),
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func mapString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapStringSlice(m map[string]interface{}, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// legacyLowerName 返回同一目录下的小写文件名（soul.md / user.md）。
//
// 老版本的服务器把 SOUL.md / USER.md 写成小写：Linux 上大小写敏感，不兜住
// 老文件会让用户看到"人格/画像丢了"。Windows 上两者其实是同一个文件，
// 写回后 removeLegacyIfDistinct 会用 os.SameFile 判断，不会误删。
func legacyLowerName(path string) string {
	return filepath.Join(filepath.Dir(path), strings.ToLower(filepath.Base(path)))
}

// readableSoulPath 返回实际存在的 SOUL.md 路径（优先大写，其次旧的小写）。
func readableSoulPath(soulPath string) string {
	if _, err := os.Stat(soulPath); err == nil {
		return soulPath
	}
	return legacyLowerName(soulPath)
}

// readFirstExisting 依次尝试 candidates，返回第一个读成功的路径。
func readFirstExisting(candidates ...string) ([]byte, string, error) {
	var lastErr error
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, p, nil
		}
		if !os.IsNotExist(err) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", os.ErrNotExist
}

// removeLegacyIfDistinct 删除旧的小写文件，但仅当它与新文件不是同一个文件
// （大小写不敏感的文件系统上它们是同一个 inode）。
func removeLegacyIfDistinct(legacy, current string) {
	if legacy == current {
		return
	}
	legacyInfo, err := os.Stat(legacy)
	if err != nil {
		return
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return
	}
	if os.SameFile(legacyInfo, currentInfo) {
		return
	}
	_ = os.Remove(legacy)
}

// copyFileIfExists 复制文件；源不存在时静默跳过。
func copyFileIfExists(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeFileAtomic(dst, data, perm)
}

// writeFileAtomic 先写同目录临时文件再 rename，避免崩溃留下半截文件。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

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
