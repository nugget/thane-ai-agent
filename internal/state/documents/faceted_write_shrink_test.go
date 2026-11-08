package documents

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// replyingShrinker returns a Shrinker that answers every call with text and
// counts the calls.
func replyingShrinker(text string, calls *int) *documentfacets.Shrinker {
	return &documentfacets.Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shrink: func(context.Context, string, int) (string, error) {
			*calls++
			return text, nil
		},
	}
}

// TestDocWriteSavesASmallOverrunShortened pins the generic writer's wiring:
// a doc_write refused only for a status line a little over budget lands with
// the shortened text, and its result gives the model the exact saved text
// and both counts beside a warning that says what happened.
func TestDocWriteSavesASmallOverrunShortened(t *testing.T) {
	t.Parallel()

	const saved = "Printer online; two packages wait at the desk."
	store, kbDir := newMutationStore(t)
	calls := 0
	sent := strings.Repeat("s", 142)
	result, err := NewTools(store, replyingShrinker(saved, &calls)).Publish(context.Background(), PublishArgs{
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
			Projection      string `json:"projection"`
			Limit           int    `json:"limit"`
			SentCharacters  int    `json:"sent_characters"`
			SavedCharacters int    `json:"saved_characters"`
			SavedText       string `json:"saved_text"`
		} `json:"shortened_projections"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, result)
	}
	if !decoded.Applied || len(decoded.Shortened) != 1 {
		t.Fatalf("result = %s, want an applied write reporting one shortened projection", result)
	}
	got := decoded.Shortened[0]
	if got.Projection != "status_line" || got.Limit != 120 || got.SentCharacters != 142 || got.SavedCharacters != len(saved) || got.SavedText != saved {
		t.Errorf("shortened_projections[0] = %+v, want status_line 142 -> %d with the saved text", got, len(saved))
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
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: strings.Repeat("s", 142), Full: "Complete detail."},
			reply: strings.Repeat("r", 121), wantCalls: 2,
		},
		{
			name:  "another violation beside the overrun",
			args:  PublishArgs{Ref: "kb:status.md", StatusLine: strings.Repeat("s", 142), Full: "## Details\n\nsmuggled"},
			reply: "Short.", wantCalls: 0,
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
