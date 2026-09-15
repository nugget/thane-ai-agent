package contacts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MaxDossierCitationLookups bounds how many distinct leading parts one
// refusal resolves, so a dossier carrying dozens of inherited prefixes
// still gets a refusal that fits in one tool result.
const MaxDossierCitationLookups = 10

// ArchiveSessionMatch is one archived session whose id begins with a
// leading part cited in a dossier.
type ArchiveSessionMatch struct {
	ID        string
	StartedAt time.Time
	Title     string
}

// ArchiveSessionLookup is the bounded answer of an
// [ArchiveSessionResolver]: a few matches in id order, and Total counting
// every archived session that matched, listed or not.
type ArchiveSessionLookup struct {
	Matches []ArchiveSessionMatch
	Total   int
}

// ArchiveSessionResolver finds the archived sessions whose ids begin with
// prefix, a leading part of a session id in canonical 8-4-4-4-12 form
// such as "019c52f0" or "019c52f0-9ce8". It searches the whole archive,
// and zero matches is an empty lookup, not an error.
type ArchiveSessionResolver func(prefix string) (ArchiveSessionLookup, error)

// ConfigureDossierArchiveSessions installs the archive lookup
// [Tools.WriteDossier] uses when it refuses a leading part of a session
// id: one match is named as the full citation to copy, several are listed
// as candidates. Without a resolver the leading part is still refused,
// with the content-search recovery and no candidates.
func (t *Tools) ConfigureDossierArchiveSessions(resolve ArchiveSessionResolver) {
	if t == nil {
		return
	}
	t.dossierSessions = resolve
}

// citationResolver looks up the leading parts of one refusal, once each
// and at most [MaxDossierCitationLookups] of them.
type citationResolver struct {
	resolve ArchiveSessionResolver
	seen    map[string]string
}

func newCitationResolver(resolve ArchiveSessionResolver) *citationResolver {
	return &citationResolver{resolve: resolve, seen: make(map[string]string)}
}

// describe returns what the archive says about prefix, as a sentence to
// append to the citation's refusal, or "" without a resolver.
func (r *citationResolver) describe(prefix string) string {
	if r == nil || r.resolve == nil {
		return ""
	}
	if described, ok := r.seen[prefix]; ok {
		return described
	}
	if len(r.seen) >= MaxDossierCitationLookups {
		return fmt.Sprintf(". Not looked up in this refusal, which looks up at most %d leading parts. "+
			"Leaving this one as written in your next call is expected: once the citations looked up here are fixed, that call looks it up. "+
			"To look it up now instead, call archive_session_transcript with session_id %q",
			MaxDossierCitationLookups, prefix)
	}
	lookup, err := r.resolve(prefix)
	described := describeSessionLookup(prefix, lookup, err)
	r.seen[prefix] = described
	return described
}

// sessionCandidateView is one candidate a shared leading part lists. The
// key is session_id, the name archive tool results use for the same id.
type sessionCandidateView struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
	Title     string `json:"title"`
}

// describeSessionLookup renders one resolver answer: the full citation
// when exactly one session matches, the bounded candidates when several
// do, and a plain statement when none does.
func describeSessionLookup(prefix string, lookup ArchiveSessionLookup, err error) string {
	if err != nil {
		return fmt.Sprintf(". Looking %s up in the archive failed: %v", prefix, err)
	}
	total := max(lookup.Total, len(lookup.Matches))
	switch {
	case total == 0:
		return ". No archived session has an id that begins with it"
	case total == 1:
		match := lookup.Matches[0]
		return fmt.Sprintf(". Exactly one archived session begins with it: cite %s%s (started_at %s, title %q)",
			archiveSessionCitationPrefix, match.ID, formatCandidateTime(match.StartedAt), match.Title)
	}

	candidates := make([]sessionCandidateView, 0, len(lookup.Matches))
	for _, match := range lookup.Matches {
		candidates = append(candidates, sessionCandidateView{
			SessionID: match.ID,
			StartedAt: formatCandidateTime(match.StartedAt),
			Title:     match.Title,
		})
	}
	listed, err := json.Marshal(candidates)
	if err != nil {
		return fmt.Sprintf(". %d archived sessions begin with it; listing them failed: %v", total, err)
	}
	unlisted := ""
	if n := total - len(candidates); n > 0 {
		unlisted = fmt.Sprintf(" (%d more not listed)", n)
	}
	return fmt.Sprintf(". %d archived sessions begin with it (ids minted close together share leading digits, and an import mints a whole batch that way), "+
		"so the prefix alone cannot say which one the claim meant. Candidates in id order: %s%s. "+
		"Search archive_search for the claim's own words and cite the hit whose session_id begins with %s, listed here or not",
		total, listed, unlisted, prefix)
}

func formatCandidateTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// withCanonicalizedCitations adds the citations Go respelled to a
// successful write result, so the caller learns the stored spelling
// without reading the dossier back. The field is spliced into the result
// object to keep its existing key order.
func withCanonicalizedCitations(result string, changes []canonicalizedCitation) (string, error) {
	if len(changes) == 0 {
		return result, nil
	}
	listed, err := json.Marshal(changes)
	if err != nil {
		return "", fmt.Errorf("the contact dossier was written, but encoding the citations Go canonicalized failed: %w", err)
	}
	trimmed := strings.TrimSpace(result)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") && json.Valid([]byte(trimmed)) {
		head := strings.TrimSpace(strings.TrimSuffix(trimmed, "}"))
		separator := ","
		if head == "{" {
			separator = ""
		}
		return head + separator + `"canonicalized_citations":` + string(listed) + "}", nil
	}
	wrapped, err := json.Marshal(struct {
		WriteResult            string                  `json:"write_result"`
		CanonicalizedCitations []canonicalizedCitation `json:"canonicalized_citations"`
	}{WriteResult: result, CanonicalizedCitations: changes})
	if err != nil {
		return "", fmt.Errorf("the contact dossier was written, but encoding its result failed: %w", err)
	}
	return string(wrapped), nil
}
