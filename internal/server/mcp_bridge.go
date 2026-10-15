package server

import (
	"time"

	"github.com/magicwubiao/go-magic/internal/mcpbridge"
	"github.com/magicwubiao/go-magic/pkg/log"
)

// 独立 MCP server → 工具注册表 的桥接。
//
// 背景:Agent Plugin 的 MCP 工具(ap_* 前缀)已经由 agentplugin 注入
// tool.Registry,但通过 config 的 "mcp.servers"、`magic mcp connect` 或
// Web UI /api/mcp 连接的独立 MCP server,工具此前只停留在 mcp.Manager,
// 从未进入 *tool.Registry —— 模型在 agent 循环中看不到、也调不到 mcp_* 工具。
//
// 这里把 s.mcpMgr 桥接到 s.toolReg,使独立 MCP server 的工具以
// mcp_<server>_<tool> 名称暴露给模型,与内置工具一样可被循环调用。

// initStandaloneMCP 在启动阶段连接 config 里配置的独立 MCP server,并把
// 发现到的工具注册进 Server 的工具注册表。单个 server 连接失败不会阻断
// 启动(与 Agent Plugin MCP 的隔离策略一致)。
//
// 该方法本身可能阻塞较久(stdio 拉起子进程 / SSE 握手,且 server 之间串行
// 连接),由 NewServer 放到后台 goroutine 执行;调用方负责通过 mcpInitDone
// (close 时机见 waitForMCPInit)协调。
func (s *Server) initStandaloneMCP() {
	if s == nil || s.mcpMgr == nil || s.toolReg == nil {
		return
	}
	cfg := s.cfgSnapshot()
	if cfg == nil || cfg.MCP == nil || len(cfg.MCP.Servers) == 0 {
		return
	}

	if err := mcpbridge.ConnectAndSync(s.mcpMgr, s.toolReg, cfg.MCP.Servers); err != nil {
		log.Warnf("[MCP] standalone MCP servers partially failed: %v", err)
	}
	log.Infof("[MCP] standalone MCP servers connected: %d, total tools in registry: %d",
		len(s.mcpMgr.ListServers()), s.toolReg.Count())
}

// waitForMCPInit 有界等待启动期后台 MCP 连接完成。mcpInitDone 未初始化
// (如部分测试手工构造 Server)或等待超时都直接放行,绝不无限阻塞业务:
// 超时后创建的 agent 只是暂时缺 mcp_* 工具,用户在连接器页手动重连/
// 刷新工具后,新会话即可拿到完整 schema。
func (s *Server) waitForMCPInit(maxWait time.Duration) {
	if s == nil || s.mcpInitDone == nil {
		return
	}
	select {
	case <-s.mcpInitDone:
	case <-time.After(maxWait):
		log.Warnf("[MCP] startup MCP init still running after %v; creating agent without waiting", maxWait)
	}
}

// syncMCPServerToRegistry 把单个 MCP server 的工具刷新到注册表
// (connect / reconnect / tools refresh 后调用)。
func (s *Server) syncMCPServerToRegistry(serverName string) {
	if s == nil || s.mcpMgr == nil || s.toolReg == nil {
		return
	}
	mcpbridge.SyncServerToRegistry(s.mcpMgr, s.toolReg, serverName)
}

// removeMCPServerTools 从注册表移除某个 server 注册的全部 mcp_* 工具
// (disconnect / delete 后调用,避免模型继续调用已断开的工具)。
func (s *Server) removeMCPServerTools(serverName string) {
	if s == nil || s.toolReg == nil {
		return
	}
	mcpbridge.RemoveServerTools(s.toolReg, serverName)
}
