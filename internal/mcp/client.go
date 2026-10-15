package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/magicwubiao/go-magic/pkg/log"
)

// Protocol constants
const (
	JSONRPCVersion        = "2.0"
	ProtocolName          = "modelcontextprotocol"
	DefaultTimeout        = 30 * time.Second
	DefaultReconnectDelay = 5 * time.Second
	MaxReconnectAttempts  = 3
	ClientName            = "go-magic"
	ClientVersion         = "0.5.8"
)

// handshakeProtocolVersions 是握手代协议版本，**从新到旧**排列。
//
// initialize 会从第一个开始尝试；服务端返回 UnsupportedProtocolVersion
// 时自动降级到下一个（见 isUnsupportedVersionError）。这样既不会
// 因为写死一个版本而在协议演进时失联，也不需要预先知道对端支持什么。
var handshakeProtocolVersions = []string{
	"2025-11-25", // 最新握手版：Tasks、枚举 schema、sampling 工具调用
	"2025-06-18", // 结构化工具输出、elicitation，移除 JSON-RPC 批处理
	"2025-03-26", // Streamable HTTP、OAuth 2.1、tool annotations
	"2024-11-05", // 初版稳定
}

// ProtocolVersion 是**首选**协议版本。
//
// 注意 MCP 有两代模型（见官方 SEP-2575）：
//   - modern era（2026-07-28 起）：**没有握手**，靠 server/discover 发现，
//     每个请求在 _meta 里自带版本号；
//   - handshake era（2025-11-25 及更早）：用 initialize 握手约定一个版本。
//
// 本客户端实现的是 **handshake** 路径，所以首选值必须落在握手代里。
// 曾经这里写死 2026-07-28（modern 代），DeepWiki 之类的服务端直接返回
// 400 "Unsupported protocol version"，整台 server 从此连不上。
var ProtocolVersion = handshakeProtocolVersions[0]

// unsupportedVersionHint 从服务端的 400 报文里挖出"它到底支持哪些版本"。
//
// 常见形态（DeepWiki 实测）：
//
//	Bad Request: Unsupported protocol version: 2026-07-28.
//	Supported versions: 2024-11-05, 2025-03-26, 2025-06-18, 2025-11-25
//
// 能拿到列表时按它的列表重试，拿不到就按 handshakeProtocolVersions 盲降。
func unsupportedVersionHint(msg string) []string {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "unsupported protocol version") &&
		!strings.Contains(lower, "unsupportedprotocolversion") {
		return nil
	}
	idx := strings.Index(lower, "supported version")
	if idx < 0 {
		return nil
	}
	// 取 "supported versions:" 之后的部分，先剥掉 JSON 残留的引号与括号，
	// 再按分隔符切分——服务端报文常见的结尾形态是 `2025-11-25"}}`，
	// 不剥离的话最后一个版本号会连着 `"` 一起被切掉。
	tail := msg[idx:]
	tail = strings.NewReplacer(`"`, " ", `}`, " ", `]`, " ", `)`, " ", `(`, " ").Replace(tail)

	seen := map[string]bool{}
	var out []string
	for _, field := range strings.FieldsFunc(tail, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\r' || r == '.'
	}) {
		field = strings.TrimSpace(field)
		if len(field) == 10 && field[4] == '-' && field[7] == '-' && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

// isUnsupportedVersionError 判断错误是否属于"协议版本不被接受"。
func isUnsupportedVersionError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unsupported protocol version") ||
		strings.Contains(lower, "unsupportedprotocolversion") ||
		strings.Contains(lower, "-32600") && strings.Contains(lower, "version")
}

// JSONRPCRequest represents a JSON-RPC 2.0 request
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      interface{}     `json:"id,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
	ID      interface{}     `json:"id,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// MCPMessage represents an MCP protocol message
type MCPMessage struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Tool represents an MCP tool (2025-03-26 protocol compatible)
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Annotations *ToolAnnotations       `json:"annotations,omitempty"` // 2025-03-26
}

