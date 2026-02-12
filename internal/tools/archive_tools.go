package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// SetArchiveStore adds conversation archive tools to the registry.
func (r *Registry) SetArchiveStore(store *memory.ArchiveStore) {
	r.registerArchiveSearch(store)
	r.registerArchiveSessionList(store)
	r.registerArchiveSessionGet(store)
}

func (r *Registry) registerArchiveSearch(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_search",
		Description: "Search your conversation archive — your long-term memory of past sessions. " +
			"Use this to recall what was discussed previously, find decisions made, " +
			"look up things the user told you, or recover context from earlier conversations. " +
			"Returns matching messages with surrounding conversation context, " +
			"bounded by natural silence gaps in the conversation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query — what you're looking for in past conversations",
				},
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Optional: filter to a specific conversation ID",
				},
				"silence_minutes": map[string]any{
					"type":        "number",
					"description": "Minutes of silence that defines a conversation boundary for context expansion. Default: 10",
				},
				"no_context": map[string]any{
					"type":        "boolean",
					"description": "If true, return only matching messages without surrounding context. Default: false",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of results. Default: 5",
				},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
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

			if len(results) == 0 {
				return "No results found in conversation archive.", nil
			}

			// Cap results to prevent context flooding (182KB observed in production)
			const maxResultBytes = 16000 // ~4K tokens — enough for useful context
			formatted := formatSearchResults(results)
			if len(formatted) > maxResultBytes {
				// Truncate and indicate more results exist
				formatted = formatted[:maxResultBytes] + fmt.Sprintf(
					"\n\n[Truncated: %d bytes total, showing first %d bytes. Use archive_session_transcript for full context of a specific session.]",
					len(formatted), maxResultBytes,
				)
			}
			return formatted, nil
		},
	})
}

func (r *Registry) registerArchiveSessionList(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_sessions",
		Description: "List past conversation sessions from the archive. " +
			"Shows when sessions started/ended, message counts, and end reasons. " +
			"Use to understand conversation history or find a specific session to examine.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"conversation_id": map[string]any{
					"type":        "string",
					"description": "Optional: filter to a specific conversation ID",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Maximum number of sessions to return. Default: 20",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			convID, _ := args["conversation_id"].(string)
			limit := 20
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			sessions, err := store.ListSessions(convID, limit)
			if err != nil {
				return "", fmt.Errorf("list sessions: %w", err)
			}

			if len(sessions) == 0 {
				return "No sessions found in the archive.", nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Found %d sessions:\n\n", len(sessions)))

			for _, s := range sessions {
				endInfo := "active"
				if s.EndedAt != nil {
					duration := s.EndedAt.Sub(s.StartedAt).Round(time.Minute)
					endInfo = fmt.Sprintf("ended (%s, %s)", s.EndReason, duration)
				}
				title := memory.ShortID(s.ID)
				if s.Title != "" {
					// Sanitize: single line, cap length
					t := strings.ReplaceAll(s.Title, "\n", " ")
					if len(t) > 80 {
						t = t[:80] + "…"
					}
					title = fmt.Sprintf("%s — %s", memory.ShortID(s.ID), t)
				}
				sb.WriteString(fmt.Sprintf("- **%s** — %s, %d messages, %s\n",
					title,
					s.StartedAt.Format("2006-01-02 15:04"),
					s.MessageCount,
					endInfo,
				))
				if s.Summary != "" {
					sb.WriteString(fmt.Sprintf("  *%s*\n", s.Summary))
				}
				if len(s.Tags) > 0 {
					sb.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(s.Tags, ", ")))
				}
			}

			return sb.String(), nil
		},
	})
}

func (r *Registry) registerArchiveSessionGet(store *memory.ArchiveStore) {
	r.Register(&Tool{
		Name: "archive_session_transcript",
		Description: "Retrieve the full transcript of a past conversation session. " +
			"Returns all messages in chronological order. Use after archive_sessions " +
			"to read a specific session, or after archive_search to get full context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "Session ID (or first 8 characters) to retrieve",
				},
				"format": map[string]any{
					"type":        "string",
					"enum":        []string{"text", "json"},
					"description": "Output format. 'text' is human-readable, 'json' is structured. Default: text",
				},
			},
			"required": []string{"session_id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			sessionID, _ := args["session_id"].(string)
			if sessionID == "" {
				return "", fmt.Errorf("session_id is required")
			}

			format, _ := args["format"].(string)
			if format == "" {
				format = "text"
			}

			// Support short IDs — look up full ID from session list
			if len(sessionID) <= 8 {
				fullID, err := resolveShortSessionID(store, sessionID)
				if err != nil {
					return "", err
				}
				sessionID = fullID
			}

			const maxTranscriptBytes = 32000 // ~8K tokens

			if format == "json" {
				messages, err := store.GetSessionTranscript(sessionID)
				if err != nil {
					return "", fmt.Errorf("get transcript: %w", err)
				}
				data, _ := json.MarshalIndent(messages, "", "  ")
				result := string(data)
				if len(result) > maxTranscriptBytes {
					result = result[:maxTranscriptBytes] + fmt.Sprintf(
						"\n\n[Truncated: %d bytes total. Full transcript has %d messages.]",
						len(result), len(messages),
					)
				}
				return result, nil
			}

			// Text format — use markdown export
			md, err := store.ExportSessionMarkdown(sessionID)
			if err != nil {
				return "", fmt.Errorf("export session: %w", err)
			}
			if len(md) > maxTranscriptBytes {
				md = md[:maxTranscriptBytes] + fmt.Sprintf(
					"\n\n[Truncated: %d bytes total. Use archive_search to find specific content.]",
					len(md),
				)
			}
			return md, nil
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

// formatSearchResults formats archive search results for the agent.
func formatSearchResults(results []memory.SearchResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(results)))

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("--- Result %d (session %s) ---\n", i+1, memory.ShortID(r.SessionID)))

		// Context before
		for _, m := range r.ContextBefore {
			sb.WriteString(formatArchiveMessage(m))
		}

		// The match itself (highlighted)
		sb.WriteString(fmt.Sprintf(">>> [%s] %s: %s\n",
			r.Match.Timestamp.Format("2006-01-02 15:04:05"),
			r.Match.Role,
			r.Match.Content,
		))

		// Context after
		for _, m := range r.ContextAfter {
			sb.WriteString(formatArchiveMessage(m))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}

func formatArchiveMessage(m memory.ArchivedMessage) string {
	return fmt.Sprintf("    [%s] %s: %s\n",
		m.Timestamp.Format("15:04:05"),
		m.Role,
		truncate(m.Content, 500),
	)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
