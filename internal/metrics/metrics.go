// Package metrics exposes the bus's operational numbers.
//
// They answer the three questions the streaming design has to be judged by:
// how much is being written, by how many subscribers, and how long the writes
// take. The endpoint is internal-only (the bus's network is not public) and
// anonymous, so a scraper needs no credential.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// StreamEventsWritten counts accepted stream events by type (chunk,
	// complete). It counts what the bus accepted, not database documents: the
	// write coalescer may store several events as one row.
	StreamEventsWritten = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bus_stream_events_written_total",
		Help: "Stream events accepted by the bus, by type.",
	}, []string{"type"})

	// StreamSubscribers is the number of open outbound/event streams.
	StreamSubscribers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bus_stream_subscribers",
		Help: "Open stream subscriptions (per conversation and platform events).",
	})

	// StreamWriteSeconds is the database write latency of stream events.
	StreamWriteSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "bus_stream_write_seconds",
		Help:    "Time spent writing one stream event batch to MongoDB.",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1},
	})
)

func init() {
	prometheus.MustRegister(StreamEventsWritten, StreamSubscribers, StreamWriteSeconds)
}

// Handler serves the Prometheus text exposition.
func Handler() http.Handler { return promhttp.Handler() }