// ToolAnnotations holds metadata hints for clients (2025-03-26)
type ToolAnnotations struct {
	Title               string   `json:"title,omitempty"`
	ReadOnlyHint        bool     `json:"readOnlyHint,omitempty"`
	DestructiveHint     bool     `json:"destructiveHint,omitempty"`
	IdempotentHint      bool     `json:"idempotentHint,omitempty"`
	OpenWorldHint       bool     `json:"openWorldHint,omitempty"`
	ReasoningEffortHint string   `json:"reasoningEffortHint,omitempty"` // low | medium | high
	Tags                []string `json:"tags,omitempty"`
	Privacy             []string `json:"privacy,omitempty"`
}

// Supported transport names, used as ServerConfig.Transport values. External
// JSON configs spell these differently (type: "http", "streamable-http", …) —
// see NormalizeTransport in import.go for the mapping.
const (
	TransportStdio = "stdio"
	TransportSSE   = "sse"
)

// ServerConfig represents MCP server configuration
type ServerConfig struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Env       []string `json:"env,omitempty"`
	Transport string   `json:"transport"`     // "stdio" or "sse"
	URL       string   `json:"url,omitempty"` // for SSE transport
}

// Client represents an MCP client
type Client struct {
	name            string
	config          ServerConfig
	transport       Transport
	tools           map[string]Tool
	mu              sync.RWMutex
	connected       bool
	lastError       string
	lastHealthCheck time.Time
	timeout         time.Duration
	reconnect       bool
	reconnectDelay  time.Duration
	// negotiatedVersion 记录握手最终谈成的协议版本，供诊断与状态展示。
	negotiatedVersion string
}

// Transport interface for MCP transport layers
type Transport interface {
	Send(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)
	Close() error
}

// Manager manages multiple MCP server connections
type Manager struct {
	mu             sync.RWMutex
	clients        map[string]*Client
	failures       map[string]string // last connect failure per configured server
	reconnectDelay time.Duration
}

// NewManager creates a new MCP manager
func NewManager() *Manager {
	return &Manager{
		clients:        make(map[string]*Client),
		failures:       make(map[string]string),
		reconnectDelay: DefaultReconnectDelay,
	}
}

// LastFailure returns the most recent connection error recorded for a server
// that is configured but not currently connected (empty when the server either
// connected fine or was never attempted).
func (m *Manager) LastFailure(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// A live client owns the authoritative state; a stale failure entry for the
	// same name would be misleading once it connects.
	if _, ok := m.clients[name]; ok {
		return ""
	}
	return m.failures[name]
}

// recordFailure remembers why a server failed to connect.
func (m *Manager) recordFailure(name string, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.failures[name] = err.Error()
	m.mu.Unlock()
}

// clearFailure drops the recorded failure for a server (on successful connect
// or explicit disconnect).
func (m *Manager) clearFailure(name string) {
	m.mu.Lock()
	delete(m.failures, name)
	m.mu.Unlock()
}

// recordFailureLocked is recordFailure for callers already holding m.mu.
func (m *Manager) recordFailureLocked(name string, err error) {
	if err != nil {
		m.failures[name] = err.Error()
	}
}

// clearFailureLocked is clearFailure for callers already holding m.mu.
func (m *Manager) clearFailureLocked(name string) {
	delete(m.failures, name)
}

