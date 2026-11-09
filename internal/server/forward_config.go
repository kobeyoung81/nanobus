package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// maxConfigBody bounds a forwarded config write. The largest value is a PEM
// keypair, so a megabyte is generous and still refuses a runaway body.
const maxConfigBody = 1 << 20

// forwardConfigWrite sends a config write to the admin module, which owns the
// admin_ collections, and relays its answer.
//
// The bus keeps the route because its callers (the portal, nanoauth's keystore,
// the protocol's set_config_item) already speak it; what changed is who writes
// the collection. When no admin URL is configured the write is refused rather
// than silently dropped — a "save" that reports success must have landed
// somewhere.
func (s *Server) forwardConfigWrite(w http.ResponseWriter, r *http.Request, component, key string, body []byte) bool {
	if s.adminURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin module is not configured"})
		return false
	}

	target := s.adminURL + "/api/v1/config/" + url.PathEscape(component) + "/" + url.PathEscape(key)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	if s.internalToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.internalToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("forward config write", "component", component, "key", key, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "admin module unreachable"})
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, maxConfigBody))
	if resp.StatusCode != http.StatusOK {
		slog.Error("config write rejected upstream", "component", component, "key", key, "status", resp.StatusCode)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(payload)
		return false
	}
	return true
}

// configWriteBody is the shape the admin API accepts.
func configWriteBody(value, valueType, description string) []byte {
	body := map[string]string{"value": value}
	if valueType != "" {
		body["value_type"] = valueType
	}
	if description != "" {
		body["description"] = description
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

// passthroughJSON writes the value back with the keys the old handler answered,
// so existing callers see no change.
func configWriteResponse(component, key, value string) map[string]any {
	return map[string]any{"component_type": component, "key": key, "value": value}
}
