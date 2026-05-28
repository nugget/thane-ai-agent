package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// cmdBatch replays every user message in a recent window of stored
// conversations, runs the prewarm providers against each, and prints
// aggregate stats — hit rate, coverage, and the negative-space turns
// (no prewarm context despite a non-trivial user message). Best
// suited for "where is prewarm silently empty?" sweeps.
func cmdBatch(g *globals, args []string) error {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	sinceStr := fs.String("since", "24h", "look back window (accepts time.ParseDuration plus the `d` day suffix, e.g. 7d, 24h, 90m)")
	channel := fs.String("channel", "", "filter by ChannelBinding.Channel (e.g. signal); empty = all")
	limit := fs.Int("limit", 200, "maximum number of user messages to replay (must be positive)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	since, err := parseSinceDuration(*sinceStr)
	if err != nil {
		return fmt.Errorf("batch: -since %q: %w", *sinceStr, err)
	}
	if *limit <= 0 {
		// Bare flag.Int passes negatives through, and SQLite reads a
		// negative LIMIT as "no limit" — a typo of -1 would replay the
		// entire archive into stdout. Reject early.
		return fmt.Errorf("batch: -limit must be > 0, got %d", *limit)
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	rows, err := loadBatchTurns(s.thane, since, *channel, *limit)
	if err != nil {
		return err
	}

	archive := memory.NewArchiveContextProvider(s.searcher(), g.MaxResults, g.MaxBytes, silentLogger())
	subjectP := knowledge.NewSubjectContextProvider(s.knowledge_, silentLogger())
	subjectP.SetMaxFacts(g.MaxFacts)

	ctx := context.Background()
	results := make([]runResult, 0, len(rows))
	for _, r := range rows {
		w := wake{
			ConversationID: r.convID,
			UserMessage:    r.content,
			Binding:        r.binding,
		}
		res, err := runProviders(ctx, w, archive, subjectP)
		if err != nil {
			return fmt.Errorf("conv %s: %w", r.convID, err)
		}
		// Drop the verbose Output for batch mode — only the summary stats matter
		// and printing 200 full JSON envelopes drowns the terminal.
		for i := range res.Providers {
			res.Providers[i].Output = ""
		}
		results = append(results, res)
	}

	if g.Format == "json" {
		return renderBatchJSON(results)
	}
	return renderBatchHuman(results, since, *channel)
}

// parseSinceDuration accepts the same suffixes as time.ParseDuration
// plus a `d` day suffix (since "7d" is the natural way an operator
// thinks about a one-week window). Pure-day inputs like "7d" route
// through a manual conversion before delegating to time.ParseDuration
// for everything else.
func parseSinceDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(s, "d"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid days value: %w", err)
		}
		if days < 0 {
			return 0, fmt.Errorf("days must be >= 0")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

type batchTurn struct {
	convID  string
	content string
	at      time.Time
	binding *memory.ChannelBinding
}

func loadBatchTurns(db *sql.DB, since time.Duration, channelFilter string, limit int) ([]batchTurn, error) {
	// Bind time.Time directly so the driver writes its own canonical
	// SQLiteTimestampLayout (space-separated). Formatting via RFC3339
	// would compare lexically against differently-shaped stored rows
	// and silently miss the lower edge of the window — see
	// internal/platform/database/timestamp.go.
	cutoff := time.Now().Add(-since)

	args := []any{cutoff}
	channelClause := ""
	if channelFilter != "" {
		// Apply the channel filter in SQL so LIMIT doesn't fill its
		// budget with non-matching rows first. json_extract returns
		// NULL for conversations without a channel_binding, which
		// naturally excludes them from the filtered window.
		channelClause = " AND json_extract(c.metadata, '$.channel_binding.channel') = ?"
		args = append(args, channelFilter)
	}
	args = append(args, limit)

	q := `SELECT m.conversation_id, m.content, m.timestamp,
	             json_extract(c.metadata, '$.channel_binding') AS binding_json
	      FROM messages m
	      JOIN conversations c ON c.id = m.conversation_id
	      WHERE m.role = 'user' AND m.timestamp >= ?` + channelClause + `
	      ORDER BY m.timestamp DESC
	      LIMIT ?`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query batch turns: %w", err)
	}
	defer rows.Close()

	var out []batchTurn
	for rows.Next() {
		var convID, content, ts string
		var bindingJSON sql.NullString
		if err := rows.Scan(&convID, &content, &ts, &bindingJSON); err != nil {
			return nil, err
		}
		t := batchTurn{convID: convID, content: content}
		t.at, _ = time.Parse(time.RFC3339, ts)
		if bindingJSON.Valid && bindingJSON.String != "" {
			binding, err := parseBinding(bindingJSON.String)
			if err == nil && binding != nil {
				t.binding = binding
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func parseBinding(s string) (*memory.ChannelBinding, error) {
	if s == "" {
		return nil, nil
	}
	return loadBindingFromJSON(s)
}
