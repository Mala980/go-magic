package parser

import (
	"fmt"
	"os"
	"path/filepath"
)

// Parser is a unified parser that can detect and parse different skill formats
type Parser struct {
	HermesParser   *HermesParser
	OpenClawParser *OpenClawParser
}

// NewParser creates a new unified parser
func NewParser() *Parser {
	return &Parser{
		HermesParser:   NewHermesParser(),
		OpenClawParser: NewOpenClawParser(),
	}
}

// Parse parses a skill from a directory, auto-detecting the format
func (p *Parser) Parse(skillDir string) (*ParseResult, error) {
	// Detect format
	format, err := DetectFormat(skillDir)
	if err != nil {
		return nil, fmt.Errorf("failed to detect format: %w", err)
	}

	// Get the skill.md path
	skillMdPath := filepath.Join(skillDir, "SKILL.md")

	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SKILL.md: %w", err)
	}

	result := &ParseResult{
		Format: format,
		Data:   make(map[string]interface{}),
	}

	switch format {
	case FormatHermes:
		skill, err := p.HermesParser.Parse(skillDir)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Hermes skill: %w", err)
		}
		result.Name = skill.Name
		result.Content = skill.Content
		result.CodeFiles = skill.CodeFiles
		result.Data["description"] = skill.Description
		result.Data["version"] = skill.Version
		result.Data["author"] = skill.Author
		result.Data["tags"] = skill.Tags
		result.Data["trigger_conditions"] = skill.TriggerConditions
		result.Data["steps"] = skill.Steps
		// Copy remaining metadata
		for k, v := range skill.Metadata {
			result.Data[k] = v
		}

	case FormatOpenClaw:
		skill, err := p.OpenClawParser.Parse(skillDir)
		if err != nil {
			return nil, fmt.Errorf("failed to parse OpenClaw skill: %w", err)
		}
		result.Name = skill.Name
		result.Content = skill.Content
		result.CodeFiles = skill.CodeFiles
		// Extract all OpenClaw-specific fields into Data
		result.Data["description"] = skill.Description
		result.Data["version"] = skill.Version
		result.Data["author"] = skill.Author
		result.Data["license"] = skill.License
		result.Data["tags"] = skill.Tags
		result.Data["tools"] = skill.Tools
		result.Data["trigger_conditions"] = skill.TriggerConditions
		result.Data["steps"] = skill.Steps

	case FormatMagic:
		// 通用 Markdown 技能：frontmatter 只含扁平字段（name/description/version/author/
		// license/tags/tools），或者根本没有 frontmatter。
		//
		// 此前该分支落到 default 直接返回 "unsupported format"，于是 FormatMagic 这个
		// 检测结果形同虚设：调用方退回简单解析路径，version/author/license 被静默丢弃。
		frontmatter, remainder, ferr := ParseYAMLFrontmatter(string(data))
		if ferr != nil {
			return nil, fmt.Errorf("failed to parse frontmatter: %w", ferr)
		}

		name := filepath.Base(skillDir)
		if frontmatter != nil {
			if v, ok := frontmatter["name"].(string); ok && v != "" {
				name = v
			}
			for _, k := range []string{"description", "version", "author", "license"} {
				if v, ok := frontmatter[k]; ok {
					result.Data[k] = v
				}
			}
			for _, k := range []string{"tags", "tools"} {
				if v, ok := frontmatter[k]; ok {
					result.Data[k] = v
				}
			}
		}
		result.Name = name
		result.Content = remainder

	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	return result, nil
}