// connect 是三种连接入口(ConnectStdio / ConnectSSE / Reconnect)的公共实现，
// 采用两阶段模型：
//
//	阶段一(持写锁): 检查名字是否已被占用，随即释放锁。
//	阶段二(不持锁): 建 transport、initialize 握手、发现工具——这段时间可能
//	                长达 DefaultTimeout。
//	阶段三(持写锁): 回填 clients；若期间有别人抢先连上同名 server，则关闭
//	                自己刚建好的 transport 并报重复。
//
// 之所以必须这样拆: 旧实现整段握手都握着 Manager.mu 写锁，连接器页
// (ListServers)、工具列表(ListToolsByServer)、健康检查、断开操作全部被
// 挡在门外；而且那把锁会把并发的多 server 连接强行串行化。
func (m *Manager) connect(name string, config ServerConfig) error {
	if m.isConnected(name) {
		return fmt.Errorf("MCP server '%s' already connected", name)
	}

	client, err := m.buildClient(name, config)
	if err != nil {
		m.recordFailure(name, err)
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.clients[name]; exists {
		// 并发窗口里另一个调用赢了这场竞争: 放弃自己刚建的连接，
		// 否则会泄漏一个 transport(stdio 情况下还留着子进程)。
		_ = client.transport.Close()
		return fmt.Errorf("MCP server '%s' already connected", name)
	}

	m.clearFailureLocked(name)
	m.clients[name] = client
	return nil
}

// buildClient 建好 transport 并完成 initialize 握手(含工具发现)。
// 全程不持有 Manager.mu —— 这是连接不再阻塞其它操作的关键。
func (m *Manager) buildClient(name string, config ServerConfig) (*Client, error) {
	client := &Client{
		name:           name,
		config:         config,
		tools:          make(map[string]Tool),
		timeout:        DefaultTimeout,
		reconnect:      true,
		reconnectDelay: DefaultReconnectDelay,
	}

	var transport Transport
	var err error
	var kind string

	switch config.Transport {
	case "sse":
		kind = "SSE"
		transport, err = NewSSETransport(config.URL)
	default:
		kind = "stdio"
		transport, err = NewStdioTransport(config.Command, config.Args, config.Env)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create %s transport: %w", kind, err)
	}

	client.transport = transport
	client.connected = true

	if err := client.initialize(); err != nil {
		transport.Close()
		return nil, fmt.Errorf("failed to initialize MCP server: %w", err)
	}

	return client, nil
}

// ConnectStdio connects to an MCP server using stdio transport
func (m *Manager) ConnectStdio(name string, config ServerConfig) error {
	config.Transport = "stdio"
	return m.connect(name, config)
}

// ConnectSSE connects to an MCP server using SSE transport
func (m *Manager) ConnectSSE(name string, config ServerConfig) error {
	config.Transport = "sse"
	return m.connect(name, config)
}

// Disconnect disconnects an MCP server
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, exists := m.clients[name]
	if !exists {
		return fmt.Errorf("MCP server '%s' not found", name)
	}

	if err := client.transport.Close(); err != nil {
		return fmt.Errorf("failed to close transport: %w", err)
	}

	delete(m.clients, name)
	m.clearFailureLocked(name)
	return nil
}

// ListServers lists all connected MCP servers
func (m *Manager) ListServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// ListTools lists all available tools from all connected servers
func (m *Manager) ListTools() []Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var allTools []Tool
	for _, client := range m.clients {
		client.mu.RLock()
		for _, tool := range client.tools {
			allTools = append(allTools, tool)
		}
		client.mu.RUnlock()
	}
	return allTools
}

// ListToolsByServer lists tools available from a specific server
func (m *Manager) ListToolsByServer(name string) ([]Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	client, exists := m.clients[name]
	if !exists {
		return nil, fmt.Errorf("MCP server '%s' not found", name)
	}

	client.mu.RLock()
	tools := make([]Tool, 0, len(client.tools))
	for _, tool := range client.tools {
		tools = append(tools, tool)
	}
	client.mu.RUnlock()

	return tools, nil
}

// CallTool calls an MCP tool
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, arguments map[string]interface{}) (interface{}, error) {
	m.mu.RLock()
	client, exists := m.clients[serverName]
	if !exists {
		return nil, fmt.Errorf("MCP server '%s' not found", serverName)
	}
	m.mu.RUnlock()

	return client.callTool(ctx, toolName, arguments)
}

// initializeTimeout 是 MCP 握手（initialize + tools/list）的总预算。
//
// 之所以不直接复用 DefaultTimeout(30s)：这个握手是**用户点保存后同步等待**
// 的路径，30s 与前端 request() 的 30s AbortController 完全重合，两端同时
// 踩线会让用户看到"转圈到失败且没有原因"。取一半后，最坏情况下后端仍能
// 在前端超时前返回一条可操作的错误。
const initializeTimeout = 15 * time.Second

