package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockStreamableServer emulates a spec-compliant Streamable HTTP MCP server:
// it REJECTS requests whose Accept header does not list both application/json
// and text/event-stream (returning 406 like ms-learn did), and replies in the
// requested shape (JSON body or SSE stream).
func mockStreamableServer(t *testing.T, replyAsSSE bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !contains(accept, "application/json") || !contains(accept, "text/event-stream") {
			// Exactly the behaviour that broke real servers: reject a
			// partial Accept header.
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = io.WriteString(w, `{"error":"Accept must include application/json and text/event-stream"}`)
			return
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
				"serverInfo":      map[string]interface{}{"name": "mock", "version": "1.0"},
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

		envelope := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}
		payload, _ := json.Marshal(envelope)

		if replyAsSSE {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestStreamableHTTPInitializeOverJSON is the end-to-end regression for the
// ms-learn 406: the client must complete a full initialize handshake against a
// server that replies with a plain JSON body.
func TestStreamableHTTPInitializeOverJSON(t *testing.T) {
	srv := mockStreamableServer(t, false)
	defer srv.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()

	if err := mgr.ConnectSSE("mock-json", ServerConfig{Transport: "sse", URL: srv.URL}); err != nil {
		t.Fatalf("initialize over JSON must succeed, got: %v", err)
	}

	tools, err := mgr.ListToolsByServer("mock-json")
	if err != nil {
		t.Fatalf("listing tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "mock_echo" {
		t.Fatalf("expected the mock tool to be discovered, got %+v", tools)
	}
}

// TestStreamableHTTPInitializeOverSSE covers the streaming reply shape.
func TestStreamableHTTPInitializeOverSSE(t *testing.T) {
	srv := mockStreamableServer(t, true)
	defer srv.Close()

	mgr := NewManager()
	defer mgr.DisconnectAll()

	if err := mgr.ConnectSSE("mock-sse", ServerConfig{Transport: "sse", URL: srv.URL}); err != nil {
		t.Fatalf("initialize over SSE must succeed, got: %v", err)
	}

	tools, _ := mgr.ListToolsByServer("mock-sse")
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool over SSE, got %d", len(tools))
	}
}

// TestStreamableHTTP406IsReportedClearly verifies that when a server really does
// reject us, the surfaced error explains it (406 + the server's message), and
// that the failure is recorded on the manager for the UI.
func TestStreamableHTTP406IsReportedClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = io.WriteString(w, `{"error":"nope"}`)
	}))
	defer srv.Close()

	mgr := NewManager()
	err := mgr.ConnectSSE("rejecting", ServerConfig{Transport: "sse", URL: srv.URL})
	if err == nil {
		t.Fatal("expected connection to fail")
	}

	msg := err.Error()
	if !contains(msg, "406") || !contains(msg, "nope") {
		t.Errorf("error should carry status 406 and the server message, got: %v", err)
	}

	// The failure must be retrievable for the UI even though there is no client.
	if got := mgr.LastFailure("rejecting"); got == "" {
		t.Error("LastFailure should record the failure for a server that never connected")
	}
}

// TestSSETransportRetriesIdempotentOnly is a focused guard that the retry policy
// still holds after the transport rework.
func TestSSETransportRetriesIdempotentOnly(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr, _ := NewSSETransport(srv.URL)
	tr.retryDelay = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	calls = 0
	_, _ = tr.Send(ctx, &JSONRPCRequest{JSONRPC: JSONRPCVersion, Method: "tools/call", ID: 1})
	if calls != 1 {
		t.Errorf("tools/call must not be retried, got %d calls", calls)
	}
}
