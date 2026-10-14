package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StdioTransport implements MCP transport over stdio.
//
// Responses are demultiplexed by JSON-RPC id: a single reader goroutine drains
// stdout and hands each response to the pending caller that owns that id. This
// keeps concurrent Send calls from stealing each other's responses (a plain
// per-call ReadBytes would let two in-flight requests race for whichever line
// arrives first).
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr io.ReadCloser

	mu      sync.Mutex
	closed  bool
	pending map[string]chan *JSONRPCResponse
	readErr error // terminal read error, surfaced to all waiters
}

// requestIDString normalizes a JSON-RPC id (string or number) so it can be used
// as a map key. The wire form the server echoes back is the one we stored.
func requestIDString(id interface{}) string {
	switch v := id.(type) {
	case string:
		return v
	case float64:
		// JSON numbers decode to float64; format without a trailing ".0".
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// NewStdioTransport creates a new stdio transport
func NewStdioTransport(command string, args, env []string) (*StdioTransport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = append(os.Environ(), env...)

	// Set platform-specific process attributes
	setProcessAttrs(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MCP server: %w", err)
	}

	t := &StdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stderr:  stderr,
		pending: make(map[string]chan *JSONRPCResponse),
	}
	go t.readLoop(stdout)
	return t, nil
}

// readLoop is the single owner of the stdout stream. It reads one JSON-RPC
// message per line and routes it to the waiter registered for that id.
func (t *StdioTransport) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var resp JSONRPCResponse
			if jsonErr := json.Unmarshal(bytes.TrimSpace(line), &resp); jsonErr != nil {
				// A malformed line should not kill the stream; keep reading.
				continue
			}
			t.deliver(&resp)
		}
		if err != nil {
			t.failAll(err)
			return
		}
	}
}

// deliver hands a response to the waiter registered for its id. The send
// happens while holding the lock so a concurrent failAll (which closes the
// channel after swapping the pending map) can never race with it.
func (t *StdioTransport) deliver(resp *JSONRPCResponse) {
	key := requestIDString(resp.ID)
	t.mu.Lock()
	ch, ok := t.pending[key]
	if ok {
		delete(t.pending, key)
		select {
		case ch <- resp: // buffered(1): never blocks
		default:
		}
	}
	t.mu.Unlock()
}

// failAll records the terminal error and wakes every pending waiter.
func (t *StdioTransport) failAll(err error) {
	t.mu.Lock()
	if t.readErr == nil {
		t.readErr = err
	}
	waiters := t.pending
	t.pending = make(map[string]chan *JSONRPCResponse)
	for _, ch := range waiters {
		close(ch)
	}
	t.mu.Unlock()
}

// Send sends a JSON-RPC request and returns the response.
//
// Requests without an id (notifications) are fire-and-forget: they are written
// but never awaited, since the server sends no reply for them.
func (t *StdioTransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	key := requestIDString(req.ID)
	isNotification := req.ID == nil

	// Marshal the request
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	respCh := make(chan *JSONRPCResponse, 1)
	if !isNotification {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, fmt.Errorf("transport is closed")
		}
		if t.readErr != nil {
			err := t.readErr
			t.mu.Unlock()
			return nil, fmt.Errorf("transport read failed: %w", err)
		}
		t.pending[key] = respCh
		t.mu.Unlock()
	}

	// Write to stdin with newline delimiter
	t.mu.Lock()
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		t.mu.Unlock()
		t.unregister(key, respCh)
		return nil, fmt.Errorf("failed to write to stdin: %w", err)
	}
	t.mu.Unlock()

	if isNotification {
		return &JSONRPCResponse{JSONRPC: JSONRPCVersion}, nil
	}

	select {
	case <-ctx.Done():
		// A late response may still arrive after the caller gave up; the
		// reader will drop it because the waiter is no longer registered.
		t.unregister(key, respCh)
		return nil, ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			t.mu.Lock()
			err := t.readErr
			t.mu.Unlock()
			if err == io.EOF {
				// Try to read from stderr for error info
				if errBytes, _ := io.ReadAll(t.stderr); len(errBytes) > 0 {
					return nil, fmt.Errorf("MCP server stderr: %s", string(errBytes))
				}
			}
			if err == nil {
				err = io.EOF
			}
			return nil, fmt.Errorf("failed to read from stdout: %w", err)
		}
		return resp, nil
	}
}

// unregister removes a pending waiter, guarding against a concurrent deliver
// that already took ownership of it.
func (t *StdioTransport) unregister(key string, ch chan *JSONRPCResponse) {
	t.mu.Lock()
	if cur, ok := t.pending[key]; ok && cur == ch {
		delete(t.pending, key)
	}
	t.mu.Unlock()
}

