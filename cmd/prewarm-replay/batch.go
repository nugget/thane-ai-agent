package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
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
	since := fs.Duration("since", 24*time.Hour, "look back window (e.g. 7d, 24h, 1h)")
	channel := fs.String("channel", "", "filter by ChannelBinding.Channel (e.g. signal); empty = all")
	limit := fs.Int("limit", 200, "maximum number of user messages to replay")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	rows, err := loadBatchTurns(s.thane, *since, *channel, *limit)
	if err != nil {
		return err
	}

	archive := memory.NewArchiveContextProvider(s.archive, g.MaxResults, g.MaxBytes, silentLogger())
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
	return renderBatchHuman(results, *since, *channel)
}

type batchTurn struct {
	convID  string
	content string
	at      time.Time
	binding *memory.ChannelBinding
}

func loadBatchTurns(db *sql.DB, since time.Duration, channelFilter string, limit int) ([]batchTurn, error) {
	cutoff := time.Now().Add(-since).Format(time.RFC3339)
	q := `SELECT m.conversation_id, m.content, m.timestamp,
	             json_extract(c.metadata, '$.channel_binding') AS binding_json
	      FROM messages m
	      JOIN conversations c ON c.id = m.conversation_id
	      WHERE m.role = 'user' AND m.timestamp >= ?
	      ORDER BY m.timestamp DESC
	      LIMIT ?`
	rows, err := db.Query(q, cutoff, limit)
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
				if channelFilter != "" && !strings.EqualFold(binding.Channel, channelFilter) {
					continue
				}
				t.binding = binding
			} else if channelFilter != "" {
				continue
			}
		} else if channelFilter != "" {
			continue
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
