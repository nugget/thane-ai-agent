package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// archiveResultByteCap is the per-tool byte ceiling on JSON output.
// The cap protects the model's context budget when an archive query
// returns more content than is useful; callers see truncated=true in
// the JSON envelope so the model can narrow its query and try again.
const archiveResultByteCap = 16000

// archiveTranscriptByteCap is the larger ceiling reserved for full
// session transcripts, which routinely run longer than search hits.
const archiveTranscriptByteCap = 32000

// fitPrefix returns the largest count k in [0, n] such that
// render(k) is within byteCap. render must produce monotonically
// non-decreasing output as k grows. Used by prefix-fit clipping
// (e.g., search results, where the tail entries are lower-relevance
// and are the right ones to drop). Output is always rendered with
// truncated=true when k < n.
func fitPrefix(n, byteCap int, render func(k int) []byte) []byte {
	if n == 0 {
		return render(0)
	}
	full := render(n)
	if len(full) <= byteCap {
		return full
	}
	// Binary search for the largest k that fits.
	low, high := 0, n
	for low < high {
		mid := (low + high + 1) / 2
		if len(render(mid)) <= byteCap {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return render(low)
}

// fitSuffix returns the smallest count k in [0, n] such that
// render(k) is within byteCap. render must produce monotonically
// non-increasing output as k grows (k is the number of items dropped
// from the front). Used by suffix-fit clipping where older entries
// are dropped first to preserve the most-recent tail.
func fitSuffix(n, byteCap int, render func(drop int) []byte) []byte {
	if n == 0 {
		return render(0)
	}
	full := render(0)
	if len(full) <= byteCap {
		return full
	}
	low, high := 0, n
	for low < high {
		mid := (low + high) / 2
		if len(render(mid)) <= byteCap {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return render(low)
}

// SetArchiveStore registers the four archive tools on the registry.
// Together they form Thane's long-term memory surface: search across
// past conversations, browse the catalog of sessions, pull a single
// session in full, and grab message history by time/conversation range.
func (r *Registry) SetArchiveStore(store *memory.ArchiveStore) {
	r.registerArchiveSearch(store)
	r.registerArchiveSessions(store)
	r.registerArchiveSessionTranscript(store)
	r.registerArchiveRange(store)
}

func (r *Registry) registerArchiveSearch(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_search",
		Description: "Search your conversation archive — semantic search across every past " +
			"session you've had with anyone. Use this when something jogs a memory or you " +
			"need context from a prior conversation. Each result is the matching message " +
			"plus the surrounding context window (bounded by natural silence gaps), so you " +
			"see a moment in conversation, not just an isolated line. Returns JSON with " +
			"delta-second timestamps. Pair with archive_session_transcript when a hit looks " +
			"worth reading in full.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "What you're looking for. Semantic search — phrasing matters less than concept.",
				},
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Optional: scope to one conversation. Omit to search across everything.",
				},
				"silence_minutes": map[string]any{
					"type":        "number",
					"description": "How long a silence gap before context expansion stops. Default: 10.",
				},
				"no_context": map[string]any{
					"type":        "boolean",
					"description": "If true, return only matches without surrounding context. Default: false.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Max results. Default: 5.",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}

			opts := memory.SearchOptions{
				Query: query,
				Limit: 5,
			}
			if convID, ok := args["conversation_id"].(string); ok && convID != "" {
				opts.ConversationID = convID
			}
			if mins, ok := args["silence_minutes"].(float64); ok && mins > 0 {
				opts.SilenceThreshold = time.Duration(mins) * time.Minute
			}
			if noCtx, ok := args["no_context"].(bool); ok {
				opts.NoContext = noCtx
			}
			if limit, ok := args["limit"].(float64); ok && limit > 0 {
				opts.Limit = int(limit)
			}

			results, err := store.Search(opts)
			if err != nil {
				return "", fmt.Errorf("archive search: %w", err)
			}

			// Fit to byte cap by dropping from the tail (lowest-relevance
			// hits go first). Binary search avoids O(n^2) re-marshaling.
			now := time.Now()
			data := fitPrefix(len(results), archiveResultByteCap, func(k int) []byte {
				return memory.FormatSearchResults(results[:k], now, k < len(results))
			})
			return string(data), nil
		},
	})
}

func (r *Registry) registerArchiveSessions(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_sessions",
		Description: "Browse your past sessions — the catalog of every closed conversation, " +
			"newest first. Each entry shows when it happened (delta seconds), how long it " +
			"ran, message count, title, tags, and summary. Use this when you want to flip " +
			"through history without a specific search query, or to find a session by " +
			"tag or title. Returns JSON. Once you spot one worth a closer look, " +
			"archive_session_transcript pulls the full transcript.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Optional: scope to one conversation. Omit to list across everything.",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Max sessions to return. Default: 20.",
				},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			convID, _ := args["conversation_id"].(string)
			limit := 20
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			sessions, err := store.ListSessions(convID, limit)
			if err != nil {
				return "", fmt.Errorf("list sessions: %w", err)
			}

			// Fit to byte cap by dropping older sessions (results come
			// newest-first, so the oldest entries fall off the tail).
			now := time.Now()
			data := fitPrefix(len(sessions), archiveResultByteCap, func(k int) []byte {
				return memory.FormatSessionsList(sessions[:k], now, k < len(sessions))
			})
			return string(data), nil
		},
	})
}