// Close closes the stdio transport gracefully
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	var errs []error

	// Close stdin first to signal EOF to the process
	if err := t.stdin.Close(); err != nil {
		errs = append(errs, fmt.Errorf("stdin close error: %w", err))
	}
	// Wait for process to exit with timeout
	done := make(chan error, 1)
	go func() {
		done <- t.cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			// Check if process already exited
			if _, ok := err.(*exec.ExitError); ok {
				// Normal exit, no error
			} else {
				errs = append(errs, fmt.Errorf("process wait error: %w", err))
			}
		}
	case <-time.After(5 * time.Second):
		// Force kill if process doesn't exit gracefully
		if t.cmd.Process != nil {
			// Kill process group using platform-specific method
			killProcessGroup(t.cmd.Process)
		}
		// Wait for the process to be killed
		<-done
		errs = append(errs, fmt.Errorf("process force killed after timeout"))
	}

	// Drain stderr
	go io.Copy(io.Discard, t.stderr)

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// SSETransport implements MCP transport over Server-Sent Events
type SSETransport struct {
	url        string
	client     *http.Client
	mu         sync.Mutex
	closed     bool
	reconnect  bool
	maxRetries int
	retryDelay time.Duration
}

// NewSSETransport creates a new SSE transport
func NewSSETransport(url string) (*SSETransport, error) {
	return &SSETransport{
		url:        url,
		client:     &http.Client{Timeout: 30 * time.Second},
		closed:     false,
		reconnect:  true,
		maxRetries: 3,
		retryDelay: time.Second,
	}, nil
}

// retryableMethod reports whether a JSON-RPC method may safely be retried.
// Only read-only / idempotent methods qualify: tools/call may have side effects
// and must never be replayed automatically, even if the response was lost.
func retryableMethod(method string) bool {
	switch method {
	case "initialize", "ping", "tools/list", "resources/list",
		"prompts/list", "notifications/initialized":
		return true
	default:
		return false
	}
}

// Send sends a JSON-RPC request and returns the response
func (t *SSETransport) Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, fmt.Errorf("transport is closed")
	}
	t.mu.Unlock()

	// Non-idempotent calls get a single attempt: on a lost response we must not
	// risk running the tool twice.
	maxAttempts := 0
	if retryableMethod(req.Method) {
		maxAttempts = t.maxRetries
	}

	var lastErr error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(t.retryDelay * time.Duration(attempt)):
			}
		}

		resp, err := t.doRequest(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		t.mu.Lock()
		if t.closed || !t.reconnect {
			t.mu.Unlock()
			break
		}
		t.mu.Unlock()
	}

	if maxAttempts == 0 {
		return nil, lastErr
	}
	return nil, fmt.Errorf("SSE request failed after %d attempts: %w", maxAttempts, lastErr)
}

// acceptHeader is what a Streamable-HTTP MCP client must advertise. The spec
// requires the client to accept BOTH a JSON-RPC response and an SSE stream: a
// server that gets only "text/event-stream" is entitled to reject the request
// with 406 Not Acceptable (this is what broke ms-learn before).
const acceptHeader = "application/json, text/event-stream"

// maxMCPResponseBytes bounds how much of a response body we buffer, so a
// misbehaving server cannot make us allocate unbounded memory.
const maxMCPResponseBytes = 8 << 20 // 8MB

// classifyHTTPError turns a transport-level net/http error into a message that
// says what actually went wrong, rather than a generic "HTTP request failed".
// DNS failures, refused connections and timeouts are the three the UI needs to
// distinguish (each has a different fix).
func classifyHTTPError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) || strings.Contains(lower, "timeout"):
		return fmt.Errorf("connection timed out: %w", err)
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "getaddrinfo") ||
		strings.Contains(lower, "lookup ") || strings.Contains(lower, "dns"):
		return fmt.Errorf("DNS lookup failed (check the URL host or your network): %w", err)
	case strings.Contains(lower, "connection refused"):
		return fmt.Errorf("connection refused (is the server running and reachable?): %w", err)
	case strings.Contains(lower, "no route to host") || strings.Contains(lower, "network is unreachable"):
		return fmt.Errorf("host unreachable (check network/VPN): %w", err)
	case strings.Contains(lower, "certificate") || strings.Contains(lower, "tls"):
		return fmt.Errorf("TLS/certificate error: %w", err)
	default:
		return fmt.Errorf("HTTP request failed: %w", err)
	}
}

