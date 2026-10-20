package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 本文件负责 `.env` 的**全部**语义：路径裁决、解析、写回、以及"真正注入进程环境"。
//
// 为什么需要它：面板的「环境变量」页一直可以写入 `~/.magic/.env`、可以删除、
// 列表里还会显示「有 .env」，但全仓库没有任何一处把它读进进程环境。于是
// `VISION_API_KEY`、`TAVILY_API_KEY`、`GITHUB_TOKEN` 这些**只通过 os.Getenv
// 读取**的能力（视觉分析、联网搜索、GitHub 工具）在面板里填完之后依然不可用,
// 用户能看到的唯一反馈是"保存成功"。这类"假功能"比缺功能更贵：它让人反复
// 重填、重装、怀疑 key 本身。
//
// 设计取舍——**真实环境变量永远优先**：
// `.env` 是持久化的便捷层，而 `FOO=bar magic serve` 或 systemd/docker 注入的
// 变量是运维的显式意图。若让 .env 覆盖真实环境，运维改一次就会被文件悄悄盖掉，
// 排查时会得到"我明明 export 了却没用"的结果。因此 Apply 只填**进程环境里还
// 不存在**的键，并把自己设过哪些键记账（EnvSet.applied）——撤销时只回滚这些
// 键，绝不 Unsetenv 别人的变量。

// EnvFileName 是凭据文件名，位于分身目录下。
const EnvFileName = ".env"

// EnvFilePath 返回指定分身凭据文件的路径。
//
// 布局与其它分身数据保持一致（见 ProfileDir）：
//   - default → <magicHome>/.env（历史位置，不能动，已有部署的 key 都在这里）
//   - 其它分身 → <magicHome>/profiles/<name>/.env
//
// 名字非法时返回 ""，调用方必须当作参数错误处理。
func EnvFilePath(magicHome, profile string) string {
	if profile == "" || profile == DefaultProfileName {
		return filepath.Join(magicHome, EnvFileName)
	}
	dir := ProfileDir(magicHome, profile)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, EnvFileName)
}

// envLine 是 .env 的一行原始形态，写回时用它保留顺序与注释。
type envLine struct {
	raw    string // 原样行（注释、空行）
	key    string // 非空表示这是一条 KEY=VALUE
	value  string
	isPair bool
}

// parseEnvContent 把 .env 文本切成有序行，并解析出键值。
//
// 支持：`KEY=VALUE`、`export KEY=VALUE`、`#` 注释、空行、CRLF、BOM、
// 值两端的单/双引号，以及未加引号值后面的行内注释（`KEY=xx  # note`）。
// 不支持的写法（多行续行 `\`）保持原样丢弃解析、但**写回时仍在**，
// 不会因为一次保存把用户手写的东西吃掉。
func parseEnvContent(content string) []envLine {
	content = strings.TrimPrefix(content, "\ufeff")
	var lines []envLine
	for _, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := raw
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, envLine{raw: line})
			continue
		}
		body := trimmed
		if rest, ok := trimExportPrefix(body); ok {
			body = rest
		}
		key, value, ok := strings.Cut(body, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			// 不是键值对（比如跨行续写的片段）：原样保留，不参与解析。
			lines = append(lines, envLine{raw: line})
			continue
		}
		lines = append(lines, envLine{raw: line, key: key, value: decodeEnvValue(value), isPair: true})
	}
	return lines
}

// trimExportPrefix 去掉 `export ` 前缀（shell 风格的 .env 很常见）。
func trimExportPrefix(s string) (string, bool) {
	const prefix = "export"
	if !strings.HasPrefix(s, prefix) {
		return s, false
	}
	rest := s[len(prefix):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return s, false
	}
	return strings.TrimSpace(rest), true
}

