package documents

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// Freshness bands. A continuous freshness score would flip near-ties from
// turn to turn with no request change, churning selection order for
// nothing a reader can see; three coarse bands keep ordering stable while
// still letting a rotting document lose its seat.
const (
	advertiseFreshStrength = 0.55
	advertiseAgingStrength = 0.40
	advertiseStaleStrength = 0.25

	// advertiseLexicalStrength rides evidence tier "lexical", which
	// outranks every ambient offer regardless of strength — the value
	// only orders lexical matches among themselves.
	advertiseLexicalStrength = 0.60

	// advertiseEnvelopeOverheadBytes covers the one-line JSON envelope a
	// materialized fragment opens with, on top of the indexed facet
	// bytes. Estimates are commitments — the discriminator drops a
	// payload that overruns its own declaration — so the overhead is
	// budgeted generously.
	advertiseEnvelopeOverheadBytes = 320

	// Fallback staleness bounds for a document no loop maintains: a
	// hand-authored dossier is fresh for a day and aging for a week.
	advertiseDefaultFreshFor = 24 * time.Hour
	advertiseDefaultAgingFor = 7 * 24 * time.Hour
)

// DocumentRootAdvertisePolicy is the per-root answer the advertiser needs
// from configuration: whether this root's documents may offer themselves,
// and which capability tag gates them when the mode is "tagged". Values
// mirror config.RootAdvertise*; the string coupling is validated at wiring.
type DocumentRootAdvertisePolicy struct {
	Mode        string
	RequiresTag string
}

// DocumentAdvertiserConfig wires the advertiser's dependencies.
type DocumentAdvertiserConfig struct {
	Store *Store
	// RootPolicy resolves a root name to its advertise policy. Roots
	// with no policy (or mode "never") never offer.
	RootPolicy func(root string) DocumentRootAdvertisePolicy
	// SleepMax resolves the live sleep ceiling of the loop maintaining a
	// document, when one does. It must read the live definition
	// registry, not document frontmatter: publishes stamp no updated
	// field, and create-time sleep bounds rot when retunes promote
	// scalars into the running spec.
	SleepMax func(loopDefinitionName string) (time.Duration, bool)
	// HomeZone anchors deltas and temporal-template expansion; nil falls
	// back to the process-local zone.
	HomeZone *time.Location
	// Now is overridable for tests; nil means time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// DocumentAdvertiser offers faceted documents to the context-advertisement
// rail (#1431). It is the generic consumer of the corpus: any root that
// opts in through its advertise policy competes here, with the schedule
// root's curated calendar as the first production case.
//
// Advertising is index-only — one enumeration, no file reads, no store
// locks — because it runs inside the serial context-assembly walk. Only a
// selected offer pays for a file read, at materialization, under the
// discriminator's own detached budget.
type DocumentAdvertiser struct {
	cfg DocumentAdvertiserConfig

	// lastRows remembers the most recent enumeration by ref, so
	// materialization can find the file and freshness facts for a
	// selection without a second index query; lastNow is the instant that
	// enumeration was judged against, reused by every materialization it
	// feeds so all fragments of one turn share one clock — a prompt must
	// not say "today" in one fragment and "tomorrow" in the next because
	// the clock ticked between file reads. Advertise and materialize
	// happen on the same turn, but nothing forbids concurrent turns —
	// hence the lock.
	mu       sync.RWMutex
	lastRows map[string]AdvertisableDocument
	lastNow  time.Time
}

// NewDocumentAdvertiser builds the advertiser. It implements the
// assembler's TagContextProvider and ContextAdvertiser contracts
// structurally: TagContext renders nothing (this provider only offers),
// and the advertisement path carries all content.
func NewDocumentAdvertiser(cfg DocumentAdvertiserConfig) *DocumentAdvertiser {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HomeZone == nil {
		cfg.HomeZone = time.Local
	}
	return &DocumentAdvertiser{cfg: cfg, lastRows: make(map[string]AdvertisableDocument)}
}

// TagContext implements TagContextProvider with an empty eager render:
// every byte this provider contributes goes through the advertisement
// rail, where it competes instead of assuming a seat.
func (d *DocumentAdvertiser) TagContext(context.Context, agentctx.ContextRequest) (string, error) {
	return "", nil
}

// ContextAdvertisements enumerates the corpus and offers every eligible
// faceted document. Zero file I/O: everything an offer needs — facet
// presence, exact byte costs, tags, provenance, freshness — was captured
// at index time.
func (d *DocumentAdvertiser) ContextAdvertisements(ctx context.Context, req agentctx.ContextRequest) ([]agentctx.ContextAdvertisement, error) {
	if d.cfg.Store == nil || d.cfg.RootPolicy == nil {
		return nil, nil
	}
	rows, err := d.cfg.Store.AdvertisableDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("enumerate corpus for advertising: %w", err)
	}

	pinned := make(map[string]struct{}, len(req.PinnedRefs))
	for _, ref := range req.PinnedRefs {
		pinned[ref] = struct{}{}
	}
	subjects := knowledge.SubjectsFromContext(ctx)

	now := d.cfg.Now().In(d.cfg.HomeZone)
	remembered := make(map[string]AdvertisableDocument)
	var ads []agentctx.ContextAdvertisement
	for _, row := range rows {
		policy := d.cfg.RootPolicy(row.Root)
		switch policy.Mode {
		case "always":
		case "tagged":
			if policy.RequiresTag == "" || !req.ActiveTags[policy.RequiresTag] {
				continue
			}
		default:
			continue
		}
		if _, isPinned := pinned[row.Ref]; isPinned {
			// Session-origin context refs inject this document whole
			// already; offering it again would spend rail budget saying
			// something twice.
			continue
		}

		projections := d.projectionsFor(row)
		if len(projections) == 0 {
			// An unfaceted document has only detail to offer, and
			// automatic selection never takes detail. It stays reachable
			// through search; the rail is for documents that authored
			// their own compact projections.
			continue
		}

		matches := []agentctx.ContextMatchSignal{{
			Kind:     agentctx.ContextMatchAmbient,
			Strength: d.freshnessStrength(row, now),
		}}
		if m, ok := lexicalMatch(req.UserMessage, row); ok {
			matches = append(matches, m)
		}
		if m, ok := subjectMatch(subjects, row); ok {
			matches = append(matches, m)
		}

		remembered[row.Ref] = row
		ads = append(ads, agentctx.ContextAdvertisement{
			ID:          row.Ref,
			Source:      "documents",
			Kind:        "faceted_document",
			Ref:         row.Ref,
			Bucket:      agentctx.ContextBucketRelated,
			Summary:     advertisementSummary(row),
			Matches:     matches,
			Projections: projections,
		})
	}

	d.mu.Lock()
	for ref, row := range remembered {
		d.lastRows[ref] = row
	}
	d.lastNow = now
	d.mu.Unlock()
	return ads, nil
}

