package contacts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// TestWriteDossierShrinksOnlyASmallOverrun pins the dossier writer's
// wiring against the real contacts-root validator: a dossier refused only
// for a status line a little over budget lands shortened and its result
// reports the saved text, while one that also breaks a dossier rule gets
// exactly the refusal it gets with no shrinker and never reaches the model.
func TestWriteDossierShrinksOnlyASmallOverrun(t *testing.T) {
	const saved = "Steady collaborator; prefers direct technical threads."
	sentStatus := strings.Repeat("s", 142)
	for _, tt := range []struct {
		name      string
		teaser    string
		reply     string
		wantCalls int
		wantSaved bool
	}{
		{name: "only the overrun", teaser: "Useful hook.", reply: saved, wantCalls: 1, wantSaved: true},
		{name: "the overrun and the subject's name", teaser: "Dossier Person is a useful hook.", reply: saved, wantCalls: 0},
		// The document store refuses a rendered delta in prose; one the
		// shrink model wrote leaves the model's own refusal standing.
		{name: "a shortened text the store would refuse", teaser: "Useful hook.", reply: "Steady collaborator; next call +5d, by phone.", wantCalls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := func(contactID string) DossierWriteArgs {
				return DossierWriteArgs{
					ContactID: contactID, StatusLine: sentStatus, Teaser: tt.teaser,
					Digest: "Enough context to act.", Full: "Detail. — evidence: archive:session:" + citeUniqueID,
				}
			}
			offTools, offID := newCitationTestTools(t)
			newDossierLifecycleStore(t, offTools)
			_, off := offTools.WriteDossier(context.Background(), args(offID))
			if off == nil {
				t.Fatal("unconfigured dossier write accepted an over-budget status line")
			}

			tools, contactID := newCitationTestTools(t)
			_, docTools, dir := newDossierLifecycleStore(t, tools)
			calls := 0
			tools.ConfigureDossierDocuments(docTools.Read, docTools.WriteFaceted, &documentfacets.Shrinker{
				Model:  "fast-model",
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Shrink: func(context.Context, string, int) (string, error) {
					calls++
					return tt.reply, nil
				},
			})
			result, err := tools.WriteDossier(context.Background(), args(contactID))
			if calls != tt.wantCalls {
				t.Fatalf("shrink calls = %d, want %d", calls, tt.wantCalls)
			}
			path := filepath.Join(dir, uuid.MustParse(contactID).String()+".md")
			if !tt.wantSaved {
				if err == nil || err.Error() != off.Error() {
					t.Fatalf("refusal = %v, want exactly the unconfigured refusal %v", err, off)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("refused dossier left a file (stat err %v)", statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteDossier() error = %v, want the shortened dossier to land", err)
			}
			var decoded struct {
				Shortened []documentfacets.Shortened `json:"shortened_projections"`
			}
			if err := json.Unmarshal([]byte(result), &decoded); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, result)
			}
			if len(decoded.Shortened) != 1 || decoded.Shortened[0].SavedText != saved || decoded.Shortened[0].SentCharacters != 142 {
				t.Fatalf("result = %s, want status_line reported shortened from 142 to the saved text", result)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read dossier: %v", err)
			}
			if !strings.Contains(string(body), "## Status Line\n\n"+saved+"\n") || strings.Contains(string(body), sentStatus) {
				t.Fatalf("dossier does not carry the shortened status line:\n%s", body)
			}
		})
	}
}
