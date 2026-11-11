package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// statusOverrun is a status line 22 words long, two over its 20-word
// budget: an overrun small enough for a shrinker to take on.
var statusOverrun = strings.TrimSpace(strings.Repeat("word ", 22))

// replyingShrinker returns a Shrinker that answers every call with text and
// counts the calls.
func replyingShrinker(text string, calls *int) *documentfacets.Shrinker {
	return &documentfacets.Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shrink: func(context.Context, string, documentfacets.Field, string) (string, error) {
			*calls++
			return text, nil
		},
	}
}

// TestDocWriteSavesASmallOverrunShortened pins the generic writer's wiring:
// a doc_write refused only for a status line a little over budget lands with
// the shortened text, and its result gives the model the exact saved text
// and both counts beside a warning that says what happened. The reply
// carries a non-breaking hyphen the store folds to a plain one, so the
// saved text reported is the one stored, not the one the model returned.
func TestDocWriteSavesASmallOverrunShortened(t *testing.T) {
	t.Parallel()

	const reply = "Printer online and non\u2011stop; two packages wait at the desk."
	const saved = "Printer online and non-stop; two packages wait at the desk."
	store, kbDir := newMutationStore(t)
	calls := 0
	sent := strings.TrimSpace(strings.Repeat("word ", 22))
	result, err := NewTools(store, replyingShrinker(reply, &calls)).Publish(context.Background(), PublishArgs{
		Ref: "kb:status.md", StatusLine: sent, Full: "Complete detail.",
	})
	if err != nil {
		t.Fatalf("Publish() error = %v, want the shortened write to land", err)
	}
	if calls != 1 {
		t.Fatalf("shrink calls = %d, want 1", calls)
	}
	var decoded struct {
		Applied   bool     `json:"applied"`
		Warnings  []string `json:"warnings"`
		Shortened []struct {
			Projection string `json:"projection"`
			Limit      string `json:"limit"`
			Sent       string `json:"sent"`
			Saved      string `json:"saved"`
			SavedText  string `json:"saved_text"`
		} `json:"shortened_projections"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}
	if !decoded.Applied || len(decoded.Shortened) != 1 {
		t.Fatalf("result = %s, want an applied write reporting one shortened projection", result)
	}
	got := decoded.Shortened[0]
	if got.Projection != "status_line" || got.Limit != "20 words" || got.Sent != "22 words" || got.Saved != "10 words" || got.SavedText != saved {
		t.Errorf("shortened_projections[0] = %+v, want status_line 22 -> 10 words against 20 with the stored text %q", got, saved)
	}
	if len(decoded.Warnings) != 1 || decoded.Warnings[0] != shortenedWarning {
		t.Errorf("warnings = %q, want the shortened-projections note", decoded.Warnings)
	}
	body, err := os.ReadFile(filepath.Join(kbDir, "status.md"))
	if err != nil {
		t.Fatalf("read written document: %v", err)
	}
	if !strings.Contains(string(body), "## Status Line\n\n"+saved+"\n") || strings.Contains(string(body), sent) {
		t.Errorf("written document does not carry the shortened status line:\n%s", body)
	}
}

// TestDocWriteShortenedOntoTheStoredTextIsUnchanged pins what a model reads
// when shortening lands its over-budget text on what is already stored. The
// write is unchanged, and the warning that says so speaks of the document
// the write would save, which shortened_projections explains, not of what
// the model sent, which never matched.
func TestDocWriteShortenedOntoTheStoredTextIsUnchanged(t *testing.T) {
	t.Parallel()

	const saved = "Printer online; two packages wait at the desk."
	store, _ := newMutationStore(t)
	calls := 0
	tools := NewTools(store, replyingShrinker(saved, &calls))
	sent := strings.TrimSpace(strings.Repeat("word ", 25))
	args := PublishArgs{Ref: "kb:status.md", StatusLine: sent, Full: "Complete detail."}
	for i, wantUnchanged := range []bool{false, true} {
		result, err := tools.Publish(context.Background(), args)
		if err != nil {
			t.Fatalf("publish %d: error = %v", i+1, err)
		}
		var decoded struct {
			Unchanged bool     `json:"unchanged"`
			Warnings  []string `json:"warnings"`
			Shortened []struct {
				SavedText string `json:"saved_text"`
			} `json:"shortened_projections"`
		}
		if err := json.Unmarshal([]byte(result), &decoded); err != nil {
			t.Fatalf("publish %d: result is not JSON: %v\n%s", i+1, err, result)
		}
		if decoded.Unchanged != wantUnchanged || len(decoded.Shortened) != 1 || decoded.Shortened[0].SavedText != saved {
			t.Errorf("publish %d: result = %s, want unchanged = %v with one projection shortened to %q", i+1, result, wantUnchanged, saved)
		}
		said := strings.Contains(strings.Join(decoded.Warnings, "\n"), "unchanged: the document this write would save is byte for byte the one already stored, so no new revision was created")
		if said != wantUnchanged {
			t.Errorf("publish %d: warnings = %q, want the unchanged warning = %v", i+1, decoded.Warnings, wantUnchanged)
		}
	}
	if calls != 2 {
		t.Errorf("shrink calls = %d, want one per publish", calls)
	}
}

// TestDocWriteRefusalIsUnchangedWhenShrinkCannotHelp pins that the model
// reads exactly the refusal it reads with no shrinker configured, whether
// the shrink result does not fit or another violation rules shrinking out.
func TestDocWriteRefusalIsUnchangedWhenShrinkCannotHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		args      PublishArgs
		reply     string
		wantCalls int
	}{
		{
			name:  "the shrink result is still over",
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: statusOverrun, Full: "Complete detail."},
			reply: strings.TrimSpace(strings.Repeat("r ", 21)), wantCalls: 2,
		},
		{
			name:  "another violation beside the overrun",
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: statusOverrun, Full: "## Details\n\nsmuggled"},
			reply: "Short.", wantCalls: 0,
		},
		{
			// The store refuses a rendered delta in prose. One the shrink
			// model wrote must come back as the model's own refusal, not
			// as an error quoting text the model never sent.
			name:  "the shrink result bakes in a rendered delta",
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: statusOverrun, Full: "Complete detail."},
			reply: "Printer online; toner due +5d, order confirmed by email.", wantCalls: 1,
		},
		{
			// The store folds a non-breaking hyphen before its guard runs,
			// so the check of shortened text folds it first too.
			name:  "the shrink result bakes in a delta behind a glyph artifact",
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: statusOverrun, Full: "Complete detail."},
			reply: "Printer back online ‑2h after the outage.", wantCalls: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			offStore, _ := newMutationStore(t)
			_, off := NewTools(offStore, nil).Publish(context.Background(), tt.args)
			store, kbDir := newMutationStore(t)
			calls := 0
			_, err := NewTools(store, replyingShrinker(tt.reply, &calls)).Publish(context.Background(), tt.args)
			if off == nil || err == nil || err.Error() != off.Error() {
				t.Fatalf("refusal = %v, want exactly the unconfigured refusal %v", err, off)
			}
			if calls != tt.wantCalls {
				t.Fatalf("shrink calls = %d, want %d", calls, tt.wantCalls)
			}
			if _, statErr := os.Stat(filepath.Join(kbDir, "status.md")); !os.IsNotExist(statErr) {
				t.Fatalf("refused write left a document (stat err %v)", statErr)
			}
		})
	}
}

// TestDocWriteLogsWhetherTheShortenedTextWasSaved pins the operator's record
// of a shortened write against what the store did with it: one record per
// shortened projection, written once the store has answered, saying the text
// was saved, or that it was not and the store's reason. A refusal that has
// nothing to do with the text, a placement rule or a read the loop has not
// made, reaches the author exactly as it does once the author fixes the
// overrun itself.
func TestDocWriteLogsWhetherTheShortenedTextWasSaved(t *testing.T) {
	t.Parallel()

	const reply = "Printer online."
	for _, tt := range []struct {
		name       string
		store      func(t *testing.T) *Store
		args       PublishArgs
		wantMsg    string
		wantReason string
	}{
		{
			name:    "saved",
			store:   func(t *testing.T) *Store { store, _ := newMutationStore(t); return store },
			args:    PublishArgs{Ref: "kb:status.md", Full: "Complete detail."},
			wantMsg: "projection shortened to fit its budget",
		},
		{
			name:       "a placement rule refuses the ref",
			store:      func(t *testing.T) *Store { store, _ := newMutationStoreForRoot(t, "dossiers"); return store },
			args:       PublishArgs{Ref: "dossiers:nested/person.md", Full: "Complete detail."},
			wantMsg:    "shortened projection not saved",
			wantReason: "dossiers accepts direct-child refs only",
		},
		{
			name: "no read on record for the loop",
			store: func(t *testing.T) *Store {
				_, store, _ := newReceiptLifecycleTools(t)
				if _, err := NewTools(store, nil).Publish(context.Background(), PublishArgs{Ref: "projects:status.md", StatusLine: "Seed.", Full: "Seed detail."}); err != nil {
					t.Fatalf("seed: %v", err)
				}
				return store
			},
			args:       PublishArgs{Ref: "projects:status.md", Full: "New detail.", ReceiptScope: "loop:other"},
			wantMsg:    "shortened projection not saved",
			wantReason: "read projects:status.md before replacing its whole body",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixed := tt.args
			fixed.StatusLine = reply
			wantResult, wantErr := NewTools(tt.store(t), nil).Publish(ctx, fixed)

			var logs strings.Builder
			shrinker := &documentfacets.Shrinker{
				Model:  "fast-model",
				Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
				Shrink: func(context.Context, string, documentfacets.Field, string) (string, error) { return reply, nil },
			}
			args := tt.args
			args.StatusLine = statusOverrun
			result, err := NewTools(tt.store(t), shrinker).Publish(ctx, args)
			if tt.wantReason != "" && (result != wantResult || fmt.Sprint(err) != fmt.Sprint(wantErr)) {
				t.Errorf("Publish() = %s, %v; want what the author gets once it fixes the overrun itself: %s, %v", result, err, wantResult, wantErr)
			}

			var records []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("log line %q is not JSON: %v", line, err)
				}
				records = append(records, record)
			}
			if len(records) != 1 {
				t.Fatalf("log = %s, want one record for status_line", logs.String())
			}
			got := records[0]
			reason, _ := got["reason"].(string)
			if got["msg"] != tt.wantMsg || got["facet"] != "status_line" || got["new_text"] != reply || !strings.Contains(reason, tt.wantReason) || (tt.wantReason == "") != (reason == "") {
				t.Errorf("log record = %v, want %q for status_line with new_text %q and a reason naming %q", got, tt.wantMsg, reply, tt.wantReason)
			}
		})
	}
}