// initialize initializes the MCP server and discovers tools (handshake era).
//
// 协议版本协商：从 handshakeProtocolVersions 里逐个尝试，直到服务端接受。
// 这一步是必要的——硬编码单个版本时，一旦服务端不支持该版本，整台 server
// 就永远连不上，而报错还常被超时重试掩盖成一句 "context deadline exceeded"。
func (c *Client) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), initializeTimeout)
	defer cancel()

	candidates := handshakeProtocolVersions
	var lastErr error

	for i, version := range candidates {
		resp, err := c.sendInitialize(ctx, version)
		if err != nil {
			// 版本不被接受 → 用服务端给出的支持列表继续协商
			if isUnsupportedVersionError(err) && i < len(candidates)-1 {
				if hint := unsupportedVersionHint(err.Error()); len(hint) > 0 {
					log.Infof("[MCP] server '%s' rejected protocol %s; it supports %s",
						c.name, version, strings.Join(hint, ", "))
				} else {
					log.Infof("[MCP] server '%s' rejected protocol %s; trying an older one",
						c.name, version)
				}
				lastErr = err
				continue
			}
			return err
		}
		if resp.Error != nil {
			if isUnsupportedVersionError(fmt.Errorf("%s", resp.Error.Message)) && i < len(candidates)-1 {
				lastErr = fmt.Errorf("initialize error: %s", resp.Error.Message)
				continue
			}
			return fmt.Errorf("initialize error: %s", resp.Error.Message)
		}

		// 握手成功：记录协商结果并继续完成能力发现
		c.negotiatedVersion = version
		c.validateInitResult(resp)
		return c.finishHandshake(ctx)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no supported protocol version")
	}
	return fmt.Errorf("protocol version negotiation failed: %w", lastErr)
}

// sendInitialize 用指定协议版本发一次 initialize 请求。
func (c *Client) sendInitialize(ctx context.Context, version string) (*JSONRPCResponse, error) {
	req := &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "initialize",
		Params: jsonMarshal(map[string]interface{}{
			"protocolVersion": version,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{
					"listChanged": false,
				},
				"logging": map[string]interface{}{},
				"prompts": map[string]interface{}{
					"listChanged": false,
				},
				"resources": map[string]interface{}{
					"listChanged": false,
					"subscribe":   false,
				},
			},
			"clientInfo": map[string]interface{}{
				"name":    ClientName,
				"version": ClientVersion,
			},
		}),
		ID: 1,
	}
	return c.transport.Send(ctx, req)
}

// finishHandshake 发 initialized 通知并发现工具。
func (c *Client) finishHandshake(ctx context.Context) error {
	// 通知本身不需要响应，但底层仍要走一次 HTTP 往返；沿用握手的 ctx
	// 以保证超时行为一致（用 Background 会让不响应的服务端把预算吃光）。
	notif := &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "notifications/initialized",
	}
	_, _ = c.transport.Send(ctx, notif)

	return c.discoverTools(ctx)
}

// validateInitResult 解析服务端返回的 initialize 结果并记录版本差异。
func (c *Client) validateInitResult(resp *JSONRPCResponse) {
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &initResult); err != nil {
		// 不致命：部分老服务端返回极简 payload
		log.Warnf("[MCP] Failed to parse initialize result: %v", err)
		return
	}
	if initResult.ProtocolVersion == "" {
		return
	}
	// 服务端可能回一个它自己更低的版本（合法协商结果），也可能回更高的。
	if initResult.ProtocolVersion != c.negotiatedVersion {
		log.Infof("[MCP] server '%s' negotiated protocol %s (client proposed %s)",
			c.name, initResult.ProtocolVersion, c.negotiatedVersion)
	} else {
		log.Infof("[MCP] server '%s' negotiated protocol %s", c.name, initResult.ProtocolVersion)
	}
}

// discoverTools discovers available tools from the MCP server
func (c *Client) discoverTools(ctx context.Context) error {
	req := &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "tools/list",
		ID:      2,
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return err
	}

	if resp.Error != nil {
		return fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}

	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("failed to parse tools list: %w", err)
	}

	c.mu.Lock()
	for _, tool := range result.Tools {
		c.tools[tool.Name] = tool
	}
	c.mu.Unlock()

	return nil
}

