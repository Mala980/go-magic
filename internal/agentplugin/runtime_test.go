package agentplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magicwubiao/go-magic/internal/mcp"
	"github.com/magicwubiao/go-magic/internal/tool"
)

// fakeMCPServer 在内存管道上驱动一个最小 MCP server 替身:
//   - initialize:延迟 200ms 回复(用于验证按 id 匹配,而非按到达顺序);
//   - 无 id 的通知:不回复;
//   - tools/list:立即回复一个 echo 工具;
//   - tools/call:fail → isError=true;其他 → 两段 text 内容。
//
// 用管道而不是子进程:测试二进制在部分环境(Windows AV/文件锁)下会被拒绝
// fork/exec,管道同样覆盖协议分帧、id 匹配、通知不等响应这些被测逻辑。
type fakeMCPServer struct {
	tr *stdioPluginTransport
}

func newFakeTransport(t *testing.T) *fakeMCPServer {
	t.Helper()
	clientToServerR, clientToServerW := io.Pipe() // transport stdin → server 读
	serverToClientR, serverToClientW := io.Pipe() // server 写 → transport stdout

	tr := newPipeTransport(serverToClientR, clientToServerW)

	var writeMu sync.Mutex
	go func() {
		scanner := bufio.NewScanner(clientToServerR)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var req struct {
				JSONRPC string          `json:"jsonrpc"`
				Method  string          `json:"method"`
				ID      json.RawMessage `json:"id"`
				Params  json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.ID == nil || string(req.ID) == "null" {
				continue // 通知:不回复
			}
			switch req.Method {
			case "initialize":
				go func(id json.RawMessage) {
					time.Sleep(200 * time.Millisecond)
					writeResponse(&writeMu, serverToClientW, id, `{"protocolVersion":"2024-11-05"}`)
				}(req.ID)
			case "tools/list":
				writeResponse(&writeMu, serverToClientW, req.ID,
					`{"tools":[{"name":"echo","description":"echo tool","inputSchema":{"type":"object"}}]}`)
			case "tools/call":
				var p struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(req.Params, &p)
				if p.Name == "fail" {
					writeResponse(&writeMu, serverToClientW, req.ID,
						`{"isError":true,"content":[{"type":"text","text":"boom"}]}`)
				} else {
					writeResponse(&writeMu, serverToClientW, req.ID,
						`{"content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}`)
				}
			default:
				writeResponse(&writeMu, serverToClientW, req.ID, `{}`)
			}
		}
	}()

	t.Cleanup(func() {
		_ = tr.Close() // 关闭 stdin → scanner 结束 → 管道关闭 → readLoop 退出
		_ = serverToClientW.Close()
	})
	return &fakeMCPServer{tr: tr}
}

func writeResponse(mu *sync.Mutex, w io.Writer, id json.RawMessage, result string) {
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, result)
	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(w, resp)
}

// TestStdioTransport_NotificationAndSequentialRequests 回归:通知不得等待响应。
// 旧实现把 notifications/initialized 也当请求等响应行,会阻塞抢占 tools/list
// 的响应导致握手挂死;新实现通知只写不等。
func TestStdioTransport_NotificationAndSequentialRequests(t *testing.T) {
	f := newFakeTransport(t)
	tr := f.tr
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := tr.Send(ctx, &mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPCVersion, Method: "initialize", ID: 1}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	// server 对通知不回复:若实现仍阻塞等待,这里会在 ctx 超时处失败。
	if _, err := tr.Send(ctx, &mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPCVersion, Method: "notifications/initialized"}); err != nil {
		t.Fatalf("notification should not wait for response: %v", err)
	}
	resp, err := tr.Send(ctx, &mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPCVersion, Method: "tools/list", ID: 2})
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var res struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("parse tools: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want [echo]", res.Tools)
	}
}

