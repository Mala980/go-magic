package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/magicwubiao/go-magic/internal/cortex"
)

// handleCortexSoul 暴露 SOUL.md 的运维面：指标 / 快照 / 回滚 / 压缩。
//
// 为什么需要它：SOUL.md 是系统人格文件，自动进化会持续往里注入内容。
// 此前这些能力（含 P0-1b 的快照回滚）只在内部类型上存在，没有任何入口——
// 一旦人格文件被写坏，运维只能手工恢复。这里把"看指标、看快照、回滚、
// 压缩"四件事变成可执行的 API。
//
// 路由：
//
//	GET  /api/cortex/soul/stats        写入指标（refused / head_cuts_avoided / ...）
//	GET  /api/cortex/soul/snapshots    可用快照列表
//	POST /api/cortex/soul/rollback     回滚到快照，body: {"name":"latest"}
//	POST /api/cortex/soul/compact      立即压缩，回收注入内容
func (s *Server) handleCortexSoul(w http.ResponseWriter, r *http.Request) {
	if s.cortexMgr == nil {
		http.Error(w, "cortex manager not initialized", http.StatusServiceUnavailable)
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/api/cortex/soul/")
	switch action {
	case "stats":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		jsonResponse(w, s.cortexMgr.SoulStats())

	case "snapshots":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snaps := s.cortexMgr.SoulSnapshots()
		if snaps == nil {
			snaps = []cortex.SoulSnapshot{}
		}
		jsonResponse(w, snaps)

	case "rollback":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		if err := s.cortexMgr.RollbackSoul(req.Name); err != nil {
			http.Error(w, "rollback failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"ok": true, "name": req.Name, "stats": s.cortexMgr.SoulStats(),
		})

	case "compact":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dropped, err := s.cortexMgr.CompactSoulNow()
		if err != nil {
			http.Error(w, "compact failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"ok": true, "sections_dropped": dropped, "stats": s.cortexMgr.SoulStats(),
		})

	default:
		http.Error(w, "unknown soul action: "+action, http.StatusNotFound)
	}
}

// handleCortexSoulStats 兼容 /api/cortex/soul（无子路径）时返回指标。
func (s *Server) handleCortexSoulStats(w http.ResponseWriter, r *http.Request) {
	if s.cortexMgr == nil {
		http.Error(w, "cortex manager not initialized", http.StatusServiceUnavailable)
		return
	}
	jsonResponse(w, s.cortexMgr.SoulStats())
}

// handleCortexGEPA 暴露自进化引擎指标。
//
// 此前 evolutionLoop 在收敛后永久空转，而 is_converged 没有任何消费方——
// 线上"进化静默停了 3 周"完全无人察觉。这个接口是那类故障的探针：
// is_converged / convergence_latched / evolve_runs / consecutive_failures /
// last_evolve_at / last_improvement 一起看，就能判断进化是否还在推进。
func (s *Server) handleCortexGEPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cortexMgr == nil {
		http.Error(w, "cortex manager not initialized", http.StatusServiceUnavailable)
		return
	}
	jsonResponse(w, s.cortexMgr.GEPAStats())
}
