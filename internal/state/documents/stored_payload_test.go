package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// shrinkTargets are the projections a shrink can touch, each with
// a sent value a quarter over its budget (status_line's in words), the
// clean reply it gets when another projection carries the case's reply, and
// the length of a multi-byte reply that sits within its limits.
var shrinkTargets = []struct {
	key, sent, clean string
	replyRunes       int
}{
	{key: "status_line", sent: strings.TrimSpace(strings.Repeat("s ", 25)), clean: "Clean status.", replyRunes: 96},
	{key: "teaser", sent: strings.Repeat("t", 625), clean: "Clean teaser.", replyRunes: 400},
}

// glyphImpostors are the characters the store folds before it saves.
var glyphImpostors = []struct{ name, glyph, folded string }{
	{"U+00A0", "\u00a0", " "}, {"U+202F", "\u202f", " "}, {"U+2010", "\u2010", "-"}, {"U+2011", "\u2011", "-"},
}

// hostileReply is one shrink-model reply, which projections refuse it once
// the store has folded it, and an optional author full the case writes
// beside it.
type hostileReply struct {
	name      string
	reply     string
	refusedIn []string
	// nearCeiling sizes the author's full, dense with non-breaking spaces,
	// to sit 100 bytes under the rendered ceiling as sent, so a multi-byte
	// reply takes the raw text over it while the folded body stays under.
	nearCeiling bool
	full        string
}

var everywhere = []string{"status_line", "teaser"}

// hostileShrinkReplies is doc_write's invariant corpus (the dossier test
// carries its own, with the dossier's rules added): every glyph impostor
// in every reserved heading, alone and as a line of a longer reply;
// rendered deltas plain, quoted, and behind or inside an impostor;
// multi-line shapes a Markdown body or the codec could misread; and an
// author's own text that the store would fold into a refusal. Only an
// impostor that folds to a space can complete a reserved heading.
func hostileShrinkReplies() []hostileReply {
	rows := []hostileReply{
		// The two examples Copilot gave, verbatim.
		{name: "copilot: ##\u00a0Details", reply: "##\u00a0Details", refusedIn: everywhere},
		{name: "copilot: Dossier\u00a0Person", reply: "Dossier\u00a0Person"},

		{name: "a clean sentence", reply: "Printer online; two packages wait at the desk."},
		{name: "surrounding whitespace", reply: "  \tPrinter online.\u00a0 "},
		{name: "only impostor spaces", reply: "\u00a0\u202f", refusedIn: everywhere},
		{name: "a non-breaking space between words", reply: "Steady\u00a0collaborator; prefers direct threads."},
		{name: "hyphen impostors between words", reply: "A non\u2011stop, well\u2010known printer."},
		{name: "a rendered delta", reply: "Toner due +5d, order confirmed.", refusedIn: everywhere},
		{name: "a rendered delta in backticks", reply: "Toner due `+5d`, order confirmed."},
		{name: "a level-2 heading that is not reserved", reply: "## Notes"},
		{name: "a reserved heading in another case", reply: "## teaser", refusedIn: everywhere},
		{name: "another projection's heading as text", reply: "Status Line"},

		{name: "lines starting with #", reply: "# Heading\nBody line.\n#tag"},
		{name: "a line that is a reserved heading", reply: "Body.\n## Digest\nMore.", refusedIn: everywhere},
		{name: "a thematic break line", reply: "Above.\n---\nBelow."},
		{name: "a frontmatter-shaped block", reply: "---\ntitle: injected\n---\nBody."},
		{name: "a fenced block with a delta in it", reply: "Intro.\n```\nlast seen +5d\n```\nOutro."},
		// A fence left open exempts every projection after it from the
		// store's prose guard, the author's own included; StoredPayload
		// cannot tell the author's open fence from the reply's.
		{name: "an unterminated fence", reply: "Intro.\n```\ncode", refusedIn: everywhere},
		{name: "a line that opens a fence and says something", reply: "~~~ Printer online.", refusedIn: everywhere},
		{name: "an impostor space indenting a fence", reply: "Intro.\n ```\ncode", refusedIn: everywhere},
		{name: "an unterminated fence beside the author's delta in full", reply: "Summary.\n```", full: "Contract renewed -3d by the vendor.", refusedIn: everywhere},
		{name: "the author's full leaves a fence open", reply: "Clean reply.", full: "Detail.\n```\ncode", refusedIn: everywhere},
		{name: "a blank-line run", reply: "First.\n\n\n\nSecond."},
		{name: "CRLF line endings", reply: "First.\r\nSecond.\r\nThird."},
		{name: "trailing spaces on a line", reply: "First.   \nSecond."},
		{name: "tabs", reply: "Key:\tvalue\n\tindented"},
		{name: "an impostor space alone on a line", reply: "First.\n\u00a0\nSecond."},

		{name: "the author's full hides a reserved heading behind U+00A0", reply: "Clean reply.", full: "Detail.\n\n##\u00a0Details\n\nMore.", refusedIn: everywhere},
		{name: "the author's full bakes a delta behind U+2011", reply: "Clean reply.", full: "Detail; renewed \u20113d ago.", refusedIn: everywhere},
		{name: "a multi-byte reply over a near-ceiling full whose glyphs fold", nearCeiling: true},
	}
	for _, imp := range glyphImpostors {
		var refused []string
		if imp.folded == " " {
			refused = everywhere
		}
		for _, heading := range []string{"Status Line", "Teaser", "Digest", "Details"} {
			rows = append(rows,
				hostileReply{name: fmt.Sprintf("%s after ## of %s", imp.name, heading), reply: "##" + imp.glyph + heading, refusedIn: refused},
				hostileReply{name: fmt.Sprintf("%s after ## of %s as a line", imp.name, heading), reply: "Opening.\n\n##" + imp.glyph + heading + "\n\nClosing.", refusedIn: refused},
			)
		}
		rows = append(rows,
			hostileReply{name: imp.name + " inside the Status Line heading", reply: "## Status" + imp.glyph + "Line", refusedIn: refused},
			hostileReply{name: imp.name + " inside the Status Line heading as a line", reply: "Opening.\n## Status" + imp.glyph + "Line\nClosing.", refusedIn: refused},
			hostileReply{name: imp.name + " before a rendered delta", reply: "Toner due" + imp.glyph + "+5d, order confirmed.", refusedIn: everywhere},
			hostileReply{name: imp.name + " inside a rendered delta", reply: "Toner due +5" + imp.glyph + "d, order confirmed."},
		)
		var signRefused []string
		if imp.folded == "-" {
			signRefused = everywhere
		}
		rows = append(rows, hostileReply{name: imp.name + " as a rendered delta's sign", reply: "Printer back " + imp.glyph + "2h after the outage.", refusedIn: signRefused})
	}
	return rows
}