// decodeEnvValue 处理值的引号与行内注释。
func decodeEnvValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			inner := v[1 : len(v)-1]
			if v[0] == '"' {
				// 双引号内是唯一允许转义的地方，与常见 dotenv 实现一致。
				inner = strings.ReplaceAll(inner, `\"`, `"`)
				inner = strings.ReplaceAll(inner, `\n`, "\n")
				inner = strings.ReplaceAll(inner, `\\`, `\`)
			}
			return inner
		}
	}
	// 未加引号：`VALUE  # comment` 里的注释属于注释。
	// 只有"空白 + #"才算注释起点，`a#b` 这种 key 里的 # 要保留。
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// LoadEnvFile 读取并解析 .env；文件不存在时返回空 map 且 err 为 nil
// （"还没配过"不是错误，调用方不该为此写日志或报错）。
func LoadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	vars := make(map[string]string)
	for _, ln := range parseEnvContent(string(data)) {
		if ln.isPair {
			vars[ln.key] = ln.value
		}
	}
	return vars, nil
}

// WriteEnvFile 写回 .env，尽量保持原有顺序与注释。
//
// 保留顺序不是为了好看：用户会手工按"provider / tool / 其它"分组并写注释，
// 而 map 遍历顺序是随机的。早期实现每次都把整个文件重排，保存一个 key 会让
// 整份文件面目全非，diff 也没法看。
//
// 键被删除时，它所在的行一并删除。
func WriteEnvFile(path string, vars map[string]string) error {
	if path == "" {
		return fmt.Errorf("empty env file path")
	}
	var lines []envLine
	if data, err := os.ReadFile(path); err == nil {
		lines = parseEnvContent(string(data))
	} else if !os.IsNotExist(err) {
		return err
	}

	seen := make(map[string]bool, len(vars))
	out := make([]string, 0, len(lines)+len(vars))
	for _, ln := range lines {
		if !ln.isPair {
			out = append(out, ln.raw)
			continue
		}
		value, ok := vars[ln.key]
		if !ok || seen[ln.key] {
			// 已删除，或重复键（保留第一次出现的那个）。
			continue
		}
		seen[ln.key] = true
		out = append(out, ln.key+"="+encodeEnvValue(value))
	}

	// 新键按字典序追加，保证多次保存的结果稳定可比。
	var added []string
	for k := range vars {
		if !seen[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		out = append(out, k+"="+encodeEnvValue(vars[k]))
	}

	content := strings.Join(out, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeFileAtomic0600(path, []byte(content))
}

// encodeEnvValue 给需要保护的值加引号：含空白、#、引号或换行的值不加引号
// 读回来会被截断（`VAULE=a b` 只剩 "a"）。
func encodeEnvValue(v string) string {
	if strings.ContainsAny(v, " \t\n\r#\"'") {
		escaped := strings.ReplaceAll(v, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		escaped = strings.ReplaceAll(escaped, "\n", `\n`)
		return `"` + escaped + `"`
	}
	return v
}

// writeFileAtomic0600 以临时文件 + rename 的方式写盘，权限 0600。
//
// .env 里是明文凭据，必须 0600（早期实现用的是 os.WriteFile 0600，但非原子，
// 进程被杀会留下截断的文件——用户看到的就是"key 全没了"）。
func writeFileAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".env.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// 成功路径上 rename 已经把文件搬走，这里只是兜底清理。
		_ = os.Remove(tmpName)
	}()
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
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// EnvSet 记录"本进程从 .env 注入过哪些变量"，使撤销能精确回滚。
//
// 零值可用。所有方法都是并发安全的：面板的 POST/DELETE 与启动加载可能
// 在不同 goroutine 上撞车。
type EnvSet struct {
	mu      sync.Mutex
	applied map[string]string
}

// Apply 把 vars 注入进程环境，返回实际被设上的键。
//
// 已存在的**真实**环境变量不会被覆盖（见文件头注释）；但同一进程内多次
// Apply 时，之前由我们设的键允许更新（用户改了 .env 就该生效）。
func (e *EnvSet) Apply(vars map[string]string) []string {
	if len(vars) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied == nil {
		e.applied = make(map[string]string, len(vars))
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	applied := make([]string, 0, len(vars))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ours := e.applied[k]; !ours {
			if _, exists := os.LookupEnv(k); exists {
				// 真实环境变量优先：记下"不是我们设的"，以后也不撤销它。
				continue
			}
		}
		if err := os.Setenv(k, vars[k]); err != nil {
			continue
		}
		e.applied[k] = vars[k]
		applied = append(applied, k)
	}
	return applied
}

// SetKey 让单个键即时生效（面板保存一个变量后立刻可用，不必重启）。
//
// 真实环境变量已存在且不是我们设的，就**不动它**，并返回 false —— 调用方
// 据此可以提示"这个键由外部环境提供，面板里的值不会生效"。
func (e *EnvSet) SetKey(key, value string) bool {
	if key == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied == nil {
		e.applied = make(map[string]string)
	}
	if _, ours := e.applied[key]; !ours {
		if _, exists := os.LookupEnv(key); exists {
			return false
		}
	}
	if err := os.Setenv(key, value); err != nil {
		return false
	}
	e.applied[key] = value
	return true
}

// UnsetKey 撤销单个键：只有"由我们注入"的键才会被真正移除。
func (e *EnvSet) UnsetKey(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ours := e.applied[key]; !ours {
		return false
	}
	delete(e.applied, key)
	_ = os.Unsetenv(key)
	return true
}

// Clear 撤销本 EnvSet 注入过的全部变量（切换分身后重新加载时使用）。
func (e *EnvSet) Clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.applied {
		_ = os.Unsetenv(k)
	}
	e.applied = nil
}

// Managed 返回当前由本 EnvSet 注入的键集合（只读快照，供面板区分
// ".env 提供" 与 "外部环境提供"）。
func (e *EnvSet) Managed() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.applied))
	for k, v := range e.applied {
		out[k] = v
	}
	return out
}

// Has 判断某个键是否由我们注入。
func (e *EnvSet) Has(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.applied[key]
	return ok
}

// LoadEnvFile = 解析 + 注入的组合入口，供进程启动时使用。
//
// 返回实际注入的键数；路径为空或文件不存在时安静返回 0（这是正常状态）。
func (e *EnvSet) LoadEnvFile(path string) (int, error) {
	vars, err := LoadEnvFile(path)
	if err != nil {
		return 0, err
	}
	return len(e.Apply(vars)), nil
}
