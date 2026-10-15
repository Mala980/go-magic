package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 服务端拒绝某个协议版本时，客户端必须自动降级重试，而不是整台 server 连不上。
//
// 这条用例锁住的是一个真实的线上故障：客户端写死 protocolVersion=2026-07-28
// （属于"无握手"代），而它本身走的是 initialize 握手路径。握手代服务端会直接
// 回 400 UnsupportedProtocolVersion，导致所有这类服务端全部连不通。
func TestInitializeNegotiatesProtocolVersionDown(t *testing.T) {
	var attempts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		if req.Method == "initialize" {
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			attempts = append(attempts, params.ProtocolVersion)

			// 模拟一个只接受 2025-06-18 的服务端：比它新的版本一律报 400。
			// （首选 2025-11-25 会被拒，逼客户端降级。）
			if params.ProtocolVersion != "2025-06-18" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"server-error","error":` +
					`{"code":-32600,"message":"Bad Request: Unsupported protocol version: ` +
					params.ProtocolVersion + `. Supported versions: 2024-11-05, 2025-03-26, 2025-06-18"}}`))
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",` +
				`"serverInfo":{"name":"fake","version":"1.0"}}}`))
			return
		}

		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// tools/list
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[` +
			`{"name":"echo","description":"d","inputSchema":{"type":"object"}}]}}`))
	}))
	defer srv.Close()

	c := &Client{
		name:      "fake",
		config:    ServerConfig{URL: srv.URL, Transport: TransportSSE},
		tools:     make(map[string]Tool),
		timeout:   DefaultTimeout,
		connected: true,
	}
	tr, err := NewSSETransport(srv.URL)
	if err != nil {
		t.Fatalf("NewSSETransport: %v", err)
	}
	c.transport = tr
	defer tr.Close()

	if err := c.initialize(); err != nil {
		t.Fatalf("initialize 失败（应该自动降级成功）: %v", err)
	}

	if c.negotiatedVersion != "2025-06-18" {
		t.Errorf("negotiatedVersion = %q, want 2025-06-18", c.negotiatedVersion)
	}
	if len(attempts) < 2 {
		t.Fatalf("只尝试了 %v，应该先被拒绝再降级", attempts)
	}
	if attempts[len(attempts)-1] != "2025-06-18" {
		t.Errorf("最后一次尝试 = %q，want 2025-06-18（协商应逐步降级）", attempts[len(attempts)-1])
	}

	c.mu.RLock()
	_, hasTool := c.tools["echo"]
	c.mu.RUnlock()
	if !hasTool {
		t.Error("协商成功后没有发现工具")
	}
}

// 服务端接受首选版本时，不应该有多余的降级尝试。
func TestInitializeUsesPreferredVersionWhenAccepted(t *testing.T) {
	var attempts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req JSONRPCRequest
		_ = json.Unmarshal(body, &req)

		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			attempts = append(attempts, params.ProtocolVersion)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"` +
				params.ProtocolVersion + `","serverInfo":{"name":"fake","version":"1.0"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		}
	}))
	defer srv.Close()

	c := &Client{
		name:      "fake",
		config:    ServerConfig{URL: srv.URL, Transport: TransportSSE},
		tools:     make(map[string]Tool),
		timeout:   DefaultTimeout,
		connected: true,
	}
	tr, _ := NewSSETransport(srv.URL)
	c.transport = tr
	defer tr.Close()

	if err := c.initialize(); err != nil {
		t.Fatalf("initialize 失败: %v", err)
	}
	if len(attempts) != 1 {
		t.Errorf("尝试了 %v，首选版本被接受时不该重试", attempts)
	}
	// 首选版本必须落在握手代里（这是修复的核心）
	if attempts[0] != ProtocolVersion {
		t.Errorf("首选 = %q, want %q", attempts[0], ProtocolVersion)
	}
	if !strings.HasPrefix(ProtocolVersion, "2025") && !strings.HasPrefix(ProtocolVersion, "2024") {
		t.Errorf("ProtocolVersion = %q 不在握手代（2025-11-25 及更早）", ProtocolVersion)
	}
}

// unsupportedVersionHint 要能从服务端报文里挖出支持列表。
func TestUnsupportedVersionHintParsesServerList(t *testing.T) {
	msg := `HTTP 400: {"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,` +
		`"message":"Bad Request: Unsupported protocol version: 2026-07-28. ` +
		`Supported versions: 2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25"}}`

	got := unsupportedVersionHint(msg)
	want := []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"}
	if len(got) != len(want) {
		t.Fatalf("hint = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hint[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// 不带支持列表的报文不能瞎猜
	if h := unsupportedVersionHint("connection refused"); h != nil {
		t.Errorf("非版本错误不该产出 hint: %v", h)
	}
}

func TestIsUnsupportedVersionError(t *testing.T) {
	yes := []string{
		`HTTP 400: {"error":{"message":"Unsupported protocol version: 2026-07-28"}}`,
		`UnsupportedProtocolVersionError`,
	}
	for _, s := range yes {
		if !isUnsupportedVersionError(errString(s)) {
			t.Errorf("%q 应被判定为版本错误", s)
		}
	}
	no := []string{
		`connection refused`,
		`connection timed out`,
		`DNS lookup failed`,
	}
	for _, s := range no {
		if isUnsupportedVersionError(errString(s)) {
			t.Errorf("%q 不该被判定为版本错误", s)
		}
	}
	if isUnsupportedVersionError(nil) {
		t.Error("nil 不是版本错误")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 所有握手候选版本都必须落在握手代（2025-11-25 及更早），
// 不能混入 modern 代（2026-07-28）；且顺序必须从新到旧。
func TestHandshakeVersionsAreWellFormed(t *testing.T) {
	if len(handshakeProtocolVersions) == 0 {
		t.Fatal("握手版本列表为空")
	}
	for _, v := range handshakeProtocolVersions {
		if v > "2025-11-25" {
			t.Errorf("版本 %q 超出握手代（应 <= 2025-11-25）", v)
		}
		if len(v) != 10 || v[4] != '-' || v[7] != '-' {
			t.Errorf("版本 %q 不是 YYYY-MM-DD 形态", v)
		}
	}
	for i := 1; i < len(handshakeProtocolVersions); i++ {
		if handshakeProtocolVersions[i-1] <= handshakeProtocolVersions[i] {
			t.Errorf("版本未按从新到旧排列: %q 在 %q 之前",
				handshakeProtocolVersions[i-1], handshakeProtocolVersions[i])
		}
	}
	if ProtocolVersion != handshakeProtocolVersions[0] {
		t.Errorf("ProtocolVersion = %q，应为列表首个 %q", ProtocolVersion, handshakeProtocolVersions[0])
	}
}

// 重试不能在共享超时预算里累加：预算不够时必须带着**具体**错误退出，
// 而不是让最后一次尝试被 ctx 掐断后只剩一句 "context deadline exceeded"。
func TestSendDoesNotBurnBudgetOnRetries(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// 每次都慢过剩余预算
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	_, err := tr.Send(ctx, &JSONRPCRequest{
		JSONRPC: JSONRPCVersion, Method: "initialize", ID: 1,
	})
	if err == nil {
		t.Fatal("预期超时/失败，却成功了")
	}
	// 关键：错误要说明发生了什么，而不是空泛的 ctx 错误
	if !strings.Contains(err.Error(), "HTTP 500") && !strings.Contains(err.Error(), "attempt") &&
		!strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("错误信息失去诊断价值: %q", err.Error())
	}
	t.Logf("尝试 %d 次，错误 = %v", hits, err)
}