// httpStatusError builds a descriptive error for a non-200 response, folding in
// the server's own explanation when it sent one.
func httpStatusError(status int, contentType, body string) error {
	body = strings.TrimSpace(body)
	if body != "" {
		if isJSONContentType(contentType) || strings.HasPrefix(body, "{") {
			// Prefer the error/message field when the server sent structured JSON.
			var payload struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(body), &payload) == nil {
				if payload.Error != "" {
					return fmt.Errorf("HTTP %d: %s", status, payload.Error)
				}
				if payload.Message != "" {
					return fmt.Errorf("HTTP %d: %s", status, payload.Message)
				}
			}
		}
		if len(body) > 300 {
			body = body[:300] + "…"
		}
		return fmt.Errorf("HTTP %d: %s", status, body)
	}

	// No body: at least name the common statuses so the operator knows where to look.
	switch status {
	case http.StatusNotAcceptable:
		return fmt.Errorf("HTTP 406 Not Acceptable: server rejected the Accept/Content-Type negotiation")
	case http.StatusUnauthorized:
		return fmt.Errorf("HTTP 401 Unauthorized: the server requires credentials")
	case http.StatusForbidden:
		return fmt.Errorf("HTTP 403 Forbidden: access denied")
	case http.StatusNotFound:
		return fmt.Errorf("HTTP 404 Not Found: check the endpoint path")
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("HTTP 405 Method Not Allowed: the endpoint may not accept POST")
	default:
		return fmt.Errorf("HTTP %d", status)
	}
}

func (t *SSETransport) doRequest(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", t.url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", acceptHeader)
	// Protocol negotiation header used by Streamable HTTP servers; harmless for
	// older SSE-only endpoints.
	httpReq.Header.Set("MCP-Protocol-Version", ProtocolVersion)

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, classifyHTTPError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read a bounded slice of the body: servers usually explain the failure
		// (e.g. "Missing session ID") in the payload, which is far more useful
		// than a bare status code in the UI.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, httpStatusError(resp.StatusCode, resp.Header.Get("Content-Type"), string(snippet))
	}

	// Streamable HTTP servers may answer with either a plain JSON body or an SSE
	// stream, depending on the request and their own policy. Route on the
	// Content-Type instead of assuming SSE.
	if isJSONContentType(resp.Header.Get("Content-Type")) {
		return parseJSONRPCBody(resp.Body)
	}
	return t.parseSSEResponse(resp.Body)
}

// isJSONContentType reports whether a response Content-Type denotes a plain
// JSON body (as opposed to an SSE stream or anything else).
func isJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/json" || strings.HasSuffix(ct, "+json")
}

// parseJSONRPCBody decodes a plain JSON JSON-RPC response (the non-streaming
// shape of a Streamable HTTP reply).
func parseJSONRPCBody(body io.Reader) (*JSONRPCResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxMCPResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response body")
	}

	// A JSON body may still be a batch (array); we only ever send single
	// requests, so accept either a single object or pick the first element.
	if trimmed[0] == '[' {
		var batch []JSONRPCResponse
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSON-RPC batch response: %w", err)
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("empty JSON-RPC batch response")
		}
		return &batch[0], nil
	}

	var resp JSONRPCResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}
	return &resp, nil
}

func (t *SSETransport) parseSSEResponse(body io.Reader) (*JSONRPCResponse, error) {
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxMCPResponseBytes)

	var responseData string
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		// Parse SSE format
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			// Skip heartbeat/comments
			if data == "" || data == ":" {
				continue
			}
			// If this is a JSON-RPC response, parse it
			if strings.HasPrefix(data, "{") || strings.HasPrefix(data, "[") {
				responseData = data
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read SSE response: %w", err)
	}

	if responseData == "" {
		return nil, fmt.Errorf("no data received from SSE")
	}

	// A transport-level error event ("event: error") is a failure, not a
	// response — its payload carries {code, message} rather than a JSON-RPC
	// envelope. Surface it instead of silently unmarshalling an empty result.
	if eventType == "error" {
		var sseErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(responseData), &sseErr); err == nil && sseErr.Message != "" {
			if sseErr.Code != "" {
				return nil, fmt.Errorf("SSE server error [%s]: %s", sseErr.Code, sseErr.Message)
			}
			return nil, fmt.Errorf("SSE server error: %s", sseErr.Message)
		}
		return nil, fmt.Errorf("SSE server error: %s", responseData)
	}

	// Reuse the plain-JSON decoder: an SSE data line is a JSON-RPC envelope,
	// and some servers deliver it as a batch array even over SSE.
	return parseJSONRPCBody(strings.NewReader(responseData))
}

// Close closes the SSE transport
func (t *SSETransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true
	t.reconnect = false
	return nil
}

// SetReconnect enables or disables automatic reconnection
func (t *SSETransport) SetReconnect(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reconnect = enabled
}