// TestStdioTransport_ConcurrentRequestsMatchedByID 回归:响应必须按 id 匹配。
// server 对 initialize 延迟 200ms、对 tools/list 立即回复;若按到达顺序匹配,
// tools/list 会抢到 initialize 的响应,initialize 拿到 tools 列表。
func TestStdioTransport_ConcurrentRequestsMatchedByID(t *testing.T) {
	f := newFakeTransport(t)
	tr := f.tr
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		resp *mcp.JSONRPCResponse
		err  error
	}
	initCh := make(chan result, 1)
	listCh := make(chan result, 1)
	go func() {
		r, e := tr.Send(ctx, &mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPCVersion, Method: "initialize", ID: 1})
		initCh <- result{r, e}
	}()
	go func() {
		r, e := tr.Send(ctx, &mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPCVersion, Method: "tools/list", ID: 2})
		listCh <- result{r, e}
	}()

	var initRes, listRes result
	select {
	case initRes = <-initCh:
	case <-ctx.Done():
		t.Fatal("initialize timed out")
	}
	select {
	case listRes = <-listCh:
	case <-ctx.Done():
		t.Fatal("tools/list timed out")
	}
	if initRes.err != nil {
		t.Fatalf("initialize: %v", initRes.err)
	}
	if listRes.err != nil {
		t.Fatalf("tools/list: %v", listRes.err)
	}
	if !strings.Contains(string(initRes.resp.Result), "protocolVersion") {
		t.Errorf("initialize got wrong response: %s", initRes.resp.Result)
	}
	if !strings.Contains(string(listRes.resp.Result), "tools") {
		t.Errorf("tools/list got wrong response: %s", listRes.resp.Result)
	}
}

// TestRuntimeCallTool_IsErrorAndMultiText 回归:isError 必须映射为错误,
// 多段 text 内容须全部保留(旧实现只取第一段且忽略 isError)。
func TestRuntimeCallTool_IsErrorAndMultiText(t *testing.T) {
	f := newFakeTransport(t)
	r := NewRuntime(&Plugin{})
	r.conns["srv"] = &connection{transport: f.tr}

	_, err := r.CallTool(context.Background(), "srv", "fail", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected tool error boom, got %v", err)
	}

	out, err := r.CallTool(context.Background(), "srv", "echo", nil)
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if out != "hello\nworld" {
		t.Fatalf("out = %v, want hello\\nworld", out)
	}
}

// fakeRegistrar 记录注册/注销的工具名。
type fakeRegistrar struct {
	registered map[string]bool
}

func (f *fakeRegistrar) Register(t tool.Tool)   { f.registered[t.Name()] = true }
func (f *fakeRegistrar) Unregister(name string) { delete(f.registered, name) }

// TestRuntimeRegisterUnregisterTools 回归:reload/uninstall 时工具可被完整注销。
func TestRuntimeRegisterUnregisterTools(t *testing.T) {
	// 只验证注册表读写,不触发 transport。
	r := NewRuntime(&Plugin{})
	r.conns["srv"] = &connection{tools: []mcp.Tool{{Name: "echo"}}}

	reg := &fakeRegistrar{registered: map[string]bool{}}
	r.RegisterTools(reg, "demo")
	if !reg.registered["ap_demo_srv_echo"] {
		t.Fatalf("tool not registered: %v", reg.registered)
	}
	if len(r.registered) != 1 {
		t.Fatalf("runtime should track 1 registered tool, got %v", r.registered)
	}

	r.UnregisterTools(reg)
	if len(reg.registered) != 0 {
		t.Fatalf("registry should be empty after unregister, got %v", reg.registered)
	}
	if len(r.registered) != 0 {
		t.Fatalf("runtime tracking should be cleared, got %v", r.registered)
	}
}

func TestValidateEntryName(t *testing.T) {
	for _, name := range []string{"main", "srv-1", "weather.api", "a_1"} {
		if err := validateEntryName(name); err != nil {
			t.Errorf("entry name %q should be valid: %v", name, err)
		}
	}
	for _, name := range []string{"", strings.Repeat("a", 65), "a b", "srv/1", "具名"} {
		if err := validateEntryName(name); err == nil {
			t.Errorf("entry name %q should be invalid", name)
		}
	}
}

// TestUninstall_RejectsUnsafeNames 回归:卸载只接受单级目录名,防路径穿越。
func TestUninstall_RejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "..", ".", "a/b", "a\\b", "../../etc"} {
		if _, err := Uninstall(name); err == nil {
			t.Errorf("Uninstall(%q) should be rejected", name)
		}
	}
}