// MaterializeContextAdvertisement reads the selected document — directly
// from disk, off the store's locks, under the discriminator's detached
// budget — extracts the selected facet, expands temporal templates, and
// opens the fragment with a one-line JSON envelope.
//
// The envelope is what makes a fragment self-explaining: why it appeared
// (match kind), where it came from (ref, maintaining loop), how live it is
// (delta plus staleness band), and the door to more. It also marks the
// boundary doctrine demands — curator documents are loop-authored prose,
// and a reader should see them as quoted material with provenance, never
// as anonymous instructions.
func (d *DocumentAdvertiser) MaterializeContextAdvertisement(ctx context.Context, _ agentctx.ContextRequest, selection agentctx.ContextSelection) (string, error) {
	ref := selection.Advertisement.Ref
	d.mu.RLock()
	row, ok := d.lastRows[ref]
	now := d.lastNow
	d.mu.RUnlock()
	if now.IsZero() {
		now = d.cfg.Now().In(d.cfg.HomeZone)
	}
	if !ok {
		return "", fmt.Errorf("no remembered enumeration row for %s", ref)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	raw, err := os.ReadFile(row.AbsPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", ref, err)
	}
	payload, _ := looppkg.ParseFacetSections(string(raw))
	content := facetContent(payload, selection.Projection.Name)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("document %s no longer carries facet %s", ref, selection.Projection.Name)
	}

	content = promptfmt.ExpandTemporalTemplates(content, now)

	envelope := documentFragmentEnvelope{
		Ref:          ref,
		SourceLoop:   row.LoopDefinitionName,
		Match:        string(bestMatchKind(selection.Advertisement.Matches)),
		UpdatedDelta: promptfmt.FormatDeltaOnly(row.ModifiedAt, now),
		Stale:        d.freshnessBand(row, now),
		More:         fmt.Sprintf("doc_read(%s, level=full)", ref),
	}
	return promptfmt.MarshalCompact(envelope) + "\n" + content, nil
}

// documentFragmentEnvelope is the one-line JSON header a materialized
// fragment opens with.
type documentFragmentEnvelope struct {
	Ref          string `json:"ref"`
	SourceLoop   string `json:"source_loop,omitempty"`
	Match        string `json:"match"`
	UpdatedDelta string `json:"updated_delta"`
	Stale        string `json:"stale,omitempty"`
	More         string `json:"more"`
}

// projectionsFor offers each declared compact facet at its indexed byte
// cost plus envelope overhead, and the full body as detail. Roles come
// from the facet contract's own table, so the advertiser cannot drift
// from what the ladder means.
func (d *DocumentAdvertiser) projectionsFor(row AdvertisableDocument) []agentctx.ContextProjection {
	var out []agentctx.ContextProjection
	compact := false
	for facet, size := range row.FacetBytes {
		field, ok := looppkg.FacetFieldByKey(facet)
		if !ok || size <= 0 {
			continue
		}
		if field.ContextRole != agentctx.ContextRoleDetail {
			compact = true
		}
		out = append(out, agentctx.ContextProjection{
			Name:           facet,
			Role:           field.ContextRole,
			Format:         "text/markdown",
			EstimatedBytes: size + advertiseEnvelopeOverheadBytes,
		})
	}
	if !compact {
		return nil
	}
	return out
}

