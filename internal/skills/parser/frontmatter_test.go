package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// 回归：ParseYAMLFrontmatter 的第一个返回值必须是 frontmatter，第二个才是正文。
// 旧实现把两者取反（正文当 frontmatter、frontmatter 当正文），使 DetectFormat 恒判 Magic、
// 内置技能 tags/tools 恒空、GetSkillContent 把元数据当正文返回。
func TestParseYAMLFrontmatterOrientation(t *testing.T) {
	content := "---\nname: demo\ndescription: hello\ntags: [a, b]\ntools: [read_file]\n---\n\n# Body\nsome text\n"

	fm, body, err := ParseYAMLFrontmatter(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm == nil {
		t.Fatal("frontmatter map must not be nil for a frontmatter-headed document")
	}
	if got := fm["name"]; got != "demo" {
		t.Fatalf("frontmatter[name] = %v, want demo", got)
	}
	if got := fm["description"]; got != "hello" {
		t.Fatalf("frontmatter[description] = %v, want hello", got)
	}
	if tags, ok := fm["tags"].([]string); !ok || len(tags) != 2 || tags[0] != "a" {
		t.Fatalf("frontmatter[tags] = %#v, want [a b]", fm["tags"])
	}
	if tools, ok := fm["tools"].([]string); !ok || len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("frontmatter[tools] = %#v, want [read_file]", fm["tools"])
	}
	if body != "# Body\nsome text" {
		t.Fatalf("body = %q, want %q", body, "# Body\nsome text")
	}
}

func TestParseYAMLFrontmatterNoFrontmatter(t *testing.T) {
	fm, body, err := ParseYAMLFrontmatter("# Just a body\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm != nil {
		t.Fatalf("frontmatter must be nil without a `---` header, got %#v", fm)
	}
	if body != "# Just a body\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseYAMLFrontmatterUnterminated(t *testing.T) {
	fm, body, err := ParseYAMLFrontmatter("---\nname: x\nno closing delimiter\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm != nil {
		t.Fatalf("unterminated frontmatter must not count as valid, got %#v", fm)
	}
	if body != "name: x\nno closing delimiter" {
		t.Fatalf("body = %q", body)
	}
}

// 回归：FormatMagic 曾经落到 parser.Parse 的 default 分支直接报错，
// 使 detection 结果形同虚设、上层退回简单解析并丢掉 version/author/license。
func TestParseFormatMagicFlatFrontmatter(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: demo\ndescription: a demo skill\nversion: 1.2.3\nauthor: someone\nlicense: MIT\ntags: [x, y]\ntools: [read_file, lint]\n---\n\n# Demo\n\nbody text\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewParser()
	res, err := p.Parse(skillDir)
	if err != nil {
		t.Fatalf("Parse must handle magic format, got: %v", err)
	}
	if res.Format != FormatMagic {
		t.Fatalf("format = %v, want %v", res.Format, FormatMagic)
	}
	if res.Name != "demo" {
		t.Fatalf("name = %q", res.Name)
	}
	if got := res.Data["version"]; got != "1.2.3" {
		t.Fatalf("version = %v, want 1.2.3", got)
	}
	if got := res.Data["author"]; got != "someone" {
		t.Fatalf("author = %v, want someone", got)
	}
	if got := res.Data["license"]; got != "MIT" {
		t.Fatalf("license = %v, want MIT", got)
	}
	if tags, ok := res.Data["tags"].([]string); !ok || len(tags) != 2 {
		t.Fatalf("tags = %#v, want 2 items", res.Data["tags"])
	}
	if tools, ok := res.Data["tools"].([]string); !ok || len(tools) != 2 {
		t.Fatalf("tools = %#v, want 2 items", res.Data["tools"])
	}
	if res.Content == "" || res.Content[0] != '#' {
		t.Fatalf("content must be the body, got %q", res.Content)
	}
}
