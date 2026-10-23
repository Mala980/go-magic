package server

import (
	"encoding/json"
	"net/http"
)

// handlePluginProviders 处理 memory / context 引擎提供商的选择接口。
//
// 注意：这个入口与 Agent Plugins(internal/agentplugin,MCP 驱动)无关,
// 它是 provider 选择的历史入口,目前仅回显请求、不落盘。
func (s *Server) handlePluginProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var req struct {
			MemoryProvider string `json:"memory_provider"`
			ContextEngine  string `json:"context_engine"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"ok":              true,
			"memory_provider": req.MemoryProvider,
			"context_engine":  req.ContextEngine,
		})
		return
	}
	// GET
	jsonResponse(w, map[string]interface{}{
		"memory_provider": "",
		"memory_options":  []map[string]interface{}{},
		"context_engine":  "",
		"context_options": []map[string]interface{}{},
	})
}
