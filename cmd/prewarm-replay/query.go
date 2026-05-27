package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// cmdQuery synthesizes a wake from inline inputs. Unlike replay, it
// does not read any stored conversation metadata — the caller
// supplies the ChannelBinding fields and subjects directly. Best for
// "what if I had this subject?" iteration.
//
// In raw-search mode it instead bypasses the prewarm provider and
// calls ArchiveStore.Search directly so an experimenter can see
// what the underlying search returns before any prewarm trimming,
// optionally filtering out hits from one or more
// "current-conversation" IDs (the contamination-source experiment).
func cmdQuery(g *globals, args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	message := fs.String("message", "", "user message text (required)")
	contactID := fs.String("contact-id", "", "ContactID for synthetic ChannelBinding")
	contactAddress := fs.String("contact-address", "", "Address (phone/email) for synthetic ChannelBinding")
	rawSearchMode := fs.Bool("raw-search", false, "bypass the prewarm provider and call ArchiveStore.Search directly; useful for examining retrieval quality without prewarm trimming")
	rawLimit := fs.Int("limit", 10, "limit on raw search results (raw-search mode only)")
	excludeConvs := stringList{}
	fs.Var(&excludeConvs, "exclude-conv", "conversation_id whose hits should be dropped post-search (repeatable; raw-search mode only)")
	subjects := stringList{}
	fs.Var(&subjects, "subject", "extra subject to inject (repeatable; e.g. entity:light.office)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *message == "" && len(subjects) == 0 && *contactID == "" && *contactAddress == "" {
		return fmt.Errorf("query: at least one of -message, -subject, -contact-id, -contact-address is required")
	}

	s, err := openStores(g.DataDir)
	if err != nil {
		return err
	}
	defer s.close()

	if *rawSearchMode {
		if *message == "" {
			return fmt.Errorf("query -raw-search: -message is required")
		}
		hits, err := rawSearch(s.archive, *message, *rawLimit, []string(excludeConvs))
		if err != nil {
			return err
		}
		return renderRaw(g.Format, *message, hits, []string(excludeConvs))
	}

	var binding *memory.ChannelBinding
	if *contactID != "" || *contactAddress != "" {
		binding = &memory.ChannelBinding{
			ContactID: *contactID,
			Address:   *contactAddress,
		}
	}

	archive := memory.NewArchiveContextProvider(s.archive, g.MaxResults, g.MaxBytes, silentLogger())
	subjectP := knowledge.NewSubjectContextProvider(s.knowledge_, silentLogger())
	subjectP.SetMaxFacts(g.MaxFacts)

	res, err := runProviders(context.Background(), wake{
		ConversationID: "(synthetic)",
		UserMessage:    *message,
		Binding:        binding,
		ExtraSubjects:  []string(subjects),
	}, archive, subjectP)
	if err != nil {
		return err
	}

	return render(g.Format, []runResult{res})
}

func renderRaw(format, query string, hits []rawHit, excluded []string) error {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Query    string         `json:"query"`
			Excluded []string       `json:"excluded_conversations,omitempty"`
			HitCount int            `json:"hit_count"`
			Roles    map[string]int `json:"role_histogram"`
			Hits     []rawHit       `json:"hits"`
		}{
			Query:    query,
			Excluded: excluded,
			HitCount: len(hits),
			Roles:    roleHistogram(hits),
			Hits:     hits,
		})
	}
	fmt.Print(formatRawHitsHuman("raw search", query, hits, excluded))
	return nil
}
