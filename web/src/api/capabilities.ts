import { request } from './client'

// 会话能力清单：输入框的 /skill、/mcp 候选面板据此渲染。
//
// 为什么不让前端自己拼：MCP 工具名是动态的（mcp_<server>_<tool>），且只有
// **已连接**的服务器此刻才真的能被调用。后端 /capabilities 已经过滤好了
// 可执行清单，前端直接用，避免给用户展示"看起来有、点了不生效"的项。
export interface CapabilitySkill {
  name: string
  description: string
}

export interface CapabilityMCPTool {
  name: string
  description?: string
  // 注册进工具表的全名（mcp_<server>_<tool>）。仅用于展示/排查；
  // /mcp 指令的参数是「服务器名 + 工具名」两段（见 chat_directives.go
  // 的 renderMCPDirective），不是这个全名。
  full_name: string
}

export interface CapabilityMCPServer {
  name: string
  transport?: string
  connected: boolean
  tools: CapabilityMCPTool[]
}

export interface SessionCapabilities {
  skills: CapabilitySkill[]
  mcp_servers: CapabilityMCPServer[]
}

export async function getSessionCapabilities(sessionId: string): Promise<SessionCapabilities> {
  const res = await request<SessionCapabilities>(`/sessions/${sessionId}/capabilities`)
  return {
    skills: res.skills || [],
    mcp_servers: res.mcp_servers || [],
  }
}
