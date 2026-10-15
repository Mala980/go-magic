package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/magicwubiao/go-magic/internal/mcp"
	appconfig "github.com/magicwubiao/go-magic/pkg/config"
)

// 复现「添加连接器保存时页面一直卡住」。
//
// 前端 client.ts 的 request() 对每个请求设了 30s AbortController 超时，
// 后端 applyMCPServers 在 persist 之后会**同步** connectMCPServer，
// 而 connect 内部（buildClient → initialize）可能贴满 DefaultTimeout=30s。
// 两者时间接近时，用户看到的就是"保存转圈转到超时"。
//
// 这个测试用一个「TCP 能连上但永不回 HTTP 响应」的服务端来模拟那种最坏的
// 第三方 MCP 端点（防火墙丢包、反向代理挂住、服务器不认 MCP 但端口开着）。
func TestSaveServerBlocksOnSlowMCPHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("需要真实网络等待")
	}

	// 裸 TCP listener：接受连接后什么都不回，握手必然挂到超时。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			// 挂着不读不写：让客户端等满超时
			go func(c net.Conn) {
				<-time.After(60 * time.Second)
				c.Close()
			}(conn)
		}
	}()

	s, home := newMCPTestServer(t)
	s.cfg.MCP = &appconfig.MCPConfig{}

	url := "http://" + ln.Addr().String() + "/mcp"
	body := `{"url": "` + url + `", "transport": "sse"}`

	start := time.Now()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doMCPRequest(t, s, http.MethodPut, "/api/mcp/servers/stuck", body)
	}()

	// 保存必须**立即**返回：连接转移到后台后，这里不该再等握手。
	// 门槛取 3s——远超落盘 + 组装响应的量级，又能立刻抓住"又变回同步等待"。
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
		elapsed := time.Since(start)
		t.Logf("保存返回耗时 = %v（前端超时阈值 30s）", elapsed)
		if elapsed > 3*time.Second {
			t.Errorf("保存耗时 %v：连接又变回同步等待了，用户会以为卡死", elapsed)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		// 新语义：保存成功、连接中
		var resp struct {
			Success    bool     `json:"success"`
			Added      []string `json:"added"`
			Connecting []string `json:"connecting"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应不是 JSON: %v (%s)", err, rec.Body.String())
		}
		if !resp.Success || len(resp.Connecting) != 1 || resp.Connecting[0] != "stuck" {
			t.Errorf("响应未反映「已保存、连接中」: %s", rec.Body.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("保存请求 20s 仍未返回 —— handler 又同步阻塞在第三方 MCP 握手上")
	}

	// 关键断言：即使握手挂死，配置也必须已经落盘
	// （applyMCPServers 是先 persist 再 connect，这个顺序本身是对的）。
	persisted := readPersistedConfig(t, home)
	if _, ok := persisted.MCP.Servers["stuck"]; !ok {
		t.Error("配置没有落盘：用户会认为保存彻底失败")
	}
}

// 保存一个"连不上"的 server 时：HTTP 立即成功（配置已落盘），
// 但连接失败的原因必须随后能通过列表接口读到——否则用户面对一个
// 显示"未连接"的条目却看不出为什么。
func TestSaveFailureSurfacesActionableError(t *testing.T) {
	s, _ := newMCPTestServer(t)

	// 关掉的端口：连接必然被拒
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	rec := doMCPRequest(t, s, http.MethodPut, "/api/mcp/servers/refused",
		`{"url": "http://`+addr+`/mcp", "transport": "sse"}`)

	// 保存本身立即成功（异步连接的语义）
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 等后台连接失败并把原因写进 failures 表
	var lastErr string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if lastErr = s.mcpMgr.LastFailure("refused"); lastErr != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == "" {
		t.Fatal("后台连接失败后没有记录失败原因：列表里只会显示「未连接」而无从排查")
	}

	// 列表接口要把这个原因暴露给前端
	rec = doMCPRequest(t, s, http.MethodGet, "/api/mcp/servers", "")
	var list []mcpServerView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("列表不是 JSON: %v (%s)", err, rec.Body.String())
	}
	found := false
	for _, v := range list {
		if v.Name != "refused" {
			continue
		}
		found = true
		if v.Connected {
			t.Error("该 server 不应显示为已连接")
		}
		if v.LastError == "" {
			t.Error("列表没有暴露失败原因（last_error 为空）")
		}
	}
	if !found {
		t.Fatal("保存的 server 没有出现在列表里")
	}

	// 原因本身要可操作：说得出是连接被拒，而不是一句笼统失败
	low := strings.ToLower(lastErr)
	if !strings.Contains(low, "refused") && !strings.Contains(low, "connection") {
		t.Errorf("错误信息不可操作: %q", lastErr)
	}
}

// 保存慢 server 期间，列表接口必须仍然可用——这是之前修过的锁粒度问题，
// 这里补一条端到端回归，防止同步 connect 又被塞回锁里。
func TestListServersNotBlockedDuringSave(t *testing.T) {
	if testing.Short() {
		t.Skip("需要真实网络等待")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				<-time.After(60 * time.Second)
				c.Close()
			}(conn)
		}
	}()

	s, _ := newMCPTestServer(t)
	s.cfg.MCP = &appconfig.MCPConfig{
		Servers: map[string]mcp.ServerConfig{
			"existing": {Command: "npx", Transport: mcp.TransportStdio},
		},
	}

	saveStarted := make(chan struct{})
	go func() {
		close(saveStarted)
		doMCPRequest(t, s, http.MethodPut, "/api/mcp/servers/stuck",
			`{"url": "http://`+ln.Addr().String()+`/mcp", "transport": "sse"}`)
	}()
	<-saveStarted

	// 保存挂住期间轮询列表：必须在很短时间内返回
	deadline := time.After(5 * time.Second)
	for i := 0; i < 5; i++ {
		select {
		case <-deadline:
			t.Fatal("列表接口被保存操作阻塞（锁被握手占住）")
		default:
		}
		start := time.Now()
		rec := doMCPRequest(t, s, http.MethodGet, "/api/mcp/servers", "")
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("列表接口耗时 %v，疑似被保存操作阻塞", el)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("列表接口 status = %d", rec.Code)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
