package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/internal/skills/parser"
)

// TestGeneratedAutoSkillHasFrontmatter 保证自动生成的 SKILL.md 是一份合法技能
// 文件：带 frontmatter，且 name/description/tags/tools 都能被加载器解析出来。
//
// 此前生成的正文**完全没有 frontmatter**，落盘后谁按 SKILL.md 加载都只能拿到
// 目录名当技能名、默认描述当描述，tags/tools 为空——与仓库内其余技能口径不一致。
func TestGeneratedAutoSkillHasFrontmatter(t *testing.T) {
	base := t.TempDir()
	creator := NewEnhancedAutoCreator(base)

	pattern := Pattern{
		Name:         "Pattern 3",
		Description:  "Search the repo, then read the matching file. Handles \"quoted\" and: colon cases.",
		Tools:        []string{"search_in_files", "read_file"},
		ToolSequence: []string{"search_in_files", "read_file", "search_in_files"},
		Frequency:    5,
		Confidence:   0.9,
		ExampleTasks: []string{"find where the config is parsed"},
	}
	if err := creator.GenerateSkillFromPattern(pattern); err != nil {
		t.Fatalf("GenerateSkillFromPattern: %v", err)
	}

	var skillMdPath string
	err := filepath.Walk(filepath.Join(base, "auto_skills", "pending"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "SKILL.md" {
			skillMdPath = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if skillMdPath == "" {
		t.Fatal("no SKILL.md generated")
	}

	raw, err := os.ReadFile(skillMdPath)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	fm, body, err := parser.ParseYAMLFrontmatter(string(raw))
	if err != nil {
		t.Fatalf("ParseYAMLFrontmatter: %v", err)
	}
	if fm["name"] != "Automated Pattern 3" {
		t.Errorf("name = %v, want Automated Pattern 3", fm["name"])
	}
	// frontmatter 里的引号会被归一成单引号（解析器不处理转义，见 skillFrontmatterValue），
	// 正文（body）则保留原始文本。
	wantDescription := "Search the repo, then read the matching file. Handles 'quoted' and: colon cases."
	if fm["description"] != wantDescription {
		t.Errorf("description = %v, want %v", fm["description"], wantDescription)
	}
	if !strings.Contains(body, pattern.Description) {
		t.Errorf("body must keep the original description text")
	}
	if fm["version"] != "1.0.0" || fm["author"] != "cortex-auto" {
		t.Errorf("version/author = %v/%v", fm["version"], fm["author"])
	}
	tags := extractStringSlice(fm, "tags")
	if len(tags) != 1 || tags[0] != "auto-generated" {
		t.Errorf("tags = %v, want [auto-generated]", tags)
	}
	// 顺序保留 + 去重
	tools := extractStringSlice(fm, "tools")
	want := []string{"search_in_files", "read_file"}
	if len(tools) != len(want) || tools[0] != want[0] || tools[1] != want[1] {
		t.Errorf("tools = %v, want %v", tools, want)
	}
	if len(body) == 0 {
		t.Error("body must not be empty")
	}

	// 端到端：同一个加载器读这份文件，元数据不应退化。
	skill := (&Manager{}).loadSkillFromContent(string(raw), "auto-Pattern-3")
	if skill.Name != "Automated Pattern 3" {
		t.Errorf("loader name = %q", skill.Name)
	}
	if len(skill.Tools) != 2 {
		t.Errorf("loader tools = %v", skill.Tools)
	}
}

// TestSkillFrontmatterValueIsSingleLine 保证引号/换行不会破坏 frontmatter。
func TestSkillFrontmatterValueIsSingleLine(t *testing.T) {
	got := skillFrontmatterValue("line1\nline2 \"quoted\"  extra   spaces")
	if want := `"line1 line2 'quoted' extra spaces"`; got != want {
		t.Errorf("skillFrontmatterValue = %s, want %s", got, want)
	}
}
