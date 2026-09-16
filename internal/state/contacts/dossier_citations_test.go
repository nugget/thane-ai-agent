package contacts

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

const (
	// citeUniqueID is the only archived session its first 8 and first
	// 12 hex digits begin.
	citeUniqueID = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	// citeSharedPrefix begins citeSharedCount sessions, the way one
	// import batch shares leading id digits.
	citeSharedPrefix = "01a1bbbb"
	citeSharedCount  = 7
	// citeAbsentID is canonical and names no archived session.
	citeAbsentID = "01a2cccc-dddd-7eee-8fff-000000000001"
)

// fakeArchiveSessions resolves leading parts against a fixed archive,
// bounded at five matches like the archive's own lookup, and records
// every prefix it was asked about.
type fakeArchiveSessions struct {
	sessions []ArchiveSessionMatch
	calls    []string
}

func newFakeArchiveSessions() *fakeArchiveSessions {
	f := &fakeArchiveSessions{sessions: []ArchiveSessionMatch{{
		ID:        citeUniqueID,
		StartedAt: time.Date(2025, 1, 10, 9, 0, 0, 0, time.UTC),
		Title:     "Alice plans the garden",
	}}}
	batch := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	for i := range citeSharedCount {
		f.sessions = append(f.sessions, ArchiveSessionMatch{
			ID:        fmt.Sprintf("%s-0000-7000-8000-%012d", citeSharedPrefix, i),
			StartedAt: batch.Add(time.Duration(i) * time.Hour),
			Title:     fmt.Sprintf("Bob import %d", i),
		})
	}
	return f
}

func (f *fakeArchiveSessions) resolve(ctx context.Context, prefix string) (ArchiveSessionLookup, error) {
	if err := ctx.Err(); err != nil {
		return ArchiveSessionLookup{}, err
	}
	f.calls = append(f.calls, prefix)
	var lookup ArchiveSessionLookup
	for _, session := range f.sessions {
		if !strings.HasPrefix(session.ID, prefix) {
			continue
		}
		lookup.Total++
		if len(lookup.Matches) < 5 {
			lookup.Matches = append(lookup.Matches, session)
		}
	}
	return lookup, nil
}

