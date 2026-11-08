package mcpprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonRPCServer answers initialize + tools/list over streamable HTTP with plain
// JSON responses.
func jsonRPCServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"serverInfo":      map[string]string{"name": "fixture", "version": "1"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{{"name": "hello", "description": "greets"}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestProbeHTTP(t *testing.T) {
	srv := jsonRPCServer(t)
	defer srv.Close()

	res, err := Probe(context.Background(), "http", srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.ServerName != "fixture" || res.ProtocolVersion != "2025-03-26" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "hello" {
		t.Fatalf("tools = %+v", res.Tools)
	}
}

func TestProbeSSE(t *testing.T) {
	responses := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("no flusher")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", "/msg")
			flusher.Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case msg := <-responses:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
					flusher.Flush()
				}
			}
		}
		// POST /msg: echo the request id back as a JSON-RPC response.
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusAccepted)
		if req.ID == 0 {
			return // notification
		}
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-03-26","serverInfo":{"name":"ssefix","version":"1"}}`
		case "tools/list":
			result = `{"tools":[{"name":"remote"}]}`
		default:
			result = `{}`
		}
		responses <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	defer srv.Close()

	res, err := Probe(context.Background(), "sse", srv.URL, nil)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.ServerName != "ssefix" {
		t.Fatalf("server name = %q", res.ServerName)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "remote" {
		t.Fatalf("tools = %+v", res.Tools)
	}
}

func TestProbeRejectsUnsupportedTransport(t *testing.T) {
	if _, err := Probe(context.Background(), "stdio", "http://localhost", nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported transport error, got %v", err)
	}
}

func TestReadSSEEvent(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("event: message\ndata: {\"id\":1}\n\n"))
	event, data, err := readSSEEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if event != "message" || data != `{"id":1}` {
		t.Fatalf("event=%q data=%q", event, data)
	}
}
