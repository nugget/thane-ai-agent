// Package telemetry collects and publishes operational metrics via
// MQTT for Home Assistant sensor integration. Metrics include system
// health (DB sizes), token usage, active sessions, request performance,
// loop states, and attachment store statistics.
package telemetry

import "time"

// Metrics holds a point-in-time snapshot of all collected operational
// metrics. The zero value is safe — nil maps and zero counts produce
// valid (empty) sensor states.
type Metrics struct {
	CollectedAt time.Time

	// System health — database file sizes in bytes.
	DBSizes map[string]int64

	// Token usage (24h rolling window).
	TokensInput   int64
	TokensOutput  int64
	TokensCost    float64
	TokensByModel map[string]ModelTokens

	// Sessions & context.
	ActiveSessions     int
	ContextUtilization float64 // 0–100 percentage

	// Request performance (24h rolling window).
	Requests24h  int
	Errors24h    int
	LatencyP50Ms float64
	LatencyP95Ms float64

	// Loop aggregate counts.
	LoopsActive   int
	LoopsSleeping int
	LoopsErrored  int
	LoopsTotal    int
	LoopDetails   []LoopMetric

	// Attachment store.
	AttachmentsTotal      int64
	AttachmentsTotalBytes int64
	AttachmentsUnique     int64
}

// LoopMetric captures the state and iteration count for a single
// registered loop.
type LoopMetric struct {
	Name       string
	State      string
	Iterations int
}

// ModelTokens holds per-model token usage for the 24h window. Used
// as JSON attributes on the tokens_24h_cost sensor.
type ModelTokens struct {
	Input  int64   `json:"input"`
	Output int64   `json:"output"`
	Cost   float64 `json:"cost"`
}
