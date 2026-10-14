package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- requestIDString ---

func TestRequestIDString(t *testing.T) {
	cases := []struct {
		in   interface{}
		want string
	}{
		{"abc", "abc"},
		{float64(1), "1"},
		{float64(42), "42"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := requestIDString(c.in); got != c.want {
			t.Errorf("requestIDString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- retryableMethod ---

func TestRetryableMethod(t *testing.T) {
	retryable := []string{"initialize", "ping", "tools/list", "resources/list", "prompts/list"}
	notRetryable := []string{"tools/call", "resources/read", "prompts/get", "completion/complete"}

	for _, m := range retryable {
		if !retryableMethod(m) {
			t.Errorf("method %q should be retryable", m)
		}
	}
	for _, m := range notRetryable {
		if retryableMethod(m) {
			t.Errorf("method %q must NOT be retryable (side effects)", m)
		}
	}
}

// --- SSE error event handling ---

func TestParseSSEResponseErrorEvent(t *testing.T) {
	tr, _ := NewSSETransport("http://example.invalid/sse")
	body := "event: error\ndata: {\"code\":\"CREATE_PROXY_FAILED\",\"message\":\"创建代理失败: refused\"}\n\n"

	_, err := tr.parseSSEResponse(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected an error for event:error, got nil (silent failure regression)")
	}
	if !strings.Contains(err.Error(), "CREATE_PROXY_FAILED") {
		t.Errorf("error should carry the SSE code, got: %v", err)
	}
	if !strings.Contains(err.Error(), "创建代理失败") {
		t.Errorf("error should carry the SSE message, got: %v", err)
	}
}

func TestParseSSEResponseErrorEventNoMessage(t *testing.T) {
	tr, _ := NewSSETransport("http://example.invalid/sse")
	body := "event: error\ndata: {\"code\":\"X\"}\n\n"
	if _, err := tr.parseSSEResponse(strings.NewReader(body)); err == nil {
		t.Fatal("expected error for event:error without message")
	}
}

func TestParseSSEResponseNormalMessage(t *testing.T) {
	tr, _ := NewSSETransport("http://example.invalid/sse")
	body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n"

	resp, err := tr.parseSSEResponse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID == nil {
		t.Fatal("expected parsed JSON-RPC response with id")
	}
}

// --- SSE retry gating (tools/call must not be retried) ---

func TestSSETransportSendNonIdempotentNoRetry(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := tr.Send(ctx, &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "tools/call",
		ID:      1,
	})
	if err == nil {
		t.Fatal("expected error from failing server")
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if got != 1 {
		t.Errorf("tools/call must be attempted exactly once, got %d attempts", got)
	}
}

func TestSSETransportSendIdempotentRetries(t *testing.T) {
	var hits int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	tr.retryDelay = time.Millisecond // keep the test fast
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := tr.Send(ctx, &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "tools/list",
		ID:      1,
	})
	if err == nil {
		t.Fatal("expected error from failing server")
	}
	mu.Lock()
	got := hits
	mu.Unlock()
	if got <= 1 {
		t.Errorf("tools/list should be retried, got %d attempts", got)
	}
}

// --- ConfigLoader fault isolation ---

func TestConfigLoaderIsolatesFailures(t *testing.T) {
	loader := &ConfigLoader{}
	mgr := NewManager()

	// One unreachable SSE server + one bogus stdio command. Both fail, but the
	// loader must attempt both and report them together (not bail on the first).
	servers := map[string]ServerConfig{
		"dead-sse": {Transport: "sse", URL: "http://127.0.0.1:1/sse"},
		"dead-cmd": {Transport: "stdio", Command: "definitely-not-a-real-binary-xyz"},
	}

	err := loader.LoadFromConfig(mgr, servers)
	if err == nil {
		t.Fatal("expected an error when all servers fail")
	}
	// Both failures should be mentioned, proving isolation.
	if !strings.Contains(err.Error(), "dead-sse") || !strings.Contains(err.Error(), "dead-cmd") {
		t.Errorf("error should mention both failed servers, got: %v", err)
	}
}
