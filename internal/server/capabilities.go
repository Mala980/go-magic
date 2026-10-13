package server

import (
	"net/http"
	"sort"

	"github.com/magicwubiao/go-magic/internal/mcp"
	"github.com/magicwubiao/go-magic/internal/skills"
)

// 会话能力清单接口。
//
//	GET /api/sessions/{id}/capabilities
//
// 为什么需要它：聊天输入框要让用户能"用上"技能与 MCP 工具（斜杠命令
// /skill、/mcp 以及输入框里的候选面板），前端必须知道**当前真的可用**的
// 技能名与 MCP 服务器/工具名。前端此前无从得知：
//   - /api/skills 返回的是管理页视角的全量技能（含 pending/rejected 的
//     自动技能），这些技能模型根本用不到，放进候选面板只会让用户选出
//     "看起来存在、实际不生效"的项；
//   - MCP 工具名是动态拼出来的（mcp_<server>_<tool>），前端拼不出
//     服务器与工具的正确组合。
//
// 因此这里只回两类**可执行的**清单：
//   - skills：非 pending/rejected 的技能（与 GetSkillsList 同一过滤口径，
//     保证候选面板与系统提示词里注入的技能索引一致）；
//   - mcp_servers：已连接的服务器及其工具名（未连接的服务器不会出现在
//     面板里——选中一个没连上的服务器只会让用户白点一次）。
//
// 会话 id 只用于鉴权路径一致性（能力清单是进程级的），但保留在路径里
// 让前端能在会话上下文里自然调用，也便于将来按会话做能力裁剪。
type sessionCapabilities struct {
	Skills     []capabilitySkill     `json:"skills"`
	MCPServers []capabilityMCPServer `json:"mcp_servers"`
}

type capabilitySkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type capabilityMCPServer struct {
	Name      string              `json:"name"`
	Transport string              `json:"transport,omitempty"`
	Connected bool                `json:"connected"`
	Tools     []capabilityMCPTool `json:"tools"`
}

type capabilityMCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// FullName 是注册进工具表的名字（mcp_<server>_<tool>）。前端直接把它
	// 当作斜杠命令 /mcp <full_name> 的参数，无需自己拼前缀。
	FullName string `json:"full_name"`
}

func (s *Server) handleSessionCapabilities(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = sessionID // 能力清单当前是进程级的；路径保留会话上下文以便将来按会话裁剪。
	jsonResponse(w, sessionCapabilities{
		Skills:     s.capabilitySkills(),
		MCPServers: s.capabilityMCPServers(),
	})
}

// capabilitySkills 返回对模型可见的技能（过滤掉待审核/已拒绝的自动技能，
// 与 skills.Manager.GetSkillsList 的过滤口径保持一致）。
func (s *Server) capabilitySkills() []capabilitySkill {
	if s.skillMgr == nil {
		return []capabilitySkill{}
	}
	list := s.skillMgr.List()
	out := make([]capabilitySkill, 0, len(list))
	for _, sk := range list {
		if sk == nil || sk.Name == "" {
			continue
		}
		if sk.Source == skills.SkillSourceAuto &&
			(sk.Status == skills.SkillStatusPending || sk.Status == skills.SkillStatusRejected) {
			continue
		}
		out = append(out, capabilitySkill{Name: sk.Name, Description: sk.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// capabilityMCPServers 返回已连接的 MCP 服务器及其工具。
// 工具全名用 mcp.ToolName 拼，与桥接进工具注册表的命名口径天然一致。
func (s *Server) capabilityMCPServers() []capabilityMCPServer {
	if s.mcpMgr == nil {
		return []capabilityMCPServer{}
	}
	out := make([]capabilityMCPServer, 0)
	for _, name := range s.mcpMgr.ListServers() {
		info, err := s.mcpMgr.GetServerInfo(name)
		if err != nil || info == nil {
			continue
		}
		connected, _ := info["connected"].(bool)
		if !connected {
			// 未连接的服务器不进候选面板：模型此刻也调不到它的工具。
			continue
		}
		transport, _ := info["transport"].(string)
		srv := capabilityMCPServer{
			Name:      name,
			Transport: transport,
			Connected: true,
			Tools:     []capabilityMCPTool{},
		}
		tools, err := s.mcpMgr.ListToolsByServer(name)
		if err == nil {
			for _, t := range tools {
				srv.Tools = append(srv.Tools, capabilityMCPTool{
					Name:        t.Name,
					Description: t.Description,
					FullName:    mcp.ToolName(name, t.Name),
				})
			}
			sort.Slice(srv.Tools, func(i, j int) bool { return srv.Tools[i].Name < srv.Tools[j].Name })
		}
		out = append(out, srv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
