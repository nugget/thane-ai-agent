package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// render dispatches to the chosen output formatter for one or more
// single-wake results.
func render(format string, results []runResult) error {
	switch format {
	case "json":
		return renderJSON(results)
	default:
		return renderHuman(results)
	}
}

func renderJSON(results []runResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if len(results) == 1 {
		return enc.Encode(results[0])
	}
	return enc.Encode(results)
}

func renderHuman(results []runResult) error {
	for i, r := range results {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("=== %s ===\n", r.ConversationID)
		fmt.Printf("User message:  %s\n", oneline(r.UserMessage, 120))
		if r.Binding != nil {
			fmt.Printf("Binding:       channel=%s address=%s contact_id=%s contact_name=%s trust=%s\n",
				orDash(r.Binding.Channel), orDash(r.Binding.Address),
				orDash(r.Binding.ContactID), orDash(r.Binding.ContactName),
				orDash(r.Binding.TrustZone),
			)
		} else {
			fmt.Println("Binding:       (none)")
		}
		fmt.Printf("Subjects:      %s\n", formatList(r.Subjects))
		fmt.Println()
		for _, p := range r.Providers {
			fmt.Printf("[%s]\n", p.Name)
			if p.Query != "" {
				fmt.Printf("  query:        %q (source=%s)\n", p.Query, p.QuerySource)
			}
			fmt.Printf("  hits:         %d\n", p.HitCount)
			fmt.Printf("  output bytes: %d\n", p.OutputBytes)
			if p.Output != "" {
				fmt.Println("  output:")
				printIndented(p.Output, "    ")
			}
			fmt.Println()
		}
	}
	return nil
}

// renderBatchHuman summarizes a batch run: per-provider hit rates and
// the negative-space subset.
func renderBatchHuman(results []runResult, since time.Duration, channel string) error {
	chLabel := channel
	if chLabel == "" {
		chLabel = "all"
	}
	fmt.Printf("=== batch %s channel since %s (%d turns) ===\n", chLabel, since.String(), len(results))

	bySubject := newCoverage()
	byArchive := newCoverage()
	for _, r := range results {
		for _, p := range r.Providers {
			switch p.Name {
			case "SubjectContextProvider":
				bySubject.add(p)
			case "ArchiveContextProvider":
				byArchive.add(p)
			}
		}
	}

	fmt.Println()
	fmt.Println("SubjectContextProvider")
	bySubject.print()
	fmt.Println()
	fmt.Println("ArchiveContextProvider")
	byArchive.print()

	// Negative space: prewarm produced no hits despite a user message
	// long enough that the model would normally benefit from context.
	const minInterestingMessage = 40
	var dark []runResult
	for _, r := range results {
		if len(strings.TrimSpace(r.UserMessage)) < minInterestingMessage {
			continue
		}
		anyHits := false
		for _, p := range r.Providers {
			if p.HitCount > 0 {
				anyHits = true
				break
			}
		}
		if !anyHits {
			dark = append(dark, r)
		}
	}
	fmt.Println()
	fmt.Printf("Negative space: %d turns ≥%d chars with zero prewarm hits\n", len(dark), minInterestingMessage)
	for _, r := range dark {
		bind := "(none)"
		if r.Binding != nil {
			bind = strings.Join(filterEmpty([]string{r.Binding.Channel, r.Binding.ContactID, r.Binding.Address}), "/")
		}
		fmt.Printf("  %s  binding=%s  msg=%q\n", r.ConversationID, bind, oneline(r.UserMessage, 80))
	}
	return nil
}

func renderBatchJSON(results []runResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// coverage tracks per-provider aggregate stats over a batch.
type coverage struct {
	turns      int
	withHits   int
	totalHits  int
	totalBytes int
}

func newCoverage() *coverage { return &coverage{} }

func (c *coverage) add(p providerResult) {
	c.turns++
	if p.HitCount > 0 {
		c.withHits++
		c.totalHits += p.HitCount
	}
	c.totalBytes += p.OutputBytes
}

func (c *coverage) print() {
	if c.turns == 0 {
		fmt.Println("  no observations")
		return
	}
	fmt.Printf("  turns observed:        %d\n", c.turns)
	fmt.Printf("  turns with hits:       %d (%.0f%%)\n", c.withHits, percent(c.withHits, c.turns))
	if c.withHits > 0 {
		fmt.Printf("  avg hits per firing:   %.1f\n", float64(c.totalHits)/float64(c.withHits))
	}
	fmt.Printf("  avg bytes per turn:    %.0f\n", float64(c.totalBytes)/float64(c.turns))
}

func percent(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}

func oneline(s string, max int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func formatList(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

func filterEmpty(items []string) []string {
	out := items[:0]
	for _, s := range items {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func printIndented(body, prefix string) {
	for _, line := range strings.Split(body, "\n") {
		fmt.Println(prefix + line)
	}
}
