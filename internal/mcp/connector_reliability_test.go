package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件锁定 2026-10-24 连接器可靠性修复的回归：
//   1. Streamable HTTP 的 Mcp-Session-Id 必须捕获并在后续请求回传；
//   2. tools/list 的 nextCursor 分页必须翻页（大 server 工具不再被截断）；
//   3. LastFailure 对"已断开的 client"要返回真实错误（消除状态盲区）；
//   4. stdio 子进程退出必须触发 OnDead（状态不再惰性）；
//   5. 链路失败后自动重连会跑起来（reconnect 字段不再是摆设）。

// --- 1. Mcp-Session-Id round-trip ---

func TestSSETransportSessionIDRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var gotSession string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotSession = r.Header.Get("Mcp-Session-Id")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-123")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	defer tr.Close()

	// 第一次请求：建立会话
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "initialize", ID: 1}); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// 第二次请求：必须带会话 id
	if _, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "ping", ID: 2}); err != nil {
		t.Fatalf("second request failed: %v", err)
	}

	mu.Lock()
	got := gotSession
	mu.Unlock()
	if got != "sess-123" {
		t.Fatalf("session id was not echoed back on follow-up requests: got %q", got)
	}
}

// --- 2. tools/list pagination ---

func TestDiscoverToolsFollowsPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		var params struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(req.Params, &params)

		var payload string
		switch params.Cursor {
		case "":
			payload = `{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
				`{"name":"a","description":"d","inputSchema":{"type":"object"}}],` +
				`"nextCursor":"page-2"}}`
		case "page-2":
			payload = `{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
				`{"name":"b","description":"d","inputSchema":{"type":"object"}}]}}`
		default:
			t.Fatalf("unexpected cursor %q", params.Cursor)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := &Client{
		name:      "paged",
		transport: mustSSE(t, srv.URL),
		tools:     make(map[string]Tool),
	}
	defer c.transport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.discoverTools(ctx); err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.tools) != 2 {
		t.Fatalf("expected both pages of tools, got %d: %v", len(c.tools), c.tools)
	}
	if _, ok := c.tools["a"]; !ok {
		t.Error("tool from page 1 missing")
	}
	if _, ok := c.tools["b"]; !ok {
		t.Error("tool from page 2 missing (pagination not followed?)")
	}
}

// --- 3. LastFailure covers dead (disconnected) clients ---

func TestLastFailureReportsDeadClient(t *testing.T) {
	mgr := NewManager()
	c := &Client{
		name:      "dead",
		transport: mustSSE(t, "http://127.0.0.1:1/sse"),
		tools:     make(map[string]Tool),
		// manager 留空：本用例只验证 LastFailure 口径，不触发自动重连。
	}
	c.connected = true
	mgr.mu.Lock()
	mgr.clients["dead"] = c
	mgr.mu.Unlock()

	if got := mgr.LastFailure("dead"); got != "" {
		t.Fatalf("healthy client should report no failure, got %q", got)
	}

	c.markDisconnected(errString2("stdio process exited"))

	if got := mgr.LastFailure("dead"); got == "" {
		t.Fatal("dead (disconnected but present) client must surface its last error via LastFailure")
	}
}

// --- 4. stdio child exit fires OnDead ---

func TestStdioOnDeadFiresOnChildExit(t *testing.T) {
	var cmd string
	var args []string
	if runtime.GOOS == "windows" {
		cmd, args = "cmd", []string{"/c", "exit", "0"}
	} else {
		cmd, args = "sh", []string{"-c", "exit 0"}
	}

	tr, err := NewStdioTransport(cmd, args, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}

	dead := make(chan error, 1)
	tr.OnDead(func(err error) { dead <- err })

	select {
	case err := <-dead:
		if err == nil {
			t.Fatal("OnDead should carry the terminal error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnDead never fired after child exit")
	}

	_ = tr.Close()
}

// --- 5. automatic reconnect runs after a link failure ---

func TestAutoReconnectScheduledAfterCallFailure(t *testing.T) {
	var up atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up.Load() == 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"t","description":"d","inputSchema":{"type":"object"}}]}}`))
		}
	}))
	defer srv.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()
	mgr.reconnectDelay = 10 * time.Millisecond // 测试加速：退避 10/20/30ms

	up.Store(1)
	if err := mgr.ConnectSSE("flaky", ServerConfig{Transport: "sse", URL: srv.URL}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	// server 掉线：callTool 失败 → markDisconnected → 调度自动重连
	up.Store(0)
	if _, err := mgr.CallTool(context.Background(), "flaky", "t", map[string]interface{}{}); err == nil {
		t.Fatal("expected call failure while server is down")
	}

	// 等 server 恢复，自动重连应当在几次退避内把它救回来
	go func() {
		time.Sleep(50 * time.Millisecond)
		up.Store(1)
	}()

	for i := 0; i < 100; i++ {
		time.Sleep(50 * time.Millisecond)
		info, err := mgr.GetServerInfo("flaky")
		if err == nil {
			if connected, _ := info["connected"].(bool); connected {
				return // 自动重连成功
			}
		}
	}
	t.Fatal("auto-reconnect did not restore the server within 5s")
}

func mustSSE(t *testing.T, url string) Transport {
	t.Helper()
	tr, err := NewSSETransport(url)
	if err != nil {
		t.Fatalf("NewSSETransport: %v", err)
	}
	return tr
}

type errString2 string

func (e errString2) Error() string { return string(e) }
