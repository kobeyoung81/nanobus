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

	// RPCRequests counts message dispatches by type. It is how a rollout of the
	// agent socket is verified: poll_inbound should collapse once agents connect.
	RPCRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bus_rpc_requests_total",
		Help: "Protocol messages dispatched, by type.",
	}, []string{"type"})

	// AgentSockets is the number of agent processes connected to this replica
	// over the WebSocket transport. The fallback poll rate is what it replaces.
	AgentSockets = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "bus_agent_sockets",
		Help: "Agent processes connected to this replica over the agent socket.",
	})

	// AgentSocketPushes counts push frames by kind (inbound, platform).
	AgentSocketPushes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bus_agent_socket_pushes_total",
		Help: "Push frames written to agent sockets, by kind.",
	}, []string{"kind"})

	// RateLimited counts calls refused by the REST rate limiter. It is the only
	// way to tell "the platform is busy" from "one caller is hammering it".
	RateLimited = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "bus_rate_limited_total",
		Help: "Requests refused by the REST rate limiter.",
	})

	// StreamWriteSeconds is the database write latency of stream events.
	StreamWriteSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "bus_stream_write_seconds",
		Help:    "Time spent writing one stream event batch to MongoDB.",
		Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1},
	})

	// VoiceSessions counts dictation sessions by how they ended. A burst of
	// client_close outcomes over one take is the signature of a browser that
	// keeps reconnecting, which is what made "the dictation cut off" invisible
	// in the logs.
	VoiceSessions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bus_voice_sessions_total",
		Help: "Voice dictation sessions handled by the relay, by outcome.",
	}, []string{"outcome"})

	// VoiceEvents counts what the provider sent us, by kind. A dictation that
	// produces several done events is normal (one per utterance); several
	// sessions for one dictation is not.
	VoiceEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bus_voice_events_total",
		Help: "Realtime events relayed to the browser, by kind.",
	}, []string{"kind"})
)

func init() {
	prometheus.MustRegister(StreamEventsWritten, StreamSubscribers, StreamWriteSeconds,
		AgentSockets, AgentSocketPushes, RPCRequests, RateLimited, VoiceSessions, VoiceEvents)
}

// Handler serves the Prometheus text exposition.
func Handler() http.Handler { return promhttp.Handler() }