// callTool calls a tool on the MCP server (2025-03-26 compatible)
func (c *Client) callTool(ctx context.Context, toolName string, arguments map[string]interface{}) (interface{}, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
	}

	req := &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "tools/call",
		Params:  jsonMarshal(params),
		ID:      uuid.New().String(),
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		c.markDisconnected(err)
		return nil, err
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tool call error: %s", resp.Error.Message)
	}

	var result struct {
		Content []struct {
			// Text content
			Type string `json:"type"`
			Text string `json:"text,omitempty"`

			// Image content (2025-03-26)
			Data     string `json:"data,omitempty"`     // base64
			MimeType string `json:"mimeType,omitempty"` // e.g. image/png

			// Embedded resource (2025-03-26)
			URI      string `json:"uri,omitempty"`
			BlobData string `json:"blobData,omitempty"`

			// Audio content (2025-03-26)
			// Transcript optional field for audio/image

		} `json:"content"`
		IsError    bool   `json:"isError,omitempty"`    // 2025-03-26
		NextCursor string `json:"nextCursor,omitempty"` // 2025-03-26 paginated response
	}

	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to parse tool result: %w", err)
	}

	// Server explicitly signaled an error via the 2025-03-26 isError field
	if result.IsError {
		if len(result.Content) > 0 && result.Content[0].Text != "" {
			return nil, fmt.Errorf("tool error: %s", result.Content[0].Text)
		}
		return nil, fmt.Errorf("tool call returned isError=true")
	}

	// Concatenate all text-type contents; non-text (images, resources) are
	// preserved as annotations in the raw result but summarized as text for LLM.
	if len(result.Content) == 0 {
		return "", nil
	}

	var combined strings.Builder
	for i, item := range result.Content {
		if i > 0 {
			combined.WriteString("\n")
		}
		switch item.Type {
		case "", "text":
			combined.WriteString(item.Text)
		case "image":
			// NOTE: avoid square brackets — GLM mimics them in its output.
			desc := fmt.Sprintf("image %s (%d bytes)", item.MimeType, base64DecodedLen(item.Data))
			combined.WriteString(desc)
		case "embedded_resource":
			name := item.URI
			if name == "" {
				name = "embedded-resource"
			}
			combined.WriteString(fmt.Sprintf("resource: %s", name))
			if item.Text != "" {
				combined.WriteString("\n")
				combined.WriteString(item.Text)
			}
		case "audio":
			combined.WriteString("audio content")
		default:
			if item.Text != "" {
				combined.WriteString(item.Text)
			} else {
				combined.WriteString(fmt.Sprintf("%s content", item.Type))
			}
		}
	}

	return combined.String(), nil
}

// base64DecodedLen returns the approximate byte size of a base64-encoded
// payload without decoding the full string (cheap for logging).
func base64DecodedLen(s string) int {
	if s == "" {
		return 0
	}
	n := len(s)
	pad := 0
	if n >= 1 && s[n-1] == '=' {
		pad++
	}
	if n >= 2 && s[n-2] == '=' {
		pad++
	}
	return (n*3)/4 - pad
}

// NamespacePrefix is the registry-name prefix shared by every tool exposed by
// a standalone MCP server (mcp_<server>_<tool>).
const NamespacePrefix = "mcp_"

// ToolPrefix returns the registry-name prefix under which the tools of a given
// standalone MCP server are registered (e.g. "mcp_filesystem_"). Callers use
// it to find/remove all tools that belong to one server.
func ToolPrefix(serverName string) string {
	return NamespacePrefix + serverName + "_"
}

// ToolName returns the canonical registry name for a tool exposed by a
// standalone MCP server: mcp_<server>_<tool>.
func ToolName(serverName, toolName string) string {
	return ToolPrefix(serverName) + toolName
}

// MCPTool adapts a tool discovered on a standalone MCP server so it can be
// registered into the tool registry and invoked from the agent loop. It
// satisfies the tool.Tool interface (Name/Description/Schema/Execute) by
// construction, even though this package intentionally does not import the
// tool package (avoiding the tool -> kanban -> config -> mcp import cycle).
type MCPTool struct {
	serverName string
	tool       Tool
	manager    *Manager
}

