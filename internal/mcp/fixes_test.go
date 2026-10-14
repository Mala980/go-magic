package mcp

import (
	"context"
	"errors"
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

// --- Accept header (406 regression) ---

func TestSSETransportSendsCorrectAcceptHeader(t *testing.T) {
	var gotAccept, gotProtocol string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotProtocol = r.Header.Get("MCP-Protocol-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "ping", ID: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The spec requires BOTH media types; advertising only text/event-stream is
	// what made streamable-HTTP servers answer 406.
	if !strings.Contains(gotAccept, "application/json") || !strings.Contains(gotAccept, "text/event-stream") {
		t.Errorf("Accept header must offer both types, got %q", gotAccept)
	}
	if gotProtocol == "" {
		t.Error("MCP-Protocol-Version header should be set")
	}
}

// --- plain JSON response routing ---

func TestSSETransportParsesPlainJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}`))
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "tools/list", ID: 7})
	if err != nil {
		t.Fatalf("plain JSON response should parse, got error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
}

func TestSSETransportParsesJSONBatchResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"jsonrpc":"2.0","id":1,"result":{"ok":true}}]`))
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "ping", ID: 1}); err != nil {
		t.Fatalf("JSON batch response should parse, got error: %v", err)
	}
}

func TestIsJSONContentType(t *testing.T) {
	yes := []string{"application/json", "application/json; charset=utf-8", "APPLICATION/JSON", "application/vnd.api+json"}
	no := []string{"text/event-stream", "text/plain", "", "application/xml"}
	for _, ct := range yes {
		if !isJSONContentType(ct) {
			t.Errorf("isJSONContentType(%q) = false, want true", ct)
		}
	}
	for _, ct := range no {
		if isJSONContentType(ct) {
			t.Errorf("isJSONContentType(%q) = true, want false", ct)
		}
	}
}

// --- HTTP status error descriptions ---

func TestHTTPStatusErrorIncludesServerMessage(t *testing.T) {
	err := httpStatusError(400, "application/json", `{"error":"Missing session ID"}`)
	if err == nil || !strings.Contains(err.Error(), "Missing session ID") {
		t.Errorf("400 with JSON body should surface the server message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include the status code, got: %v", err)
	}
}

func TestHTTPStatusErrorNamesCommonCodes(t *testing.T) {
	cases := []struct {
		status int
		substr string
	}{
		{http.StatusNotAcceptable, "406"},
		{http.StatusUnauthorized, "401"},
		{http.StatusForbidden, "403"},
		{http.StatusNotFound, "404"},
	}
	for _, c := range cases {
		err := httpStatusError(c.status, "", "")
		if err == nil || !strings.Contains(err.Error(), c.substr) {
			t.Errorf("status %d should be named in the error, got: %v", c.status, err)
		}
	}
}

// --- network error classification ---

func TestClassifyHTTPErrorDNS(t *testing.T) {
	// Go's DNS error text varies by platform; feed the shapes it produces.
	for _, msg := range []string{
		`Post "http://x.invalid": dial tcp: lookup x.invalid: no such host`,
		`dial tcp: lookup y: getaddrinfo ENOTFOUND`,
	} {
		err := classifyHTTPError(errors.New(msg))
		if !strings.Contains(strings.ToLower(err.Error()), "dns") {
			t.Errorf("DNS error should be classified as DNS, got: %v", err)
		}
	}
}

func TestClassifyHTTPErrorConnectionRefused(t *testing.T) {
	err := classifyHTTPError(errors.New("dial tcp 127.0.0.1:8080: connect: connection refused"))
	if !strings.Contains(strings.ToLower(err.Error()), "refused") {
		t.Errorf("refused connection should be classified, got: %v", err)
	}
}

func TestClassifyHTTPErrorTimeout(t *testing.T) {
	err := classifyHTTPError(context.DeadlineExceeded)
	if !strings.Contains(strings.ToLower(err.Error()), "timed out") {
		t.Errorf("deadline should be classified as timeout, got: %v", err)
	}
}

func TestClassifyHTTPErrorFallback(t *testing.T) {
	err := classifyHTTPError(errors.New("some totally unexpected failure"))
	if !strings.Contains(err.Error(), "HTTP request failed") {
		t.Errorf("unknown errors should fall back to a generic message, got: %v", err)
	}
}
