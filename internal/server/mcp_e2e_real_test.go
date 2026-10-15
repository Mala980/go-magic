package server

import (
	"net/http"
	"testing"
	"time"
)

// 端到端实测：对**真实的** DeepWiki 远程 MCP 端点走一遍保存链路。
//
// 需要外网，默认跳过；用 -run TestE2ESaveRealRemoteMCP 显式开启。
// 这个测试的价值是回答"改动在真实网络下到底多快"——单测里的裸 TCP
// 挂死服务端只能证明"不再等超时"，不能证明正常路径的延迟量级。
func TestE2ESaveRealRemoteMCP(t *testing.T) {
	if testing.Short() {
		t.Skip("需要外网")
	}

	s, home := newMCPTestServer(t)

	start := time.Now()
	rec := doMCPRequest(t, s, http.MethodPut, "/api/mcp/servers/deepwiki",
		`{"url": "https://mcp.deepwiki.com/mcp", "transport": "sse"}`)
	elapsed := time.Since(start)

	t.Logf("保存返回耗时 = %v", elapsed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 保存本身必须即时：连接在后台
	if elapsed > 3*time.Second {
		t.Errorf("保存耗时 %v：不该同步等待远程握手", elapsed)
	}

	// 配置立即落盘
	if _, ok := readPersistedConfig(t, home).MCP.Servers["deepwiki"]; !ok {
		t.Error("配置没有落盘")
	}

	// 后台连接应该在合理时间内成功（真实端点正常时）
	deadline := time.Now().Add(40 * time.Second)
	var connected bool
	for time.Now().Before(deadline) {
		for _, v := range s.listMCPServers() {
			if v.Name == "deepwiki" && v.Connected {
				connected = true
			}
		}
		if connected {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !connected {
		t.Logf("后台连接未在 40s 内完成（可能网络受限）；失败原因: %q", s.mcpMgr.LastFailure("deepwiki"))
		return
	}

	tools, err := s.mcpMgr.ListToolsByServer("deepwiki")
	if err != nil {
		t.Fatalf("列工具失败: %v", err)
	}
	t.Logf("后台连接成功，发现 %d 个工具", len(tools))
	if len(tools) == 0 {
		t.Error("连接成功但没发现任何工具")
	}
}
