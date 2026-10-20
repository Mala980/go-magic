package parser

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillFormat represents the format type of a skill
type SkillFormat string

const (
	FormatOpenClaw SkillFormat = "openclaw"
	FormatHermes   SkillFormat = "hermes"
	FormatMagic    SkillFormat = "magic"
	FormatUnknown  SkillFormat = "unknown"
)

// ParseResult holds the result of parsing a skill
type ParseResult struct {
	Format         SkillFormat
	Name           string
	Data           map[string]interface{}
	Content        string
	CodeFiles      map[string]string // filename -> content
	RawFrontmatter string
}

// ParseYAMLFrontmatter extracts YAML frontmatter from markdown content
// Returns the frontmatter map and the remaining content
//
// 方向约定（所有调用方都按此约定使用，包括 HermesParser / OpenClawParser /
// DetectFormat / loadSkillFromContent / GetSkillContent）：
//
//	第一个返回值 = frontmatter（YAML 头），第二个返回值 = 正文 body。
func ParseYAMLFrontmatter(content string) (map[string]interface{}, string, error) {
	content = strings.TrimPrefix(content, "\xef\xbb\xbf") // Remove BOM if present

	if !strings.HasPrefix(content, "---") {
		return nil, content, nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		return nil, content, nil
	}

	// 闭合分隔符 = 开头 "---" 之后，第一条去行尾空白后恰好为 "---" 的行。
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == "---" {
			end = i
			break
		}
	}

	if end == -1 {
		// 未闭合：不视为合法 frontmatter，整体按正文返回（保持原有行为）
		return nil, strings.TrimSpace(strings.Join(lines[1:], "\n")), nil
	}

	frontmatter := parseSimpleYAML(strings.Join(lines[1:end], "\n"))
	remainder := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))

	return frontmatter, remainder, nil
}

// yamlLine 是 parseSimpleYAML 处理的一行 frontmatter（已去掉空行与注释）。
type yamlLine struct {
	indent  int
	content string
}

// parseSimpleYAML 解析 frontmatter 里的简单 YAML。
//
// 支持：标量、行内数组 [a, b]、块数组（"- item"）、嵌套映射（缩进）、
// 以及 | / > 两种块标量。
//
// 这里曾经把所有缩进键**压平进同一个顶层 map**：`currentMap` 除了 result 之
// 外从不指向别处，于是
//
//	metadata:
//	  hermes:
//	    version: 2
//
// 会被解析成 {"metadata": (被丢弃), "hermes": ..., "version": "2"} ——
// 嵌套层消失、顶层 version 被覆盖，而 DetectFormat 正是靠
// `frontmatter["metadata"].(map[string]interface{})` 识别 Hermes 的，
// 该分支永远为假。现在按缩进递归构建真实的嵌套结构。
func parseSimpleYAML(content string) map[string]interface{} {
	result := make(map[string]interface{})

	var lines []yamlLine
	scanner := bufio.NewScanner(strings.NewReader(content))
	// frontmatter 里的 description 可能很长；默认 64KB 上限会静默截断整段，
	// 表现为"后面的键全丢了"。
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		lines = append(lines, yamlLine{indent: indent, content: trimmed})
	}

	idx := 0
	if idx < len(lines) {
		parseYAMLMap(lines, &idx, lines[0].indent, result)
	}
	return result
}

// parseYAMLMap 从 lines[*idx] 开始，解析一层映射到 out，直到缩进变浅。
func parseYAMLMap(lines []yamlLine, idx *int, indent int, out map[string]interface{}) {
	for *idx < len(lines) {
		line := lines[*idx]
		if line.indent < indent {
			return
		}
		if line.indent > indent {
			// 上一层没认领的更深缩进：跳过，避免死循环。
			*idx++
			continue
		}
		if strings.HasPrefix(line.content, "- ") || line.content == "-" {
			// 没有键的裸序列：本层不是映射，交由调用方处理。
			return
		}

		key, value, ok := splitYAMLKeyValue(line.content)
		if !ok {
			*idx++
			continue
		}
		*idx++

		switch {
		case value == "|" || value == ">":
			out[key] = collectBlockScalar(lines, idx, indent, value == ">")
		case value == "":
			// 空值：可能是嵌套映射、块数组，或者就是个空字符串。
			if *idx < len(lines) && lines[*idx].indent > indent {
				if strings.HasPrefix(lines[*idx].content, "- ") || lines[*idx].content == "-" {
					out[key] = parseYAMLSequence(lines, idx, lines[*idx].indent)
				} else {
					nested := make(map[string]interface{})
					parseYAMLMap(lines, idx, lines[*idx].indent, nested)
					out[key] = nested
				}
			} else {
				out[key] = ""
			}
		default:
			out[key] = decodeYAMLScalar(value)
		}
	}
}

// parseYAMLSequence 解析一个块数组（"- item"），返回 []string。
//
// 返回 []string 而不是 []interface{} 是既有契约：上层（Hermes/OpenClaw
// parser、origin_tools 的校验）都按 []string 断言。
func parseYAMLSequence(lines []yamlLine, idx *int, indent int) []string {
	items := []string{}
	for *idx < len(lines) {
		line := lines[*idx]
		if line.indent < indent {
			break
		}
		if line.indent > indent && len(items) > 0 {
			// 序列项的续行：拼到上一项（简单 YAML 里多见于多行字符串）。
			items[len(items)-1] += " " + line.content
			*idx++
			continue
		}
		if !strings.HasPrefix(line.content, "- ") && line.content != "-" {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(line.content, "-"))
		items = append(items, strings.Trim(decodeYAMLScalar(item).(string), `"`))
		*idx++
	}
	return items
}

