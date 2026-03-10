// Package loop implements persistent goroutine-based delegate loops —
// lightweight autonomous observers that run continuously alongside the
// main agent, reporting via output targets without blocking conversation
// flow.
//
// Loops are the universal primitive replacing three currently-separate
// systems (metacognitive loop, anticipations, observers) with a single
// [SpawnLoop] abstraction. Each loop is a background goroutine that
// iterates on a randomized bounded sleep schedule, running LLM iterations
// via the agent runner and optionally writing observations to output
// targets.
//
// A [Registry] tracks all active loops and provides visibility into what
// is running, their health, and resource usage. It enforces concurrency
// limits and coordinates graceful shutdown.
//
// See issue #509 for the full design.
package loop
