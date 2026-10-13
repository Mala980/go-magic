package tool

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// toolNameAliases 记录模型常见的错误工具名到真实注册名的确定性映射。
// 这些映射无需模糊匹配即可给出唯一答案。
var toolNameAliases = map[string]string{
	"edit_file":          "file_edit",
	"str_replace_editor": "file_edit",
	"apply_patch":        "diff_patch",
	"read":               "read_file",
	"write":              "write_file",
	"shell":              "execute_command",
	"bash":               "execute_command",
}

// levenshtein 计算两个字符串的编辑距离（按 rune）。
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = minInt3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func minInt3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// normalizeToolName 统一大小写与分隔符，使 "Edit-File"、"edit file" 等都能对齐到 edit_file。
func normalizeToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(n)
	return n
}

// suggestToolNames 在已注册工具中找出与 name 最接近的候选，返回按距离排序的前 3 个。
// 只返回距离不超过阈值的候选，避免给出无关建议。
func suggestToolNames(name string, registered []string) []string {
	norm := normalizeToolName(name)
	if alias, ok := toolNameAliases[norm]; ok {
		for _, r := range registered {
			if r == alias {
				return []string{alias}
			}
		}
	}

	type cand struct {
		name string
		dist int
	}
	var cands []cand
	// 阈值：名字较长时允许更多差异，但上限为 3，避免过度宽松。
	threshold := len(norm) / 3
	if threshold < 2 {
		threshold = 2
	}
	if threshold > 3 {
		threshold = 3
	}
	for _, r := range registered {
		d := levenshtein(norm, normalizeToolName(r))
		if d <= threshold {
			cands = append(cands, cand{name: r, dist: d})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].name < cands[j].name
	})
	out := make([]string, 0, 3)
	for i := 0; i < len(cands) && i < 3; i++ {
		out = append(out, cands[i].name)
	}
	return out
}

// toolNotFoundError 生成带建议的"工具不存在"错误。
// 若存在候选，错误信息中会明确给出 "Did you mean ...?"，便于模型直接改用正确名称重试。
func toolNotFoundError(name string, registered []string) error {
	suggestions := suggestToolNames(name, registered)
	if len(suggestions) == 0 {
		return fmt.Errorf("tool %s not found", name)
	}
	return fmt.Errorf("tool %s not found. Did you mean %s? (use the exact registered tool name)", name, strings.Join(suggestions, " or "))
}

// longRunningPrefixes 是常见的长耗时命令前缀；命中时超时提示会建议改用后台进程。
var longRunningPrefixes = []string{
	"go test", "go build", "go vet", "go mod download",
	"npm install", "npm ci", "npm run build", "yarn", "pnpm install",
	"make", "cargo build", "cargo test", "mvn ", "gradle ",
	"docker build", "docker compose", "pip install",
}

// isLongRunningCommand 判断命令是否属于常见长耗时命令。
func isLongRunningCommand(command string) bool {
	c := strings.TrimSpace(command)
	// 去掉 "cd xxx &&" 之类的前缀，取最后一段真正执行的命令
	if idx := strings.LastIndex(c, "&&"); idx >= 0 {
		c = strings.TrimSpace(c[idx+2:])
	}
	if idx := strings.LastIndex(c, "\n"); idx >= 0 {
		c = strings.TrimSpace(c[idx+1:])
	}
	for _, p := range longRunningPrefixes {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}

// longRunningTimeoutHint 生成命令超时后的提示。
// 对长耗时命令，明确建议改用 process(action=run) 后台运行，再用 wait/log 轮询。
func longRunningTimeoutHint(command string, timeout time.Duration) string {
	if isLongRunningCommand(command) {
		return fmt.Sprintf("command exceeded %.0fs. This looks like a long-running build/test. Retry it with process(action=run, command=...) to run it in the background, then poll with process(action=wait, session_id=..., timeout=25) or process(action=log, session_id=...). Do not re-run it synchronously.", timeout.Seconds())
	}
	return fmt.Sprintf("command exceeded %.0fs. If it is expected to run longer, use process(action=run) to run it in the background instead of blocking.", timeout.Seconds())
}