// collectBlockScalar 收集 | / > 块标量的内容。
func collectBlockScalar(lines []yamlLine, idx *int, indent int, fold bool) string {
	var parts []string
	for *idx < len(lines) && lines[*idx].indent > indent {
		parts = append(parts, lines[*idx].content)
		*idx++
	}
	if fold {
		return strings.Join(parts, " ")
	}
	return strings.Join(parts, "\n")
}

// splitYAMLKeyValue 把 "key: value" 拆成两半；不构成键值对时返回 false。
func splitYAMLKeyValue(content string) (string, string, bool) {
	// 引号内的冒号不算分隔符：description: "a: b"。
	inSingle, inDouble := false, false
	sep := -1
	for i, r := range content {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if !inSingle && !inDouble {
				sep = i
			}
		}
		if sep >= 0 {
			break
		}
	}
	if sep < 0 {
		return "", "", false
	}
	key := strings.TrimSpace(content[:sep])
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(content[sep+1:]), true
}

// decodeYAMLScalar 把标量文本转成 Go 值：行内数组 → []string，
// 行内对象 → 原样字符串，其余 → 去引号的字符串。
func decodeYAMLScalar(value string) interface{} {
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
		if inner == "" {
			return []string{}
		}
		return parseInlineArray(inner)
	}
	return strings.Trim(value, `"`)
}

// parseInlineArray parses a comma-separated inline array
func parseInlineArray(content string) []string {
	var result []string
	depth := 0
	var current strings.Builder

	for _, ch := range content {
		switch ch {
		case '{', '[':
			depth++
			current.WriteRune(ch)
		case '}', ']':
			depth--
			current.WriteRune(ch)
		case ',':
			if depth == 0 {
				item := strings.TrimSpace(current.String())
				item = strings.Trim(item, "\"")
				if item != "" {
					result = append(result, item)
				}
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	// Don't forget the last item
	item := strings.TrimSpace(current.String())
	item = strings.Trim(item, "\"")
	if item != "" {
		result = append(result, item)
	}

	return result
}

// DetectFormat detects the skill format from a directory
func DetectFormat(skillDir string) (SkillFormat, error) {
	skillMdPath := filepath.Join(skillDir, "SKILL.md")

	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		return FormatUnknown, fmt.Errorf("failed to read SKILL.md: %w", err)
	}

	frontmatter, _, err := ParseYAMLFrontmatter(string(data))
	if err != nil {
		return FormatUnknown, err
	}

	if frontmatter == nil {
		// No frontmatter, check for structured hermes markers.
		// 按行前缀匹配（而非全文子串），避免正文偶然提到 "hermes"
		// 一词即被误判为 Hermes 格式；与 skill_import 的探测意图一致。
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "hermes_version:") || strings.HasPrefix(trimmed, "hermes:") {
				return FormatHermes, nil
			}
		}
		return FormatMagic, nil
	}

	// Detect based on frontmatter fields
	if _, ok := frontmatter["trigger_conditions"]; ok {
		return FormatOpenClaw, nil
	}

	if _, ok := frontmatter["hermes"]; ok {
		return FormatHermes, nil
	}

	// Check metadata.hermes
	if meta, ok := frontmatter["metadata"].(map[string]interface{}); ok {
		if _, ok := meta["hermes"]; ok {
			return FormatHermes, nil
		}
	}

	// Check for OpenClaw-style fields
	if _, ok := frontmatter["steps"]; ok {
		return FormatOpenClaw, nil
	}

	return FormatMagic, nil
}

// ReadSkillFiles reads all relevant files from a skill directory
func ReadSkillFiles(skillDir string) (map[string]string, error) {
	files := make(map[string]string)

	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read skill directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		name := entry.Name()

		// Include SKILL.md and code files
		if name == "SKILL.md" || IsCodeFile(ext) {
			path := filepath.Join(skillDir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %w", name, err)
			}
			files[name] = string(data)
		}
	}

	return files, nil
}

// IsCodeFile checks if a file extension is a code file
func IsCodeFile(ext string) bool {
	codeExts := []string{
		".go", ".py", ".js", ".ts", ".tsx", ".jsx",
		".sh", ".bash", ".zsh",
		".rb", ".java", ".kt", ".swift",
		".rs", ".c", ".cpp", ".h", ".hpp",
		".cs", ".php", ".lua", ".pl",
	}
	ext = strings.ToLower(ext)
	for _, codeExt := range codeExts {
		if ext == codeExt {
			return true
		}
	}
	return false
}

// GetCodeLanguage determines the programming language from file extension
func GetCodeLanguage(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".py":
		return "python"
	case ".js", ".ts", ".tsx", ".jsx":
		return "typescript"
	case ".go":
		return "go"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".rs":
		return "rust"
	case ".c", ".cpp", ".h", ".hpp":
		return "c"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".lua":
		return "lua"
	case ".pl":
		return "perl"
	default:
		return "unknown"
	}
}
