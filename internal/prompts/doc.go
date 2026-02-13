// Package prompts contains all LLM prompt templates used internally by Thane.
//
// Prompt text is Go code rather than config files because it is program logic:
// templates use fmt.Sprintf interpolation, benefit from compile-time embedding,
// and can be validated by tests. User-facing configuration lives in config.yaml;
// this package holds the instructions we send to models for internal operations
// (fact extraction, metadata generation, compaction summaries, etc.).
//
// Convention: each prompt category gets its own file (extraction.go,
// metadata.go, compaction.go) with an exported function that accepts the
// dynamic parts and returns the fully interpolated prompt string.
package prompts
