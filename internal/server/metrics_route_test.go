package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /metrics is reachable without the internal token (D7: internal network,
// anonymous scrape) and the counters move when events are accepted.
func TestMetricsEndpointCountsAcceptedEvents(t *testing.T) {
	s := NewServer(nil)
	handler := s.handler()

	post := httptest.NewRequest(http.MethodPost, "/api/v1/outbound/chunk",
		strings.NewReader(`{"channel":"web","chat_id":"c1","content":"x"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("chunk status = %d", rec.Code)
	}

	scratch := httptest.NewRecorder()
	handler.ServeHTTP(scratch, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if scratch.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", scratch.Code)
	}
	body := scratch.Body.String()
	for _, name := range []string{
		"bus_stream_events_written_total",
		"bus_stream_subscribers",
		"bus_stream_write_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("%s missing from /metrics", name)
		}
	}
	if !strings.Contains(body, `bus_stream_events_written_total{type="chunk"}`) {
		t.Errorf("the accepted chunk was not counted:\n%s", body)
	}
}