// TestWriteDossierResolvesArchiveSessionCitations pins what
// contact_dossier_write does with each citation shape: a whole id in the
// older spelling is respelled and reported, a leading part is refused
// with what the archive knows about it, and a canonical id is written
// without asking the archive whether it still exists.
func TestWriteDossierResolvesArchiveSessionCitations(t *testing.T) {
	// The age is measured from the clock the write runs on, so the
	// citation is asserted as the two halves either side of it;
	// TestDescribeSessionLookupRendersAges pins the delta itself.
	uniqueCitationHead := fmt.Sprintf("Exactly one archived session begins with it: cite archive:session:%s (age -", citeUniqueID)
	uniqueCitationTail := fmt.Sprintf(", time_basis session_started, title %q)", "Alice plans the garden")

	tests := []struct {
		name       string
		digest     string
		full       string
		noResolver bool
		// wantWrite: the dossier lands. Otherwise the call is refused.
		wantWrite         bool
		want              []string
		absent            []string
		wantStoredFull    string
		wantCanonicalized []canonicalizedCitation
		wantRejected      []string
		wantLookups       []string
	}{
		{
			name:           "hyphen-form full id is canonicalized and reported",
			full:           "Garden plans. — evidence: archive:session-" + citeUniqueID,
			wantWrite:      true,
			wantStoredFull: "Garden plans. — evidence: archive:session:" + citeUniqueID,
			wantCanonicalized: []canonicalizedCitation{{
				From:   "archive:session-" + citeUniqueID,
				To:     "archive:session:" + citeUniqueID,
				Fields: []string{"full"},
			}},
		},
		{
			name: "unique prefix names the full citation",
			full: "Garden plans. — evidence: archive:session:0190aaaa",
			want: []string{
				"archive:session:0190aaaa in field full carries only the first 8 of a session id's 32 hex digits",
				uniqueCitationHead,
				uniqueCitationTail,
			},
			wantRejected: []string{"full"},
			wantLookups:  []string{"0190aaaa"},
		},
		{
			name: "shared prefix lists the candidates",
			full: "Imported claim. — evidence: archive:session-" + citeSharedPrefix,
			want: []string{
				"archive:session-01a1bbbb in field full carries only the first 8",
				"7 archived sessions begin with it (ids minted close together share leading digits, and an import mints a whole batch that way)",
				`{"session_id":"01a1bbbb-0000-7000-8000-000000000000","age":"-`,
				`","time_basis":"session_started","title":"Bob import 0"}`,
				"(2 more not listed)",
				"Search archive_search for the claim's own words and cite the hit whose session_id begins with 01a1bbbb, listed here or not",
			},
			// The collision is stated as a fact, never blamed on an import:
			// native sessions minted within one UUIDv7 window collide too.
			// "among them" would rule out the unlisted candidates.
			absent:       []string{"import-time prefix", "among them"},
			wantRejected: []string{"full"},
			wantLookups:  []string{citeSharedPrefix},
		},
		{
			name:         "unknown prefix says no session has it",
			full:         "Claim. — evidence: archive:session:01ffffff",
			want:         []string{"archive:session:01ffffff in field full", "No archived session has an id that begins with it"},
			absent:       []string{"Exactly one", "Candidates"},
			wantRejected: []string{"full"},
			wantLookups:  []string{"01ffffff"},
		},
		{
			name:   "every problem in one write is reported at once",
			digest: "Context. — evidence: archive:session:0190aaaa and archive:session:" + citeSharedPrefix,
			full: "Claims. — evidence: archive:session-" + citeUniqueID + ", archive:session:" + citeSharedPrefix +
				", archive:session:01ffffff, archive:session:0190aaaa-bbbb, archive:session:zz01",
			want: []string{
				"archive:session:0190aaaa in field digest carries only the first 8",
				"archive:session:01a1bbbb in fields digest and full carries only the first 8",
				"archive:session:01ffffff in field full",
				"No archived session has an id that begins with it",
				"archive:session:0190aaaa-bbbb in field full carries only the first 12",
				"archive:session:zz01 in field full is not a session id: 'z' is not a hex digit",
				uniqueCitationHead,
				uniqueCitationTail,
				"### Open Questions",
			},
			// The whole id in the older spelling is respelled before
			// validation, so it is not one of the problems.
			absent:       []string{"archive:session-" + citeUniqueID},
			wantRejected: []string{"digest", "full"},
			wantLookups:  []string{"0190aaaa", citeSharedPrefix, "01ffffff", "0190aaaa-bbbb"},
		},
		{
			name:       "without a resolver a prefix is refused with the recovery",
			full:       "Garden plans. — evidence: archive:session:0190aaaa",
			noResolver: true,
			want: []string{
				"archive:session:0190aaaa in field full carries only the first 8",
				"search archive_search for the claim's own words: every hit carries its full session_id",
				"belongs under ### Open Questions in full, not in prose that describes a prefix",
			},
			absent:       []string{"Exactly one", "full="},
			wantRejected: []string{"full"},
		},
		{
			name:           "canonical id that names no archived session still writes",
			full:           "Claim. — evidence: archive:session:" + citeAbsentID,
			wantWrite:      true,
			wantStoredFull: "Claim. — evidence: archive:session:" + citeAbsentID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, contactID := newCitationTestTools(t)
			writer := &recordingDossierWriter{}
			tools.ConfigureDossierDocuments(nil, writer.Write)
			archive := newFakeArchiveSessions()
			if !tt.noResolver {
				tools.ConfigureDossierArchiveSessions(archive.resolve)
			}
			digest := tt.digest
			if digest == "" {
				digest = "Enough context to act."
			}

			result, err := tools.WriteDossier(context.Background(), DossierWriteArgs{
				ContactID:  contactID,
				StatusLine: "Current.",
				Teaser:     "Useful hook.",
				Digest:     digest,
				Full:       tt.full,
			})

			if !reflect.DeepEqual(archive.calls, tt.wantLookups) {
				t.Errorf("archive lookups = %q, want %q", archive.calls, tt.wantLookups)
			}
			if tt.wantWrite {
				assertCitationWriteLanded(t, result, err, writer, tt.wantStoredFull, tt.wantCanonicalized)
				return
			}
			if err == nil {
				t.Fatalf("WriteDossier() = %s, want a refusal", result)
			}
			if writer.calls != 0 {
				t.Errorf("refused dossier reached the writer %d times", writer.calls)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal lacks %q:\n%v", want, err)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("refusal should not contain %q:\n%v", absent, err)
				}
			}
			if got := toolargs.RejectedArguments(err); !reflect.DeepEqual(got, tt.wantRejected) {
				t.Errorf("rejected arguments = %q, want %q", got, tt.wantRejected)
			}
		})
	}
}

