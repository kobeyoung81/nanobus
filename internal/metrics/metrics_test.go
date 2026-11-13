package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The three numbers the streaming design is judged by must be exposed under the
// names a scraper will look for.
func TestHandlerExposesTheStreamMetrics(t *testing.T) {
	StreamEventsWritten.WithLabelValues("chunk").Inc()
	StreamSubscribers.Set(3)
	StreamWriteSeconds.Observe(0.002)

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{
		"bus_stream_events_written_total",
		"bus_stream_subscribers",
		"bus_stream_write_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("%s is missing from the exposition", name)
		}
	}
}