// lands reports whether a write shortened to row's reply in target should
// land: a projection the row is refused in, or a line break in a
// single-line projection, leaves the author's refusal standing.
func (row hostileReply) lands(target string) bool {
	if target == "status_line" && strings.ContainsAny(row.reply, "\r\n") {
		return false
	}
	return !slices.Contains(row.refusedIn, target)
}

// lengthIn renders text's length in the unit limit names ("20 words",
// "500 characters"), as a shortened projection reports it.
func lengthIn(limit, text string) string {
	n, unit := utf8.RuneCountInString(text), "character"
	if strings.HasSuffix(limit, " words") {
		n, unit = documentfacets.WordCount(text), "word"
	}
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// overBudget reports whether value is over any of field's limits.
func overBudget(field documentfacets.Field, value string) bool {
	return (field.MaxWords > 0 && documentfacets.WordCount(value) > field.MaxWords) ||
		(field.MaxRunes > 0 && utf8.RuneCountInString(value) > field.MaxRunes)
}

// multiByteReply returns a reply of n characters, each three bytes.
func multiByteReply(n int) string { return strings.Repeat("語", n) }

// fillToCeiling returns a full dense with non-breaking spaces that puts
// payload's rendered body 100 bytes under the ceiling.
func fillToCeiling(contract documentfacets.Contract, payload documentfacets.Payload) string {
	payload.Full = "x"
	size := documentfacets.MaxDocumentBytes - 100 - (len(contract.Render(payload)) - 1)
	units := (size - 1) / 3
	return strings.Repeat("x\u00a0", units) + strings.Repeat("x", size-3*units)
}

// keyShrinker answers each Shrink call with the reply for the projection
// it names.
func keyShrinker(replies map[string]string) *documentfacets.Shrinker {
	return &documentfacets.Shrinker{
		Model:  "fast-model",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Shrink: func(_ context.Context, _ string, field documentfacets.Field, _ string) (string, error) {
			return replies[field.Key], nil
		},
	}
}

// TestShortenedDocWriteLandsAsStoredOrNotAtAll pins the invariant
// [StoredPayload] documents, through doc_write's writer and the real
// document store: for any shrink reply, the write lands with every
// shortened projection read back from disk equal to the saved_text the
// result reports, or it returns byte for byte the refusal the same write
// returns with no shrinker, and leaves nothing on disk. Every reply runs
// in each projection a shrink can touch, once as the only overrun and once
// with both over and the other given a clean reply.
func TestShortenedDocWriteLandsAsStoredOrNotAtAll(t *testing.T) {
	t.Parallel()

	contract := documentfacets.DefaultContract()
	for _, row := range hostileShrinkReplies() {
		for _, target := range shrinkTargets {
			for _, together := range []bool{false, true} {
				name := fmt.Sprintf("%s/in %s", row.name, target.key)
				if together {
					name += " with every projection over"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					sent := documentfacets.Payload{StatusLine: "Current state.", Teaser: "Open for the hook.", Digest: "Enough to act on.", Full: "Complete detail."}
					replies := map[string]string{}
					for _, over := range shrinkTargets {
						if over.key != target.key && !together {
							continue
						}
						setProjection(&sent, over.key, over.sent)
						replies[over.key] = over.clean
					}
					replies[target.key] = row.reply
					switch {
					case row.nearCeiling:
						replies[target.key] = multiByteReply(target.replyRunes)
						sent.Full = fillToCeiling(contract, sent)
					case row.full != "":
						sent.Full = row.full
					}
					assertShrinkOutcome(t, contract, sent, keyShrinker(replies), row.lands(target.key))
				})
			}
		}
	}

	// A JSON projection the shrink reply spaces with a non-breaking space
	// is not JSON as sent, but it is once the store folds it, and the
	// store's own check of the body passes it.
	t.Run("a JSON projection spaced with U+00A0", func(t *testing.T) {
		t.Parallel()
		jsonContract := documentfacets.Contract{Facets: []documentfacets.Spec{{Name: documentfacets.StatusLine, Format: documentfacets.FormatJSON}}}
		// A JSON projection is budgeted in characters, so its overrun is
		// one long value.
		sent := documentfacets.Payload{StatusLine: `{"state":"` + strings.Repeat("s", 190) + `"}`, Full: "Complete detail."}
		assertShrinkOutcome(t, jsonContract, sent, keyShrinker(map[string]string{"status_line": "{\"state\":\u00a0\"ok\"}"}), true)
	})
}