// NewMCPTool creates a registry adapter for a single MCP server tool.
func NewMCPTool(serverName string, tool Tool, manager *Manager) *MCPTool {
	return &MCPTool{serverName: serverName, tool: tool, manager: manager}
}

// Name returns the tool name (mcp_<server>_<tool>).
func (t *MCPTool) Name() string {
	return ToolName(t.serverName, t.tool.Name)
}

// Description returns the tool description.
func (t *MCPTool) Description() string {
	return fmt.Sprintf("MCP %s: %s", t.serverName, t.tool.Description)
}

// Schema returns the input schema in OpenAI function-calling JSON Schema form.
func (t *MCPTool) Schema() map[string]interface{} {
	return t.tool.InputSchema
}

// Parameters returns the tool parameters (deprecated alias of Schema kept for
// callers that still use the old adapter shape).
func (t *MCPTool) Parameters() map[string]interface{} {
	return t.Schema()
}

// Execute executes the MCP tool.
func (t *MCPTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	return t.manager.CallTool(ctx, t.serverName, t.tool.Name, args)
}

// isConnected reports whether a server is currently connected.
func (m *Manager) isConnected(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[name]
	return ok
}

// markDisconnected records that the underlying transport has failed, so health
// and status queries stop reporting a dead connection as alive. The reason is
// kept so the UI can show *why* a server went down.
func (c *Client) markDisconnected(err error) {
	c.mu.Lock()
	c.connected = false
	if err != nil {
		c.lastError = err.Error()
	}
	c.mu.Unlock()
}

// markConnected clears any previous failure and marks the client healthy.
func (c *Client) markConnected() {
	c.mu.Lock()
	c.connected = true
	c.lastError = ""
	c.mu.Unlock()
}

// ConnectConfigured connects every configured standalone MCP server. Per-server
// failures are collected into a single error so one broken server never blocks
// the others (matching the isolation policy used for Agent Plugin MCP servers).
// Callers then sync the discovered tools into their tool registry (see
// mcpbridge.SyncToRegistry).
// ConnectConfigured connects every server in the map and reports the failures.
//
// 并发连接: 每个 server 一个 goroutine。旧实现是串行 for 循环，总耗时等于
// 各 server 握手耗时之和(每台上限 DefaultTimeout=30s)，多台 server 时会
// 让启动期后台连接拖到几分钟；并发后总耗时等于最慢的一台。
// Manager 自身的锁已在 connect 中拆开(握手不持锁)，因此并发是安全的。
// 失败信息按 server 名排序后再拼接，保证同样的失败集合产出同样的错误串。
func (m *Manager) ConnectConfigured(servers map[string]ServerConfig) error {
	var (
		wg       sync.WaitGroup
		failMu   sync.Mutex
		failures []string
	)

	for name, cfg := range servers {
		if m.isConnected(name) {
			continue
		}

		wg.Add(1)
		go func(name string, cfg ServerConfig) {
			defer wg.Done()

			var err error
			switch cfg.Transport {
			case "sse":
				err = m.ConnectSSE(name, cfg)
			default:
				err = m.ConnectStdio(name, cfg)
			}

			if err != nil {
				failMu.Lock()
				failures = append(failures, fmt.Sprintf("%s: %v", name, err))
				failMu.Unlock()
				log.Warnf("[MCP] failed to connect server %q: %v", name, err)
				return
			}
			log.Infof("[MCP] connected server %q", name)
		}(name, cfg)
	}

	wg.Wait()

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("failed to connect MCP server(s): %s", strings.Join(failures, "; "))
	}
	return nil
}

// DisconnectAll disconnects every connected MCP server. Safe to call multiple
// times (e.g. from deferred shutdown paths).
func (m *Manager) DisconnectAll() {
	for _, name := range m.ListServers() {
		_ = m.Disconnect(name)
	}
}

