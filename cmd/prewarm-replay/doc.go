// Command prewarm-replay is a developer tool for tuning the
// pre-warm context providers (SubjectContextProvider,
// ArchiveContextProvider, and the similarity-based ContextProvider)
// against production data without spinning up the rest of the agent.
//
// The tool opens the prod sqlite databases read-only (so it is safe to
// run while the live agent is writing) and exposes three modes:
//
//	prewarm-replay replay --conv-id <id> [--message TEXT] [--subject KEY ...]
//	    Reconstruct a wake from a stored conversation: load the
//	    ChannelBinding from conversations.metadata, default the
//	    user message to the conversation's most recent user turn
//	    (override with --message), and run every prewarm provider
//	    against the resulting subjects. Best for "why didn't this
//	    turn get prewarm context?" investigations.
//
//	prewarm-replay query --message TEXT [--subject KEY ...] [--contact-id|--contact-address]
//	    Synthesize a wake without an archived turn behind it. Best
//	    for iterating on a hypothesis ("what if subject foo were
//	    here?") faster than waiting for a real turn.
//
//	prewarm-replay batch --since 7d [--channel signal]
//	    Replay every recent turn in a window, aggregate hit-rate /
//	    coverage / negative-space stats. Best for "where is the
//	    prewarm silently empty?" sweeps.
//
// The output is human-readable by default and a stable JSON object
// under `--format json` so an agent can pipe it into structured
// analysis (diffs, regression fixtures, threshold sweeps).
//
// This is a developer tool. It is not part of the serving path, does
// not need a config file, and intentionally bypasses the app
// initialization so you can poke at providers in isolation.
package main
