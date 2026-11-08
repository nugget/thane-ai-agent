package contacts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
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

// dossierTargets are the budgeted dossier projections, each with its
// limit, a sent value a quarter over it, and the clean reply it gets when
// another projection carries the case's reply.
var dossierTargets = []struct {
	key, sent, clean string
	limit            int
}{
	{key: "status_line", limit: 120, sent: strings.Repeat("s", 150), clean: "Clean status."},
	{key: "teaser", limit: 500, sent: strings.Repeat("t", 625), clean: "Clean teaser."},
	{key: "digest", limit: 2048, sent: strings.Repeat("d", 2560), clean: "Clean digest."},
}

// dossierHostileReply is one shrink-model reply, the projections that
// refuse it once the store has folded it, and an optional author full
// written beside it. "{id}" stands for the dossier's own contact UUID.
type dossierHostileReply struct {
	name      string
	reply     string
	refusedIn []string
	full      string
	// nearCeiling sizes the author's full, dense with non-breaking spaces,
	// to sit 100 bytes under the rendered ceiling as sent, so a multi-byte
	// reply takes the raw text over it while the folded body stays under.
	nearCeiling bool
}

var (
	everyDossierTarget = []string{"status_line", "teaser", "digest"}
	nameDossierTargets = []string{"status_line", "teaser"}
)

// dossierHostileReplies is the dossier writer's corpus: the glyph
// impostors, rendered deltas and multi-line shapes the generic writer's
// corpus carries, plus each impostor in the subject's name, the subject's
// UUID, and an evidence citation, which only the dossier's own rules (and
// the contacts-root validator under them) refuse.
func dossierHostileReplies() []dossierHostileReply {
	rows := []dossierHostileReply{
		// The two examples Copilot gave, verbatim.
		{name: "copilot: ##\u00a0Details", reply: "##\u00a0Details", refusedIn: everyDossierTarget},
		{name: "copilot: Dossier\u00a0Person", reply: "Dossier\u00a0Person", refusedIn: nameDossierTargets},

		{name: "a clean sentence", reply: "Steady collaborator; prefers direct technical threads."},
		{name: "a non-breaking space between words", reply: "Steady\u00a0collaborator; prefers direct threads."},
		{name: "the subject's name", reply: "Dossier Person prefers direct threads.", refusedIn: nameDossierTargets},
		{name: "the subject's UUID", reply: "Linked to {id}.", refusedIn: everyDossierTarget},
		{name: "a leading part of a session id", reply: "Evidence: archive:session:0190aaaa", refusedIn: everyDossierTarget},
		{name: "a rendered delta", reply: "Next call +5d, by phone.", refusedIn: everyDossierTarget},
		{name: "a rendered delta in backticks", reply: "Next call `+5d`, by phone."},
		{name: "a reserved heading in another case", reply: "## teaser", refusedIn: everyDossierTarget},
		{name: "a line that is a reserved heading", reply: "Body.\n## Digest\nMore.", refusedIn: everyDossierTarget},
		{name: "lines starting with #", reply: "# Heading\nBody line.\n#tag"},
		{name: "a thematic break line", reply: "Above.\n---\nBelow."},
		{name: "a fenced block with a delta in it", reply: "Intro.\n```\nlast seen +5d\n```\nOutro."},
		{name: "a blank-line run", reply: "First.\n\n\n\nSecond."},
		{name: "CRLF line endings", reply: "First.\r\nSecond.\r\nThird."},
		{name: "trailing spaces and tabs", reply: "First.   \n\tSecond."},

		{name: "the author's full hides a reserved heading behind U+00A0", reply: "Clean reply.", full: "Detail.\n\n##\u00a0Details\n\nMore.", refusedIn: everyDossierTarget},
		{name: "the author's full names the subject's UUID behind U+2011", reply: "Clean reply.", full: "Detail about {id-U+2011}.", refusedIn: everyDossierTarget},
		{name: "a multi-byte reply over a near-ceiling full whose glyphs fold", nearCeiling: true},
	}
	for _, imp := range []struct{ name, glyph string }{{"U+00A0", "\u00a0"}, {"U+202F", "\u202f"}, {"U+2010", "\u2010"}, {"U+2011", "\u2011"}} {
		// An impostor space completes a heading, a name or a broken
		// citation; an impostor hyphen completes a UUID or a delta's sign.
		var spaced, hyphened, name []string
		if imp.glyph == "\u00a0" || imp.glyph == "\u202f" {
			spaced, name = everyDossierTarget, nameDossierTargets
		} else {
			hyphened = everyDossierTarget
		}
		for _, heading := range []string{"Status Line", "Teaser", "Digest", "Details"} {
			rows = append(rows,
				dossierHostileReply{name: imp.name + " after ## of " + heading, reply: "##" + imp.glyph + heading, refusedIn: spaced},
				dossierHostileReply{name: imp.name + " after ## of " + heading + " as a line", reply: "Opening.\n\n##" + imp.glyph + heading + "\n\nClosing.", refusedIn: spaced},
			)
		}
		rows = append(rows,
			dossierHostileReply{name: imp.name + " inside the Status Line heading", reply: "## Status" + imp.glyph + "Line", refusedIn: spaced},
			dossierHostileReply{name: imp.name + " in the subject's name", reply: "Dossier" + imp.glyph + "Person keeps threads direct.", refusedIn: name},
			dossierHostileReply{name: imp.name + " for the hyphens of the subject's UUID", reply: "Linked to {id-" + imp.name + "}.", refusedIn: hyphened},
			dossierHostileReply{name: imp.name + " for the hyphens of a session citation", reply: "Evidence: archive:session:" + strings.ReplaceAll(citeUniqueID, "-", imp.glyph), refusedIn: spaced},
			dossierHostileReply{name: imp.name + " before a rendered delta", reply: "Next call" + imp.glyph + "+5d, by phone.", refusedIn: everyDossierTarget},
			dossierHostileReply{name: imp.name + " inside a rendered delta", reply: "Next call +5" + imp.glyph + "d, by phone."},
			dossierHostileReply{name: imp.name + " as a rendered delta's sign", reply: "Back " + imp.glyph + "2h after the outage.", refusedIn: hyphened},
		)
	}
	return rows
}