// HealthCheck checks the health of a connected MCP server
func (m *Manager) HealthCheck(name string) error {
	m.mu.RLock()
	client, exists := m.clients[name]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("MCP server '%s' not found", name)
	}
	client.mu.RLock()
	connected := client.connected
	client.mu.RUnlock()
	m.mu.RUnlock()

	if !connected {
		return fmt.Errorf("MCP server '%s' is disconnected", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		Method:  "ping",
		ID:      1,
	}

	resp, err := client.transport.Send(ctx, req)
	if err != nil {
		client.markDisconnected(err)
		return err
	}

	if resp.Error != nil {
		pingErr := fmt.Errorf("ping error: %s", resp.Error.Message)
		client.markDisconnected(pingErr)
		return pingErr
	}

	// Update last health check time and restore connected state.
	client.mu.Lock()
	client.connected = true
	client.lastError = ""
	client.lastHealthCheck = time.Now()
	client.mu.Unlock()

	return nil
}

// Reconnect attempts to reconnect to an MCP server
func (m *Manager) Reconnect(name string) error {
	m.mu.RLock()
	client, exists := m.clients[name]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("MCP server '%s' not found", name)
	}
	// config 是构造后不再改写的只读字段，在 Manager 读锁内取值即可。
	// 旧实现在这里额外调了一次 client.mu.RUnlock() 却没有先 RLock，
	// 对已连接的 server 重连必然触发 "sync: RUnlock of unlocked RWMutex".
	config := client.config
	m.mu.RUnlock()

	// 先断开旧连接(失败可忽略：目标就是重建)，再走统一的连接路径。
	if err := m.Disconnect(name); err != nil {
		log.Warnf("[MCP] reconnect: disconnect %q before reconnect failed: %v", name, err)
	}

	return m.connect(name, config)
}

// RefreshTools rediscovers tools from a connected MCP server
func (m *Manager) RefreshTools(name string) error {
	m.mu.RLock()
	client, exists := m.clients[name]
	if !exists {
		m.mu.RUnlock()
		return fmt.Errorf("MCP server '%s' not found", name)
	}
	m.mu.RUnlock()

	if err := client.discoverTools(context.Background()); err != nil {
		return fmt.Errorf("failed to refresh tools: %w", err)
	}

	return nil
}

// GetServerInfo returns information about a connected MCP server
func (m *Manager) GetServerInfo(name string) (map[string]interface{}, error) {
	m.mu.RLock()
	client, exists := m.clients[name]
	if !exists {
		m.mu.RUnlock()
		return nil, fmt.Errorf("MCP server '%s' not found", name)
	}
	// Snapshot every client field under the client lock: reading `connected`
	// after unlocking would race with Reconnect/disconnect paths.
	client.mu.RLock()
	toolCount := len(client.tools)
	lastCheck := client.lastHealthCheck
	connected := client.connected
	lastError := client.lastError
	transport := client.config.Transport
	client.mu.RUnlock()
	m.mu.RUnlock()

	return map[string]interface{}{
		"name":              name,
		"connected":         connected,
		"tool_count":        toolCount,
		"transport":         transport,
		"last_health_check": lastCheck,
		"last_error":        lastError,
	}, nil
}

func jsonMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		log.Errorf("[MCP] Failed to marshal JSON: %v", err)
		return nil
	}
	return data
}

// ConfigLoader loads MCP configuration
type ConfigLoader struct{}

// LoadFromConfig loads MCP servers from configuration.
//
// One broken server must not block the others: failures are collected and
// returned together (mirroring ConnectConfigured), so a single unreachable
// endpoint never hides the servers that connected fine.
func (cl *ConfigLoader) LoadFromConfig(mgr *Manager, servers map[string]ServerConfig) error {
	var failures []string
	for name, config := range servers {
		var err error
		switch config.Transport {
		case "sse":
			err = mgr.ConnectSSE(name, config)
		default:
			err = mgr.ConnectStdio(name, config)
		}

		if err != nil {
			// Connect* already recorded the failure on the manager; just collect
			// it here so every failed server is reported together.
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			log.Warnf("[MCP] failed to connect server %q: %v", name, err)
			continue
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to connect MCP server(s): %s", strings.Join(failures, "; "))
	}
	return nil
}
