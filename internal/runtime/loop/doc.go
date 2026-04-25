// Package loop implements persistent goroutine-based delegate loops —
// lightweight autonomous observers that run continuously alongside the
// main agent, with direct completion delivery or ordinary tool use for
// any durable artifacts they need to maintain.
//
// Loops are the universal primitive replacing previously-separate
// systems (metacognitive loop, observers) with a single
// [Registry.SpawnLoop] abstraction. Each loop is a background goroutine that
// iterates on a randomized bounded sleep schedule, running LLM iterations
// via the agent runner and optionally delivering results back through a
// completion path.
//
// A [Registry] tracks all active loops and provides visibility into what
// is running, their health, and resource usage. It enforces concurrency
// limits and coordinates graceful shutdown.
//
// See issue #509 for the full design.
package loop