// lands reports whether a dossier shortened to row's reply in target
// should land.
func (row dossierHostileReply) lands(target string) bool {
	if target == "status_line" && strings.ContainsAny(row.reply, "\r\n") {
		return false
	}
	return !slices.Contains(row.refusedIn, target)
}

// withContactID spells "{id}" as the contact's UUID and "{id-U+XXXX}" as
// that UUID with each hyphen replaced by the named glyph impostor.
func withContactID(text, id string) string {
	for name, glyph := range map[string]string{"U+00A0": "\u00a0", "U+202F": "\u202f", "U+2010": "\u2010", "U+2011": "\u2011"} {
		text = strings.ReplaceAll(text, "{id-"+name+"}", strings.ReplaceAll(id, "-", glyph))
	}
	return strings.ReplaceAll(text, "{id}", id)
}

// TestShortenedDossierLandsAsStoredOrNotAtAll pins the invariant
// documents.StoredPayload documents through the dossier writer, wired as
// in production (one shrinker for both its own Fit and WriteFaceted's),
// and the real contacts root with its dossier validator: for any shrink
// reply, the dossier lands with every shortened projection read back from
// disk equal to the saved_text the result reports, or it returns byte for
// byte the refusal the same write returns with no shrinker, and leaves no
// file. Every reply runs in each budgeted projection, once as the only
// overrun and once with all three over and the others given clean
// replies.
func TestShortenedDossierLandsAsStoredOrNotAtAll(t *testing.T) {
	t.Parallel()

	for _, row := range dossierHostileReplies() {
		for _, target := range dossierTargets {
			for _, together := range []bool{false, true} {
				name := row.name + "/in " + target.key
				if together {
					name += " with every projection over"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					tools, contactID := newCitationTestTools(t)
					store, _, dir := newDossierLifecycleStore(t, tools)
					sent := documentfacets.Payload{StatusLine: "Current state.", Teaser: "Open for the hook.", Digest: "Enough to act on.", Full: "Complete detail."}
					replies := map[int]string{}
					for _, over := range dossierTargets {
						if over.key != target.key && !together {
							continue
						}
						setDossierProjection(&sent, over.key, over.sent)
						replies[over.limit] = over.clean
					}
					replies[target.limit] = withContactID(row.reply, contactID)
					switch {
					case row.nearCeiling:
						replies[target.limit] = strings.Repeat("語", target.limit*4/5)
						sent.Full = dossierFillToCeiling(sent)
					case row.full != "":
						sent.Full = withContactID(row.full, contactID)
					}
					assertDossierShrinkOutcome(t, tools, store, filepath.Join(dir, contactID+".md"), contactID, sent, replies, row.lands(target.key))
				})
			}
		}
	}
}

func setDossierProjection(payload *documentfacets.Payload, key, value string) {
	switch key {
	case "status_line":
		payload.StatusLine = value
	case "teaser":
		payload.Teaser = value
	case "digest":
		payload.Digest = value
	}
}

// dossierFillToCeiling returns a full dense with non-breaking spaces that
// puts payload's rendered body 100 bytes under the ceiling.
func dossierFillToCeiling(payload documentfacets.Payload) string {
	payload.Full = "x"
	size := documentfacets.MaxDocumentBytes - 100 - (len(dossierOutputContract.Render(payload)) - 1)
	units := (size - 1) / 3
	return strings.Repeat("x\u00a0", units) + strings.Repeat("x", size-3*units)
}

