// Package mcpprobe performs a one-shot MCP handshake (initialize + tools/list)
// against a remote HTTP or legacy HTTP+SSE server. It backs the bus-side "test
// connection" action; only the handshake and tool discovery are needed, so it
// is deliberately smaller than the agent's full MCP client. The stdio transport
// is intentionally unsupported (it cannot run in the bus image).
package mcpprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

var protocolVersions = []string{"2025-03-26", "2024-11-05", "2024-10-07"}

var idSeq atomic.Int64

func nextID() int64 { return idSeq.Add(1) }

// Tool is one tool advertised by the server.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Result is the outcome of a successful probe.
type Result struct {
	Tools           []Tool `json:"tools"`
	ServerName      string `json:"server_name,omitempty"`
	ServerVersion   string `json:"server_version,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type initResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type toolsResult struct {
	Tools []Tool `json:"tools"`
}

func initParams(version string) map[string]any {
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo":      map[string]any{"name": "nanobus", "version": "0.1"},
	}
}

// Probe connects to the server and returns the tools it exposes.
func Probe(ctx context.Context, transport, rawURL string, headers map[string]string) (*Result, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("mcp: url is required")
	}
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("mcp: url must be an http(s) URL")
	}
	switch transport {
	case "http":
		return probeHTTP(ctx, rawURL, headers)
	case "sse":
		return probeSSE(ctx, rawURL, headers)
	default:
		return nil, fmt.Errorf("unsupported transport %q", transport)
	}
}

func buildResult(initRaw, toolsRaw json.RawMessage) *Result {
	res := &Result{Tools: []Tool{}}
	var ir initResult
	if json.Unmarshal(initRaw, &ir) == nil {
		res.ProtocolVersion = ir.ProtocolVersion
		res.ServerName = ir.ServerInfo.Name
		res.ServerVersion = ir.ServerInfo.Version
	}
	var tr toolsResult
	if json.Unmarshal(toolsRaw, &tr) == nil && tr.Tools != nil {
		res.Tools = tr.Tools
	}
	return res
}

func probeHTTP(ctx context.Context, rawURL string, headers map[string]string) (*Result, error) {
	client := &http.Client{}
	sessionID := ""

	send := func(method string, params any, notify bool) (json.RawMessage, error) {
		body := jsonrpcRequest{JSONRPC: "2.0", Method: method, Params: params}
		if !notify {
			body.ID = nextID()
		}
		raw, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("mcp: request: %w", err)
		}
		defer resp.Body.Close()
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			sessionID = sid
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("mcp: HTTP %d: %s", resp.StatusCode, trim(string(bodyBytes)))
		}
		if notify {
			return nil, nil
		}
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			return parseSSEResponse(bodyBytes, body.ID)
		}
		return parseJSONResponse(bodyBytes)
	}

	var lastErr error
	for _, version := range protocolVersions {
		initRaw, err := send("initialize", initParams(version), false)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = send("notifications/initialized", nil, true)
		toolsRaw, err := send("tools/list", nil, false)
		if err != nil {
			lastErr = err
			continue
		}
		return buildResult(initRaw, toolsRaw), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no compatible protocol version")
	}
	return nil, lastErr
}

func probeSSE(ctx context.Context, rawURL string, headers map[string]string) (*Result, error) {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(connCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: sse connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mcp: sse HTTP %d", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	endpoint, err := readSSEEndpoint(br, rawURL)
	if err != nil {
		return nil, fmt.Errorf("mcp: sse endpoint: %w", err)
	}

	var mu sync.Mutex
	pending := map[int64]chan jsonrpcResponse{}
	go func() {
		for {
			_, data, err := readSSEEvent(br)
			if err != nil {
				mu.Lock()
				for id, ch := range pending {
					delete(pending, id)
					close(ch)
				}
				mu.Unlock()
				return
			}
			var r jsonrpcResponse
			if json.Unmarshal([]byte(data), &r) != nil {
				continue
			}
			mu.Lock()
			ch := pending[r.ID]
			delete(pending, r.ID)
			mu.Unlock()
			if ch != nil {
				ch <- r
			}
		}
	}()

	send := func(method string, params any, notify bool) (json.RawMessage, error) {
		id := nextID()
		body := jsonrpcRequest{JSONRPC: "2.0", Method: method, Params: params}
		if !notify {
			body.ID = id
		}
		raw, _ := json.Marshal(body)
		preq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		preq.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			preq.Header.Set(k, v)
		}
		var ch chan jsonrpcResponse
		if !notify {
			ch = make(chan jsonrpcResponse, 1)
			mu.Lock()
			pending[id] = ch
			mu.Unlock()
			defer func() {
				mu.Lock()
				delete(pending, id)
				mu.Unlock()
			}()
		}
		presp, err := (&http.Client{}).Do(preq)
		if err != nil {
			return nil, fmt.Errorf("mcp: sse post: %w", err)
		}
		_, _ = io.Copy(io.Discard, presp.Body)
		presp.Body.Close()
		if presp.StatusCode >= 400 {
			return nil, fmt.Errorf("mcp: sse post HTTP %d", presp.StatusCode)
		}
		if notify {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("mcp: stream closed")
			}
			if r.Error != nil {
				return nil, fmt.Errorf("mcp: %s", r.Error.Message)
			}
			return r.Result, nil
		}
	}

	var lastErr error
	for _, version := range protocolVersions {
		initRaw, err := send("initialize", initParams(version), false)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = send("notifications/initialized", nil, true)
		toolsRaw, err := send("tools/list", nil, false)
		if err != nil {
			lastErr = err
			continue
		}
		return buildResult(initRaw, toolsRaw), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no compatible protocol version")
	}
	return nil, lastErr
}

func parseJSONResponse(body []byte) (json.RawMessage, error) {
	var resp jsonrpcResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mcp: invalid json-rpc response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: %s", resp.Error.Message)
	}
	return resp.Result, nil
}

func parseSSEResponse(body []byte, id int64) (json.RawMessage, error) {
	for _, data := range splitSSE(body) {
		if strings.TrimSpace(data) == "" {
			continue
		}
		var resp jsonrpcResponse
		if json.Unmarshal([]byte(data), &resp) != nil || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp: %s", resp.Error.Message)
		}
		return resp.Result, nil
	}
	return nil, fmt.Errorf("mcp: no response for request %d", id)
}

func splitSSE(body []byte) []string {
	var out []string
	var lines []string
	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			if len(lines) > 0 {
				out = append(out, strings.Join(lines, "\n"))
				lines = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(line, "data:"))
		}
	}
	if len(lines) > 0 {
		out = append(out, strings.Join(lines, "\n"))
	}
	return out
}

func readSSEEndpoint(br *bufio.Reader, base string) (string, error) {
	for {
		event, data, err := readSSEEvent(br)
		if err != nil {
			return "", err
		}
		if event == "endpoint" && strings.TrimSpace(data) != "" {
			return resolveURL(base, strings.TrimSpace(data)), nil
		}
	}
}

func readSSEEvent(br *bufio.Reader) (event, data string, err error) {
	var dataLines []string
	for {
		line, rerr := br.ReadString('\n')
		if len(line) == 0 && rerr != nil {
			if len(dataLines) > 0 {
				return event, strings.Join(dataLines, "\n"), nil
			}
			return event, "", rerr
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 && event == "" {
				if rerr != nil {
					return event, "", rerr
				}
				continue
			}
			return event, strings.Join(dataLines, "\n"), nil
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