func setProjection(payload *documentfacets.Payload, key, value string) {
	switch key {
	case "status_line":
		payload.StatusLine = value
	case "teaser":
		payload.Teaser = value
	}
}

// assertShrinkOutcome writes sent through WriteFaceted against a fresh
// store with no shrinker, then with shrinker, and checks the second
// write's outcome is one of the two [StoredPayload] allows and the one
// wantLands names.
func assertShrinkOutcome(t *testing.T, contract documentfacets.Contract, sent documentfacets.Payload, shrinker *documentfacets.Shrinker, wantLands bool) {
	t.Helper()
	ctx := context.Background()
	store, dir := newMutationStore(t)
	args := FacetedWriteArgs{Ref: "kb:status.md", Contract: contract, Payload: sent, WriteTool: DocumentWriteToolName}
	result, off := NewTools(store, nil).WriteFaceted(ctx, args)
	if off == nil {
		t.Fatalf("unconfigured write landed (%s), want an overrun refusal", result)
	}
	result, err := NewTools(store, shrinker).WriteFaceted(ctx, args)
	path := filepath.Join(dir, "status.md")
	if err != nil {
		if err.Error() != off.Error() {
			t.Fatalf("refusal = %q\nwant exactly the unconfigured refusal %q", err, off)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("refused write left a document (stat err %v)", statErr)
		}
		if wantLands {
			t.Fatal("the write returned the original refusal, want it to land shortened")
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
		t.Fatalf("read the landed document: %v", err)
	}
	_, body := splitFrontmatter(string(raw))
	stored := contract.Parse(body)
	shortened := map[string]bool{}
	for _, entry := range decoded.Shortened {
		shortened[entry.Projection] = true
		if got, _ := stored.ByKey(entry.Projection); got != entry.SavedText || entry.Saved != lengthIn(entry.Limit, got) {
			t.Errorf("stored %s = %q, but the result reports saved_text %q (%s)", entry.Projection, got, entry.SavedText, entry.Saved)
		}
	}
	for _, field := range contract.Fields() {
		sentValue, _ := sent.ByKey(field.Key)
		storedValue, _ := stored.ByKey(field.Key)
		switch over := overBudget(field, sentValue); {
		case over != shortened[field.Key]:
			t.Errorf("%s over its budget = %v, reported shortened = %v", field.Key, over, shortened[field.Key])
		case !over && storedValue != normalizeGlyphArtifacts(strings.TrimSpace(sentValue)):
			t.Errorf("stored %s = %q, want the author's own text %q", field.Key, storedValue, sentValue)
		}
	}
	if !wantLands {
		t.Errorf("the write landed with %+v, want the original refusal", decoded.Shortened)
	}
}

// TestUnshortenedDocWriteMeetsTheStoreAsBefore pins that only shortened
// text is judged in its stored form before the write: an author's own text
// the store refuses once folded still gets the store's own refusal, with
// or without a shrinker configured.
func TestUnshortenedDocWriteMeetsTheStoreAsBefore(t *testing.T) {
	t.Parallel()

	for _, shrinker := range []*documentfacets.Shrinker{nil, keyShrinker(nil)} {
		store, _ := newMutationStore(t)
		_, err := NewTools(store, shrinker).WriteFaceted(context.Background(), FacetedWriteArgs{
			Ref: "kb:status.md", Contract: documentfacets.DefaultContract(), WriteTool: DocumentWriteToolName,
			Payload: documentfacets.Payload{StatusLine: "Current state.", Teaser: "Open for the hook.", Digest: "Opening.\n\n##\u00a0Details\n\nClosing.", Full: "Complete detail."},
		})
		if err == nil || !strings.HasPrefix(err.Error(), "validate faceted document kb:status.md: ") {
			t.Errorf("shrinker %v: WriteFaceted() error = %v, want the store's own refusal of the folded body", shrinker != nil, err)
		}
	}
}