// assertDossierShrinkOutcome writes sent as contactID's dossier with no
// shrinker, then with one answering replies by limit, and checks the
// second write's outcome is one of the two documents.StoredPayload allows
// and the one wantLands names.
func assertDossierShrinkOutcome(t *testing.T, tools *Tools, store *documents.Store, path, contactID string, sent documentfacets.Payload, replies map[int]string, wantLands bool) {
	t.Helper()
	ctx := context.Background()
	args := DossierWriteArgs{ContactID: contactID, StatusLine: sent.StatusLine, Teaser: sent.Teaser, Digest: sent.Digest, Full: sent.Full}
	result, off := tools.WriteDossier(ctx, args)
	if off == nil {
		t.Fatalf("unconfigured dossier write landed (%s), want an overrun refusal", result)
	}
	shrinker := &documentfacets.Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shrink: func(_ context.Context, _ string, limit int) (string, error) { return replies[limit], nil },
	}
	docTools := documents.NewTools(store, shrinker)
	tools.ConfigureDossierDocuments(docTools.Read, docTools.WriteFaceted, shrinker)
	result, err := tools.WriteDossier(ctx, args)
	if err != nil {
		if err.Error() != off.Error() {
			t.Fatalf("refusal = %q\nwant exactly the unconfigured refusal %q", err, off)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("refused dossier left a file (stat err %v)", statErr)
		}
		if wantLands {
			t.Fatal("the dossier returned the original refusal, want it to land shortened")
		}
		return
	}
	var decoded struct {
		Applied   bool                       `json:"applied"`
		Shortened []documentfacets.Shortened `json:"shortened_projections"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil || !decoded.Applied {
		t.Fatalf("result = %s (%v), want an applied write or the unconfigured refusal", result, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the landed dossier: %v", err)
	}
	_, body, found := strings.Cut(strings.TrimPrefix(string(raw), "---\n"), "\n---\n")
	if !found {
		t.Fatalf("landed dossier has no frontmatter:\n%s", raw)
	}
	stored := dossierOutputContract.Parse(body)
	shortened := map[string]bool{}
	for _, entry := range decoded.Shortened {
		shortened[entry.Projection] = true
		if got, _ := stored.ByKey(entry.Projection); got != entry.SavedText || entry.SavedCharacters != utf8.RuneCountInString(got) {
			t.Errorf("stored %s = %q, but the result reports saved_text %q (%d characters)", entry.Projection, got, entry.SavedText, entry.SavedCharacters)
		}
	}
	folded := strings.NewReplacer("\u00a0", " ", "\u202f", " ", "\u2010", "-", "\u2011", "-")
	for _, field := range dossierOutputContract.Fields() {
		sentValue, _ := sent.ByKey(field.Key)
		storedValue, _ := stored.ByKey(field.Key)
		switch over := field.MaxRunes > 0 && utf8.RuneCountInString(sentValue) > field.MaxRunes; {
		case over != shortened[field.Key]:
			t.Errorf("%s over its budget = %v, reported shortened = %v", field.Key, over, shortened[field.Key])
		case !over && storedValue != folded.Replace(strings.TrimSpace(sentValue)):
			t.Errorf("stored %s = %q, want the author's own text %q", field.Key, storedValue, sentValue)
		}
	}
	if !wantLands {
		t.Errorf("the dossier landed with %+v, want the original refusal", decoded.Shortened)
	}
}

// TestUnshortenedDossierMeetsTheStoreAsBefore pins that only shortened
// text is judged in its stored form before the write: an author's own
// teaser that names the subject behind a non-breaking space still gets the
// contacts-root validator's own refusal, with or without a shrinker.
func TestUnshortenedDossierMeetsTheStoreAsBefore(t *testing.T) {
	for _, configured := range []bool{false, true} {
		tools, contactID := newCitationTestTools(t)
		store, _, _ := newDossierLifecycleStore(t, tools)
		if configured {
			shrinker := &documentfacets.Shrinker{Model: "fast-model", Shrink: func(context.Context, string, int) (string, error) { return "", nil }}
			docTools := documents.NewTools(store, shrinker)
			tools.ConfigureDossierDocuments(docTools.Read, docTools.WriteFaceted, shrinker)
		}
		_, err := tools.WriteDossier(context.Background(), DossierWriteArgs{
			ContactID: contactID, StatusLine: "Current state.", Teaser: "Dossier\u00a0Person is a useful hook.",
			Digest: "Enough context to act.", Full: "Complete detail.",
		})
		if want := "validate " + DossierRef(uuid.MustParse(contactID)) + " write: "; err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("shrinker configured %v: WriteDossier() error = %v, want the contacts root's own refusal (%q...)", configured, err, want)
		}
	}
}