func (r *Registry) registerArchiveSessionTranscript(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_session_transcript",
		Description: "Read one past session in full. Pass either the full session ID or its " +
			"first 8 characters (longer prefixes are also fine). Returns the complete " +
			"message-by-message transcript as JSON, ordered chronologically with delta " +
			"timestamps. Best after archive_search or archive_sessions has narrowed you to " +
			"a specific session worth examining.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Full session ID or its first 8+ characters.",
				},
			},
			"required": []string{"session_id"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			sessionID, _ := args["session_id"].(string)
			if sessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}
			if len(sessionID) <= 8 {
				fullID, err := resolveShortSessionID(store, sessionID)
				if err != nil {
					return "", err
				}
				sessionID = fullID
			}

			messages, err := store.GetSessionTranscript(sessionID)
			if err != nil {
				return "", fmt.Errorf("get transcript: %w", err)
			}

			// Drop oldest messages first to fit the byte cap — the tail
			// is more useful when the model is following up on a recent
			// moment. Binary search avoids O(n^2) re-marshaling on long
			// transcripts.
			now := time.Now()
			data := fitSuffix(len(messages), archiveTranscriptByteCap, func(drop int) []byte {
				return memory.FormatRecentMessages(messages[drop:], now, drop > 0)
			})
			return string(data), nil
		},
	})
}

func (r *Registry) registerArchiveRange(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_range",
		Description: "Pull archived messages by time range — the verbatim history tool. " +
			"min_time / max_time accept either RFC3339 absolute timestamps or signed " +
			"deltas (\"-1800s\" = 30 minutes ago). min_messages acts as a floor: set it " +
			"to 50 and you'll get at least 50 of the most recent messages even on a quiet " +
			"conversation, regardless of min_time. Filter to one conversation_id or omit " +
			"it for everything. Crosses session boundaries — sessions are an internal " +
			"abstraction here; this tool just gives you the messages. Returns JSON with " +
			"delta-second timestamps and originating session IDs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Optional: scope to one conversation. Omit for all conversations.",
				},
				"min_time": map[string]any{
					"type": "string",
					"description": "Earliest timestamp to include. Accepts RFC3339 " +
						"(\"2026-04-25T14:00:00Z\") or signed delta (\"-1800s\"). Omit for unbounded.",
				},
				"max_time": map[string]any{
					"type":        "string",
					"description": "Latest timestamp to include. Same format as min_time. Default: now.",
				},
				"min_messages": map[string]any{
					"type": "number",
					"description": "Floor: return at least this many of the most recent messages even if " +
						"older than min_time. Useful for \"last X minutes OR Y messages, whichever is more.\" " +
						"Default: 0.",
				},
				"max_messages": map[string]any{
					"type":        "number",
					"description": "Cap on results. Default: 200.",
				},
				"exclude_session_id": map[string]any{
					"type": "string",
					"description": "Optional: drop messages from this session ID. Useful when you " +
						"want archived/older messages but not your current session's rows.",
				},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			now := time.Now()
			opts := memory.RangeOptions{}

			if convID, ok := args["conversation_id"].(string); ok && convID != "" {
				opts.ConversationID = convID
			}
			if v, ok := args["exclude_session_id"].(string); ok && v != "" {
				opts.ExcludeSessionID = v
			}
			if v, ok := args["min_time"].(string); ok && v != "" {
				t, err := promptfmt.ParseTimeOrDelta(v, now)
				if err != nil {
					return "", fmt.Errorf("min_time: %w", err)
				}
				opts.From = t
			}
			if v, ok := args["max_time"].(string); ok && v != "" {
				t, err := promptfmt.ParseTimeOrDelta(v, now)
				if err != nil {
					return "", fmt.Errorf("max_time: %w", err)
				}
				opts.To = t
			}
			if n, ok := args["min_messages"].(float64); ok && n > 0 {
				opts.MinMessages = int(n)
			}
			if n, ok := args["max_messages"].(float64); ok && n > 0 {
				opts.MaxMessages = int(n)
			}

			messages, truncated, err := store.GetMessagesInRange(opts)
			if err != nil {
				return "", fmt.Errorf("archive range: %w", err)
			}

			// Drop oldest messages first to fit the byte cap. The
			// query-level truncated flag stays sticky once set; the
			// fit pass only deepens it.
			data := fitSuffix(len(messages), archiveResultByteCap, func(drop int) []byte {
				return memory.FormatRecentMessages(messages[drop:], now, truncated || drop > 0)
			})
			return string(data), nil
		},
	})
}

// resolveShortSessionID finds a full session ID from a prefix.
func resolveShortSessionID(store *memory.ArchiveStore, prefix string) (string, error) {
	sessions, err := store.ListSessions("", 100)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, prefix) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session found with prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q matches %d sessions", prefix, len(matches))
	}
}