// freshnessBand places a document in one of three coarse bands against
// the sleep ceiling of the loop that maintains it — the live registry
// value, because frontmatter carries no updated stamp and create-time
// sleep bounds rot after retunes. A document no loop maintains uses the
// hand-authored defaults.
func (d *DocumentAdvertiser) freshnessBand(row AdvertisableDocument, now time.Time) string {
	freshFor := advertiseDefaultFreshFor
	agingFor := advertiseDefaultAgingFor
	if row.LoopDefinitionName != "" && d.cfg.SleepMax != nil {
		if sleepMax, ok := d.cfg.SleepMax(row.LoopDefinitionName); ok && sleepMax > 0 {
			// Fresh means "within one wake of true": the loop had no
			// chance to republish yet. Aging tolerates a few missed or
			// quiet wakes before the document reads as stale.
			freshFor = sleepMax + sleepMax/2
			agingFor = 4 * sleepMax
		}
	}
	age := now.Sub(row.ModifiedAt)
	switch {
	case age <= freshFor:
		return ""
	case age <= agingFor:
		return "aging"
	default:
		return "stale"
	}
}

func (d *DocumentAdvertiser) freshnessStrength(row AdvertisableDocument, now time.Time) float64 {
	switch d.freshnessBand(row, now) {
	case "":
		return advertiseFreshStrength
	case "aging":
		return advertiseAgingStrength
	default:
		return advertiseStaleStrength
	}
}

// lexicalMatch reports whether the user's message names this document —
// by a tag, or by a distinctive word of its title. Distinctive means four
// runes or longer: matching "the" to a title would promote every document
// on every turn, which is ambient relevance wearing a costume.
func lexicalMatch(userMessage string, row AdvertisableDocument) (agentctx.ContextMatchSignal, bool) {
	message := strings.ToLower(userMessage)
	if strings.TrimSpace(message) == "" {
		return agentctx.ContextMatchSignal{}, false
	}
	for _, tag := range row.Tags {
		if tag = strings.ToLower(strings.TrimSpace(tag)); len([]rune(tag)) >= 3 && strings.Contains(message, tag) {
			return agentctx.ContextMatchSignal{Kind: agentctx.ContextMatchLexical, Strength: advertiseLexicalStrength}, true
		}
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(row.Title), func(r rune) bool {
		return ('a' > r || r > 'z') && ('0' > r || r > '9') && r != '-'
	}) {
		if len([]rune(word)) >= 4 && strings.Contains(message, word) {
			return agentctx.ContextMatchSignal{Kind: agentctx.ContextMatchLexical, Strength: advertiseLexicalStrength}, true
		}
	}
	return agentctx.ContextMatchSignal{}, false
}

// subjectMatch reports whether one of the turn's subject handles (#986's
// cross-silo vocabulary, placed on ctx by wake bridges) exactly matches a
// document tag. This is the strongest evidence a document can carry: the
// turn is already about the thing the document is about.
func subjectMatch(subjects []string, row AdvertisableDocument) (agentctx.ContextMatchSignal, bool) {
	if len(subjects) == 0 {
		return agentctx.ContextMatchSignal{}, false
	}
	tags := make(map[string]struct{}, len(row.Tags))
	for _, tag := range row.Tags {
		tags[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
	}
	for _, subject := range subjects {
		if _, ok := tags[strings.ToLower(strings.TrimSpace(subject))]; ok {
			return agentctx.ContextMatchSignal{Kind: agentctx.ContextMatchExactSubject, Strength: 1}, true
		}
	}
	return agentctx.ContextMatchSignal{}, false
}

func advertisementSummary(row AdvertisableDocument) string {
	if s := strings.TrimSpace(row.Summary); s != "" {
		return s
	}
	if s := strings.TrimSpace(row.Title); s != "" {
		return s
	}
	return row.Ref
}

func bestMatchKind(matches []agentctx.ContextMatchSignal) agentctx.ContextMatchKind {
	best := agentctx.ContextMatchAmbient
	bestRank := 0
	rank := map[agentctx.ContextMatchKind]int{
		agentctx.ContextMatchAmbient: 1, agentctx.ContextMatchLexical: 2,
		agentctx.ContextMatchSemantic: 3, agentctx.ContextMatchAlias: 4,
		agentctx.ContextMatchExactSubject: 5,
	}
	for _, m := range matches {
		if r := rank[m.Kind]; r > bestRank {
			best, bestRank = m.Kind, r
		}
	}
	return best
}

// facetContent selects one projection's text from a parsed facet payload.
func facetContent(payload looppkg.FacetPayload, name string) string {
	switch name {
	case string(looppkg.OutputFacetStatusLine):
		return payload.StatusLine
	case string(looppkg.OutputFacetTeaser):
		return payload.Teaser
	case string(looppkg.OutputFacetDigest):
		return payload.Digest
	case "full":
		return payload.Full
	default:
		return ""
	}
}
