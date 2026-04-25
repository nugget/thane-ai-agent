// Package iterate provides a shared model iteration engine for
// agentic LLM workflows. An [Engine] repeatedly calls an LLM,
// inspects the response for tool calls, executes those tools, and
// feeds results back until the model produces a text-only response
// or a budget is exhausted.
//
// Both the primary agent loop and the lightweight delegate executor
// are consumers of this engine, configured via [Config] callbacks to
// layer their own streaming, archival, timeout, and budget logic on
// top of the shared iteration core.
package iterate
