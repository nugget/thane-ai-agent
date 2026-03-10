// Package openclaw replicates OpenClaw's agent behavior on Thane's plumbing.
//
// When a request arrives with model "thane:openclaw", this package handles
// workspace file injection, skill discovery, and system prompt assembly
// following OpenClaw v2026.2.9 conventions. The core types are defined in
// [config.OpenClawConfig]; this package provides runtime behavior.
package openclaw
