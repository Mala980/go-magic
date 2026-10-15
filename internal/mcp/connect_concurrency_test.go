package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// slowMockServer 是一个可以拖慢响应速度的 Streamable HTTP MCP mock。
// delay 用于模拟真实世界里握手慢的 server（npx 拉包、网络往返）。
func slowMockServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}

		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		var result map[string]interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": ProtocolVersion,
				"protocolName":    ProtocolName,
				"serverInfo":      map[string]interface{}{"name": "slow-mock", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "mock_echo", "description": "echoes", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}
		default:
			result = map[string]interface{}{}
		}

		envelope := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}
		payload, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
}

// TestConnectHandshakeDoesNotBlockReaders 是锁粒度的核心回归。
//
// 旧实现 ConnectSSE/ConnectStdio 全程持有 Manager.mu 写锁，握手期间
// ListServers / ListToolsByServer / 断开 全部被挡在门外（最长 30s）。
// 拆锁后，慢连接在飞的这段时间里这些调用必须立即返回。
func TestConnectHandshakeDoesNotBlockReaders(t *testing.T) {
	slow := slowMockServer(t, 2*time.Second)
	defer slow.Close()
	fast := slowMockServer(t, 0)
	defer fast.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()

	if err := mgr.ConnectSSE("fast", ServerConfig{Transport: "sse", URL: fast.URL}); err != nil {
		t.Fatalf("fast server connect failed: %v", err)
	}

	connectErr := make(chan error, 1)
	go func() {
		connectErr <- mgr.ConnectSSE("slow", ServerConfig{Transport: "sse", URL: slow.URL})
	}()

	// 给慢连接一点时间真正进入握手阶段。
	time.Sleep(150 * time.Millisecond)

	// 握手仍在进行时读取：必须立刻返回，不能被写锁挡住。
	readStart := time.Now()
	servers := mgr.ListServers()
	if _, err := mgr.ListToolsByServer("fast"); err != nil {
		t.Fatalf("ListToolsByServer(fast) failed during slow connect: %v", err)
	}
	if elapsed := time.Since(readStart); elapsed > 500*time.Millisecond {
		t.Fatalf("reads blocked by in-flight connect: took %v (lock held across handshake?)", elapsed)
	}

	// 慢连接自身仍应成功。
	select {
	case err := <-connectErr:
		if err != nil {
			t.Fatalf("slow connect should still succeed, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slow connect never returned")
	}

	if len(servers) != 1 || servers[0] != "fast" {
		t.Fatalf("expected only the fast server to be registered mid-connect, got %v", servers)
	}
	if got := len(mgr.ListServers()); got != 2 {
		t.Fatalf("expected both servers connected afterwards, got %d", got)
	}
}

// TestReconnectDoesNotPanic 覆盖 Reconnect 里那个无配对的 client.mu.RUnlock()。
// 旧实现只要对已连接的 server 调 Reconnect 就会 panic("sync: RUnlock of
// unlocked RWMutex")，而 UI 的重连按钮正走这条路。
func TestReconnectDoesNotPanic(t *testing.T) {
	srv := slowMockServer(t, 0)
	defer srv.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()

	if err := mgr.ConnectSSE("mock", ServerConfig{Transport: "sse", URL: srv.URL}); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	if err := mgr.Reconnect("mock"); err != nil {
		t.Fatalf("reconnect failed: %v", err)
	}

	tools, err := mgr.ListToolsByServer("mock")
	if err != nil {
		t.Fatalf("server should be usable after reconnect: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected tools to be rediscovered after reconnect, got %d", len(tools))
	}

	// 未连接过的名字仍然是 not found，而不是 panic。
	if err := mgr.Reconnect("never-connected"); err == nil {
		t.Fatal("expected an error for an unknown server")
	}
}

// TestConnectConfiguredIsConcurrent 验证配置里的多台 server 是并发连接的。
//
// 不用计时断言(握手涉及多次往返，单台耗时不是"一个 delay"，计时容易假失败)，
// 改为让两个条目指向同一个 mock，由 mock 统计同时在飞的请求数：并发连接时
// 必然出现 ≥2 个重叠请求，串行实现则永远不会。
func TestConnectConfiguredIsConcurrent(t *testing.T) {
	var inFlight, maxInFlight int32
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()

		time.Sleep(300 * time.Millisecond)

		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		result := map[string]interface{}{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": ProtocolVersion,
				"protocolName":    ProtocolName,
				"serverInfo":      map[string]interface{}{"name": "concurrency-probe", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "mock_echo", "description": "echoes", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}
		}

		envelope := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}
		payload, _ := json.Marshal(envelope)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()

	if err := mgr.ConnectConfigured(map[string]ServerConfig{
		"a": {Transport: "sse", URL: srv.URL},
		"b": {Transport: "sse", URL: srv.URL},
	}); err != nil {
		t.Fatalf("ConnectConfigured failed: %v", err)
	}

	if got := len(mgr.ListServers()); got != 2 {
		t.Fatalf("expected 2 connected servers, got %d", got)
	}

	mu.Lock()
	peak := maxInFlight
	mu.Unlock()

	if peak < 2 {
		t.Fatalf("ConnectConfigured looks serial: peak concurrent requests was %d, expected >= 2", peak)
	}
}

// TestConnectConfiguredFailureOrderIsDeterministic 保证同样的失败集合产出
// 同样的错误串（并发写入后必须排序），避免日志/UI 每次抖动。
func TestConnectConfiguredFailureOrderIsDeterministic(t *testing.T) {
	mgr := NewManager()
	defer mgr.DisconnectAll()

	servers := map[string]ServerConfig{
		"zeta":  {Transport: "stdio", Command: "definitely-not-a-real-binary-zeta"},
		"alpha": {Transport: "stdio", Command: "definitely-not-a-real-binary-alpha"},
		"mid":   {Transport: "stdio", Command: "definitely-not-a-real-binary-mid"},
	}

	var first string
	for i := 0; i < 5; i++ {
		err := mgr.ConnectConfigured(servers)
		if err == nil {
			t.Fatal("expected an error when every server fails")
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("failure message is not deterministic:\nfirst: %s\nnow:   %s", first, err.Error())
		}
	}

	// 错误文本里含平台相关的失败细节，只断言三台 server 按名字升序出现。
	for _, name := range []string{"alpha", "mid", "zeta"} {
		if indexOf(first, name+": ") < 0 {
			t.Fatalf("failure message %q is missing %s", first, name)
		}
	}
	alphaIdx := indexOf(first, "alpha: ")
	midIdx := indexOf(first, "mid: ")
	zetaIdx := indexOf(first, "zeta: ")
	if !(alphaIdx < midIdx && midIdx < zetaIdx) {
		t.Fatalf("failures are not sorted by server name: %s", first)
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