func newCitationTestTools(t *testing.T) (*Tools, string) {
	t.Helper()
	tools := newTestTools(t)
	if _, err := tools.SaveContact(`{"name":"Dossier Person","kind":"individual"}`); err != nil {
		t.Fatal(err)
	}
	contact, err := tools.store.FindByName("Dossier Person")
	if err != nil {
		t.Fatal(err)
	}
	tools.ConfigureDossierRoot(true, true)
	return tools, contact.ID.String()
}

func assertCitationWriteLanded(t *testing.T, result string, err error, writer *recordingDossierWriter, wantFull string, wantCanonicalized []canonicalizedCitation) {
	t.Helper()
	if err != nil {
		t.Fatalf("WriteDossier() error = %v, want the write to land", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
	if got := writer.args.Payload.Full; got != wantFull {
		t.Errorf("stored full = %q, want %q", got, wantFull)
	}
	var decoded struct {
		Action                 string                  `json:"action"`
		Applied                bool                    `json:"applied"`
		CanonicalizedCitations []canonicalizedCitation `json:"canonicalized_citations"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("result %q is not JSON: %v", result, err)
	}
	if !decoded.Applied || decoded.Action != "doc_write" {
		t.Errorf("result %s lost the writer's own fields", result)
	}
	if !reflect.DeepEqual(decoded.CanonicalizedCitations, wantCanonicalized) {
		t.Errorf("canonicalized_citations = %+v, want %+v", decoded.CanonicalizedCitations, wantCanonicalized)
	}
}

// TestWriteDossierBoundsCitationLookups pins the cap on archive lookups
// in one refusal: every leading part is still reported, and the ones
// past the cap say they were not looked up.
func TestWriteDossierBoundsCitationLookups(t *testing.T) {
	tools, contactID := newCitationTestTools(t)
	writer := &recordingDossierWriter{}
	tools.ConfigureDossierDocuments(nil, writer.Write)
	archive := newFakeArchiveSessions()
	tools.ConfigureDossierArchiveSessions(archive.resolve)

	citations := make([]string, 0, MaxDossierCitationLookups+2)
	for i := range MaxDossierCitationLookups + 2 {
		citations = append(citations, fmt.Sprintf("archive:session:0190ab%02x", i))
	}
	_, err := tools.WriteDossier(context.Background(), DossierWriteArgs{
		ContactID:  contactID,
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Claims. — evidence: " + strings.Join(citations, ", "),
	})
	if err == nil {
		t.Fatal("WriteDossier() accepted leading parts")
	}
	if len(archive.calls) != MaxDossierCitationLookups {
		t.Errorf("archive lookups = %d, want %d", len(archive.calls), MaxDossierCitationLookups)
	}
	for _, citation := range citations {
		if !strings.Contains(err.Error(), citation+" in field full") {
			t.Errorf("refusal does not report %s:\n%v", citation, err)
		}
	}
	if got := strings.Count(err.Error(), "Not looked up"); got != 2 {
		t.Errorf("refusal marks %d citations as not looked up, want 2:\n%v", got, err)
	}
	// A citation past the cap must read as work for the next call, not as
	// a field to abandon: the frame refuses a listed field resent
	// unchanged, so the refusal says leaving this one is expected and
	// names the lookup that answers it now.
	for _, want := range []string{
		fmt.Sprintf("Not looked up in this refusal, which looks up at most %d leading parts", MaxDossierCitationLookups),
		"Leaving this one as written in your next call is expected: once the citations looked up here are fixed, that call looks it up",
		`call archive_session_transcript with session_id "0190ab0a"`,
		`call archive_session_transcript with session_id "0190ab0b"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
}

func TestClassifySessionCitation(t *testing.T) {
	tests := []struct {
		text      string
		wantKind  citationKind
		wantID    string
		wantMatch string
	}{
		{text: "archive:session:" + citeUniqueID, wantKind: citationCanonical, wantID: citeUniqueID},
		{text: "archive:session-" + citeUniqueID, wantKind: citationRespelled, wantID: citeUniqueID},
		{text: "archive:session:" + strings.ToUpper(citeUniqueID), wantKind: citationRespelled, wantID: citeUniqueID},
		{text: "archive:session:" + strings.ReplaceAll(citeUniqueID, "-", ""), wantKind: citationRespelled, wantID: citeUniqueID},
		{text: "archive:session:0190aaaa", wantKind: citationLeadingPart, wantID: "0190aaaa"},
		{text: "archive:session-0190aaaabbbb", wantKind: citationLeadingPart, wantID: "0190aaaa-bbbb"},
		{text: "archive:session:" + citeUniqueID + "-- next clause", wantKind: citationCanonical, wantID: citeUniqueID, wantMatch: "archive:session:" + citeUniqueID},
		{text: "archive:session: prose", wantKind: citationMalformed, wantMatch: "archive:session:"},
		{text: "archive:session:00000000-0000-0000-0000-000000000000", wantKind: citationMalformed},
		{text: "archive:session:" + citeUniqueID + "0", wantKind: citationMalformed},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			match := archiveSessionCitationPattern.FindStringSubmatch(tt.text)
			if match == nil {
				t.Fatalf("pattern does not match %q", tt.text)
			}
			wantMatch := tt.wantMatch
			if wantMatch == "" {
				wantMatch = tt.text
			}
			if match[0] != wantMatch {
				t.Errorf("match = %q, want %q", match[0], wantMatch)
			}
			got := classifySessionCitation(match)
			if got.kind != tt.wantKind || (tt.wantID != "" && got.id != tt.wantID) {
				t.Errorf("classify = kind %d id %q, want kind %d id %q", got.kind, got.id, tt.wantKind, tt.wantID)
			}
		})
	}
}

func TestWithCanonicalizedCitations(t *testing.T) {
	changes := []canonicalizedCitation{{From: "archive:session-" + citeUniqueID, To: "archive:session:" + citeUniqueID, Fields: []string{"full"}}}
	tests := []struct {
		name   string
		result string
		want   string
	}{
		{name: "no changes leaves the result alone", result: `{"applied":true}`, want: `{"applied":true}`},
		{name: "object keeps its keys in order", result: `{"action":"contact_dossier_write","applied":true}` + "\n",
			want: `{"action":"contact_dossier_write","applied":true,"canonicalized_citations":[{"from":"archive:session-` + citeUniqueID + `","to":"archive:session:` + citeUniqueID + `","fields":["full"]}]}`},
		{name: "empty object", result: `{}`,
			want: `{"canonicalized_citations":[{"from":"archive:session-` + citeUniqueID + `","to":"archive:session:` + citeUniqueID + `","fields":["full"]}]}`},
		{name: "non-object result is wrapped", result: `written`,
			want: `{"write_result":"written","canonicalized_citations":[{"from":"archive:session-` + citeUniqueID + `","to":"archive:session:` + citeUniqueID + `","fields":["full"]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := changes
			if strings.HasPrefix(tt.name, "no changes") {
				input = nil
			}
			got, err := withCanonicalizedCitations(tt.result, input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("withCanonicalizedCitations() = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestValidateDossierEvidenceCitationsHonoursContext pins that the
// caller's context reaches the archive resolver. Without it a cancelled
// contact_dossier_write would still scan the archive once per refused
// leading part.
func TestValidateDossierEvidenceCitationsHonoursContext(t *testing.T) {
	archive := newFakeArchiveSessions()
	payload := documentfacets.Payload{
		StatusLine: "Current.",
		Teaser:     "Useful hook.",
		Digest:     "Enough context to act.",
		Full:       "Garden plans. — evidence: archive:session:0190aaaa",
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	err := validateDossierEvidenceCitations(cancelled, payload, archive.resolve)
	if err == nil {
		t.Fatal("validateDossierEvidenceCitations accepted a leading part")
	}
	if !strings.Contains(err.Error(), "Looking 0190aaaa up in the archive failed: context canceled") {
		t.Errorf("refusal does not report the cancelled lookup:\n%v", err)
	}
	// Negative control: under a live context the same citation resolves
	// to its one session, so the case above fails on cancellation.
	live := validateDossierEvidenceCitations(t.Context(), payload, archive.resolve)
	if live == nil || !strings.Contains(live.Error(), "Exactly one archived session begins with it") {
		t.Errorf("live refusal lost the resolved candidate:\n%v", live)
	}
}

// absoluteTimestampPattern matches an RFC3339 instant, the shape a
// model-facing refusal must not carry for a past event.
var absoluteTimestampPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

// TestDescribeSessionLookupRendersAges pins what a refused leading part
// says about when its candidates ran: an exact-second delta and the
// field the delta is measured from, all against the one now the refusal
// captured. An absolute started_at would leave the model subtracting
// timestamps to tell apart sessions an import minted an hour apart.
func TestDescribeSessionLookupRendersAges(t *testing.T) {
	batch := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	now := batch.Add(50 * time.Hour)
	shared := func(i int) string { return fmt.Sprintf("%s-0000-7000-8000-%012d", citeSharedPrefix, i) }

	tests := []struct {
		name   string
		lookup ArchiveSessionLookup
		want   []string
	}{
		{
			name: "one match names the citation with its age",
			lookup: ArchiveSessionLookup{Total: 1, Matches: []ArchiveSessionMatch{
				{ID: citeUniqueID, StartedAt: batch, Title: "Alice plans the garden"},
			}},
			want: []string{fmt.Sprintf(`cite archive:session:%s (age -2d2h, time_basis session_started, title "Alice plans the garden")`, citeUniqueID)},
		},
		{
			name: "candidates carry an age and the basis it is measured from",
			lookup: ArchiveSessionLookup{Total: 2, Matches: []ArchiveSessionMatch{
				{ID: shared(0), StartedAt: batch, Title: "Bob import 0"},
				{ID: shared(1), StartedAt: now.Add(-90 * time.Second), Title: "Bob import 1"},
			}},
			want: []string{
				`{"session_id":"` + shared(0) + `","age":"-2d2h","time_basis":"session_started","title":"Bob import 0"}`,
				`{"session_id":"` + shared(1) + `","age":"-90s","time_basis":"session_started","title":"Bob import 1"}`,
			},
		},
		{
			// A resolver that cannot say when a session started must not
			// have that read as a session two thousand years old.
			name: "a missing start time reads as unknown, not an age since the zero instant",
			lookup: ArchiveSessionLookup{Total: 1, Matches: []ArchiveSessionMatch{
				{ID: citeUniqueID, Title: "Alice plans the garden"},
			}},
			want: []string{"(age unknown, time_basis session_started,"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeSessionLookup(citeSharedPrefix, tt.lookup, now, nil)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("description lacks %s:\n%s", want, got)
				}
			}
			if stamp := absoluteTimestampPattern.FindString(got); stamp != "" {
				t.Errorf("description carries the absolute timestamp %q:\n%s", stamp, got)
			}
		})
	}
}
