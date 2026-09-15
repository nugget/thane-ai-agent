package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// archiveSessionContentRecovery is the next move when an id cannot pick
// out one session. Content search is the only lookup that can: every
// archive_search hit names its session by full id.
const archiveSessionContentRecovery = "search archive_search for words from the conversation itself; every hit carries the full session_id"

// archiveSessionCitationPrefixes are the citation spellings a session id
// may arrive wrapped in when the model copies it out of a document. The
// colon form is canonical; the hyphen form is the older spelling.
var archiveSessionCitationPrefixes = []string{"archive:session:", "archive:session-"}

// sessionCandidateView is one entry in the candidate list an ambiguous
// session_id prefix returns. The key is session_id so the chosen entry
// can be copied straight into the next call.
type sessionCandidateView struct {
	SessionID string `json:"session_id"`
	StartedAt string `json:"started_at"`
	Title     string `json:"title"`
}

// resolveTranscriptSessionID turns archive_session_transcript's
// session_id argument into exactly one full session id. A full id is
// returned as is; a leading part is resolved against every archived
// session, and a part that matches none or several is refused with an
// error that teaches the next call.
func resolveTranscriptSessionID(store *memory.ArchiveStore, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	for _, citation := range archiveSessionCitationPrefixes {
		trimmed = strings.TrimPrefix(trimmed, citation)
	}
	if trimmed == "" {
		return "", fmt.Errorf("session_id is required")
	}

	id, err := memory.NormalizeSessionIDPrefix(trimmed)
	if err != nil {
		return "", fmt.Errorf("session_id: %w; pass a full session id (32 hex digits in 8-4-4-4-12 form) or a leading part of one", err)
	}
	if memory.IsFullSessionID(id) {
		return id, nil
	}

	lookup, err := store.ResolveSessionPrefix(id, memory.DefaultSessionPrefixCandidates)
	if err != nil {
		return "", fmt.Errorf("resolve session_id %q: %w", id, err)
	}
	switch len(lookup.Matches) {
	case 0:
		return "", fmt.Errorf("no archived session has an id beginning %q; to find the session, %s", id, archiveSessionContentRecovery)
	case 1:
		return lookup.Matches[0].ID, nil
	default:
		return "", ambiguousSessionPrefixError(lookup)
	}
}

// ambiguousSessionPrefixError lists the sessions a shared prefix
// matches, bounded by the lookup's limit, and says how to choose among
// them.
func ambiguousSessionPrefixError(lookup memory.SessionPrefixLookup) error {
	candidates := make([]sessionCandidateView, 0, len(lookup.Matches))
	for _, match := range lookup.Matches {
		candidates = append(candidates, sessionCandidateView{
			SessionID: match.ID,
			StartedAt: match.StartedAt.UTC().Format(time.RFC3339),
			Title:     match.Title,
		})
	}
	listed, err := json.Marshal(candidates)
	if err != nil {
		return fmt.Errorf("session_id %q matches %d archived sessions; listing them failed: %w", lookup.Prefix, lookup.Total, err)
	}

	unlisted, unlistedRecovery := "", ""
	if n := lookup.Unlisted(); n > 0 {
		unlisted = fmt.Sprintf(" (%d more not listed)", n)
		unlistedRecovery = fmt.Sprintf(" The session you mean may be one of the %d not listed: keep the archive_search hit whose session_id begins with %q, listed here or not.", n, lookup.Prefix)
	}
	return fmt.Errorf(
		"session_id %q matches %d archived sessions, so no transcript was read. Candidates in id order: %s%s. "+
			"Retry with the full session_id of the one you mean. Ids minted close together share leading digits, "+
			"and an import mints a whole batch that way, recording when the import ran rather than when each conversation happened; "+
			"when the titles and start times do not decide it, %s.%s",
		lookup.Prefix, lookup.Total, listed, unlisted, archiveSessionContentRecovery, unlistedRecovery)
}

// missingArchiveSessionError distinguishes a full session id that names
// no archived session from a session that exists but has no messages.
// It returns nil when the session exists.
func missingArchiveSessionError(store *memory.ArchiveStore, id string) error {
	sess, err := store.GetSession(id)
	if err != nil {
		return fmt.Errorf("look up session %s: %w", id, err)
	}
	if sess != nil {
		return nil
	}
	return fmt.Errorf("no archived session has id %q; if the id came from an older note it may predate a re-import, which gives imported sessions new ids. To find the session, %s", id, archiveSessionContentRecovery)
}
