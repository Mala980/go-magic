package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/magicwubiao/go-magic/pkg/types"
)

// 对话内的斜杠指令展开（/skill、/mcp）。
//
// 为什么要做展开而不是"返回一句话"：聊天里输入 /skill <name> 的意图是
// **真的用上**这个技能（模型按它的定义干活），输入 /mcp <server> <tool> 的
// 意图是**真的调用**这个工具。此前 /api/commands/execute 只是把一句硬编码
// 字符串回给前端，模型根本看不到，用户看到的是"命令好像执行了但什么都没发生"。
//
// 展开的做法：把技能正文 / MCP 调用结果变成一条 text content part，拼在
// 用户正文之前送进模型。这样模型必然读到，且与历史落库、时间线渲染都走
// 同一条通路（不需要新的消息类型）。
//
// 一个必须绕开的坑：agent.RunConversationStreamWithMedia 在
// len(contentParts) > 0 时**完全忽略 input 纯文本**（见 agent.go 的
// "use content parts if available, otherwise fall back to plain text"）。
// 因此一旦为了塞指令而构造出 parts，用户正文就必须一并放进 parts，
// 否则"带指令的消息"会把用户的话吞掉。applyDirectiveParts 统一保证这点。

// directiveResult 是一次指令展开的结果。
type directiveResult struct {
	// Hit 表示正文里确实命中了 /skill 或 /mcp（决定是否改写 parts）。
	Hit bool
	// Text 是展开后的指令正文（含可用清单、技能定义或工具调用结果）。
	Text string
}

// expandChatDirectives 扫描用户正文里的 /skill、/mcp 指令并展开。
//
// 只识别这两类；其余 /xxx（如 /new、/help）原样保留，交给各自的既有通路处理，
// 避免把 CLI 语义误解成对话指令。
func (s *Server) expandChatDirectives(content string) directiveResult {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "/") {
		return directiveResult{}
	}

	var sections []string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
			continue
		}
		switch fields[0] {
		case "/skill", "/skills":
			sections = append(sections, s.renderSkillDirective(fields[1:]))
		case "/mcp":
			sections = append(sections, s.renderMCPDirective(fields[1:]))
		}
	}
	if len(sections) == 0 {
		return directiveResult{}
	}
	return directiveResult{Hit: true, Text: strings.Join(sections, "\n\n")}
}

// renderSkillDirective 把 /skill <name>（或无参 /skill）展开成给模型看的文本。
func (s *Server) renderSkillDirective(args []string) string {
	if s.skillMgr == nil {
		return "[skill] Skills are unavailable in this session."
	}
	// 无参：给出可用技能索引，让模型/用户知道能选什么。
	if len(args) == 0 {
		list := s.capabilitySkills()
		if len(list) == 0 {
			return "[skill] No skill name given, and no skills are currently available."
		}
		var sb strings.Builder
		sb.WriteString("[skill] No skill name given. Available skills:\n")
		for _, sk := range list {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", sk.Name, sk.Description))
		}
		return sb.String()
	}

	name := args[0]
	// 复用 Manager.GetSkillInfo：它返回剥离 frontmatter 后的正文，正是要注入
	// 给模型的内容（与 skill 工具的 invoke 行为一致）。
	desc, _, body, err := s.skillMgr.GetSkillInfo(name)
	if err != nil {
		return fmt.Sprintf("[skill] No skill named %q: %v. Use /skill with no argument to list what is available.", name, err)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[skill] The user explicitly invoked the skill %q. Apply it to this turn.\n", name))
	if desc != "" {
		sb.WriteString("Description: " + desc + "\n")
	}
	sb.WriteString("--- Skill: " + name + " ---\n")
	sb.WriteString(strings.TrimSpace(body))
	return sb.String()
}

// renderMCPDirective 把 /mcp <server> <tool> [json-args] 展开成给模型看的文本。
//
// 语义：
//   - 无参        → 列出已连接服务器与工具；
//   - 仅 server   → 列出该服务器的工具；
//   - server tool → 真正调用该工具并把结果注入（这是"really work"的核心）。
func (s *Server) renderMCPDirective(args []string) string {
	if s.mcpMgr == nil {
		return "[mcp] MCP is unavailable in this session."
	}
	switch len(args) {
	case 0:
		servers := s.capabilityMCPServers()
		if len(servers) == 0 {
			return "[mcp] No MCP servers are currently connected."
		}
		var sb strings.Builder
		sb.WriteString("[mcp] Connected MCP servers:\n")
		for _, srv := range servers {
			names := make([]string, 0, len(srv.Tools))
			for _, t := range srv.Tools {
				names = append(names, t.Name)
			}
			sort.Strings(names)
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", srv.Name, srv.Transport, strings.Join(names, ", ")))
		}
		return sb.String()
	case 1:
		server := args[0]
		tools, err := s.mcpMgr.ListToolsByServer(server)
		if err != nil {
			return fmt.Sprintf("[mcp] Server %q is not connected or has no tools: %v", server, err)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("[mcp] Tools on server %q:\n", server))
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
		return sb.String()
	}

	server, tool := args[0], args[1]
	// 可选 JSON 参数：第 3 段起重新拼回（用户可能写了带空格的 JSON）。
	rawArgs := strings.TrimSpace(strings.Join(args[2:], " "))
	callArgs := map[string]interface{}{}
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &callArgs); err != nil {
			return fmt.Sprintf("[mcp] Invalid JSON arguments for %s.%s: %v. Expected an object, e.g. /mcp %s %s {\"key\":\"value\"}",
				server, tool, err, server, tool)
		}
	}

	// 工具调用必须有超时：MCP server 是外部进程，不设上限会让整个回合被
	// 一个挂死的 server 拖到 timeout。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := s.mcpMgr.CallTool(ctx, server, tool, callArgs)
	if err != nil {
		return fmt.Sprintf("[mcp] Call to %s.%s failed: %v", server, tool, err)
	}
	pretty, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		pretty = []byte(fmt.Sprintf("%v", result))
	}
	return fmt.Sprintf("[mcp] Result of %s.%s:\n```json\n%s\n```", server, tool, string(pretty))
}

// applyDirectiveParts 把指令文本与用户正文/附件合成最终的 content parts。
//
// 关键点：只要有指令命中，就必须把用户正文也显式塞进 parts——因为上层一旦
// 看到 parts 非空就会丢弃 input 纯文本（见文件头注释）。没有指令时原样返回，
// 完全不影响既有路径。
func applyDirectiveParts(content string, parts []types.ContentPart, dr directiveResult) []types.ContentPart {
	if !dr.Hit {
		return parts
	}
	final := make([]types.ContentPart, 0, len(parts)+2)
	final = append(final, types.ContentPart{Type: "text", Text: dr.Text})
	if strings.TrimSpace(content) != "" {
		final = append(final, types.ContentPart{Type: "text", Text: content})
	}
	final = append(final, parts...)
	return final
}
