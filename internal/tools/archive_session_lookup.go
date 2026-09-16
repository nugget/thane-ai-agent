package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
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

// sessionCandidateTimeBasis names the instant each candidate's age is
// measured from, so a delta cannot be read against some other clock.
const sessionCandidateTimeBasis = "session_started"

// sessionCandidateTitleMaxBytes caps each candidate's title, matching
// what archive_search gives a session hit. A title is author-controlled
// and arbitrarily long, and this list exists to tell candidates apart,
// which their opening words already do.
const sessionCandidateTitleMaxBytes = 240

// sessionCandidateCutMarker ends a title cut to its bound. It is the
// marker the contact and document lists already use, so a cut reads the
// same wherever the model meets one.
const sessionCandidateCutMarker = "…[cut]"

// clipCandidateTitle bounds one title to sessionCandidateTitleMaxBytes
// including the marker, cutting on a rune boundary so a multi-byte
// character is never split (AGENTS.md).
func clipCandidateTitle(title string) string {
	if len(title) <= sessionCandidateTitleMaxBytes {
		return title
	}
	return truncateUTF8(title, sessionCandidateTitleMaxBytes-len(sessionCandidateCutMarker)) + sessionCandidateCutMarker
}

// sessionCandidateView is one entry in the candidate list an ambiguous
// session_id prefix returns. The key is session_id so the chosen entry
// can be copied straight into the next call. Age is an exact-second
// delta rather than the stored timestamp, because telling apart sessions
// an import minted minutes apart should not cost the model a subtraction
// (docs/model-facing-context.md); the absolute started_at stays in
// storage and logs.
type sessionCandidateView struct {
	SessionID string `json:"session_id"`
	Age       string `json:"age"`
	TimeBasis string `json:"time_basis"`
	Title     string `json:"title"`
}

// sessionCandidateViews renders the matches of one lookup against a
// single captured now, so two candidates of the same age read as the
// same age.
func sessionCandidateViews(matches []memory.SessionPrefixMatch, now time.Time) []sessionCandidateView {
	candidates := make([]sessionCandidateView, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, sessionCandidateView{
			SessionID: match.ID,
			Age:       promptfmt.FormatDeltaOnly(match.StartedAt, now),
			TimeBasis: sessionCandidateTimeBasis,
			Title:     clipCandidateTitle(match.Title),
		})
	}
	return candidates
}

// resolveTranscriptSessionID turns archive_session_transcript's
// session_id argument into exactly one full session id. A full id is
// returned as is; a leading part is resolved against every archived
// session, and a part that matches none or several is refused with an
// error that teaches the next call. The archive lookup runs under ctx,
// so a cancelled tool call stops it.
func resolveTranscriptSessionID(ctx context.Context, store *memory.ArchiveStore, raw string) (string, error) {
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

	lookup, err := store.ResolveSessionPrefix(ctx, id, memory.DefaultSessionPrefixCandidates)
	if err != nil {
		return "", fmt.Errorf("resolve session_id %q: %w", id, err)
	}
	switch len(lookup.Matches) {
	case 0:
		return "", fmt.Errorf("no archived session has an id beginning %q; to find the session, %s", id, archiveSessionContentRecovery)
	case 1:
		return lookup.Matches[0].ID, nil
	default:
		// One now for the whole refusal, so the candidate ages are
		// comparable to each other and not to when each was rendered.
		return "", ambiguousSessionPrefixError(lookup, time.Now())
	}
}

// ambiguousSessionPrefixError lists the sessions a shared prefix
// matches, bounded by the lookup's limit, and says how to choose among
// them. Every candidate age is measured from now.
//
// This refusal stands in place of the transcript the call asked for, so
// it answers to the same ceiling: clipped titles keep it small in the
// ordinary case, and a byte fit drops candidates from the tail if
// anything — JSON escaping of a hostile title, a lookup limit raised
// past the default — still pushes it past archiveTranscriptByteCap. The
// recovery that closes the refusal is never what gets dropped.
func ambiguousSessionPrefixError(lookup memory.SessionPrefixLookup, now time.Time) error {
	candidates := sessionCandidateViews(lookup.Matches, now)
	if _, err := json.Marshal(candidates); err != nil {
		return fmt.Errorf("session_id %q matches %d archived sessions; listing them failed: %w", lookup.Prefix, lookup.Total, err)
	}
	return errors.New(string(memory.FitPrefix(len(candidates), archiveTranscriptByteCap, func(k int) []byte {
		return []byte(sessionPrefixRefusal(lookup, candidates[:k]))
	})))
}

// sessionPrefixRefusal renders the refusal around the candidates it is
// given. The unlisted count covers every match the list leaves out,
// whether the lookup's limit or the byte cap dropped it, so a shortened
// list still says how much of the archive it is not showing.
func sessionPrefixRefusal(lookup memory.SessionPrefixLookup, candidates []sessionCandidateView) string {
	// Marshalling a prefix of a slice the caller has already marshalled
	// cannot fail, so there is no second error to report here.
	listed, _ := json.Marshal(candidates)

	unlisted, unlistedRecovery := "", ""
	if n := lookup.Total - len(candidates); n > 0 {
		unlisted = fmt.Sprintf(" (%d more not listed)", n)
		unlistedRecovery = fmt.Sprintf(" The session you mean may be one of the %d not listed: keep the archive_search hit whose session_id begins with %q, listed here or not.", n, lookup.Prefix)
	}
	return fmt.Sprintf(
		"session_id %q matches %d archived sessions, so no transcript was read. Candidates in id order: %s%s. "+
			"Retry with the full session_id of the one you mean. Ids minted close together share leading digits, "+
			"and an import mints a whole batch that way, recording when the import ran rather than when each conversation happened; "+
			"when the titles and ages do not decide it, %s.%s",
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
