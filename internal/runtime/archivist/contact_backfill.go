package archivist

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

const (
	contactBackfillNamespace = "archivist:contact_dossier_backfill"
	contactBackfillStateKey  = "v1"
	contactBackfillVersion   = 1

	// Historical catch-up stays behind ordinary session-close work. The
	// archivist remains self-paced, and a large one-time import cannot starve
	// fresh evidence merely because it entered the queue first.
	contactBackfillPriority = -10

	// DefaultContactBackfillLimit is the source-record limit used when an
	// operator omits or supplies a non-positive batch size.
	DefaultContactBackfillLimit = 50
	// MaxContactBackfillLimit caps one operator request so historical work is
	// always admitted to the queue in bounded increments.
	MaxContactBackfillLimit = 200
)

const (
	backfillPhaseContacts = "contacts"
	backfillPhaseSessions = "sessions"
	backfillPhaseComplete = "complete"
)

// automationSessionOrigins are conversation-ID prefixes whose sessions carry
// no dossier-worthy evidence: autonomous service-loop iterations,
// scheduled-task runs, metacognitive bookkeeping, and auxiliary utility
// traffic. This is deliberately a denylist: any unrecognized origin remains
// archivable so a rare substantive source is not silently dropped.
var automationSessionOrigins = []string{
	"loop-",
	"sched-",
	"metacog-",
	"owu-auxiliary",
}

// ContactDossierBackfill advances the explicit one-time historical traversal
// one bounded page at a time. It never wakes the archivist; enqueued subjects
// are drained on the loop's normal self-paced cadence.
type ContactDossierBackfill struct {
	listContacts func(time.Time, string, int) ([]*contacts.Contact, bool, error)
	listSessions func(time.Time, string, int) ([]*memory.Session, bool, error)
	hasRecent    func(context.Context, string, string) (bool, error)
	enqueue      func(context.Context, string, string, int, []byte) error
	getState     func(string, string) (string, error)
	setState     func(string, string, string) error
	logger       *slog.Logger
	now          func() time.Time

	mu sync.Mutex
}

// ContactDossierBackfillResult describes one bounded advancement. Counts are
// for this call rather than lifetime totals; Complete is durable and later
// calls return a no-op result.
type ContactDossierBackfillResult struct {
	Phase             string    `json:"phase"`
	NextPhase         string    `json:"next_phase,omitempty"`
	Cutoff            time.Time `json:"cutoff"`
	Scanned           int       `json:"scanned"`
	Enqueued          int       `json:"enqueued"`
	SkippedRecent     int       `json:"skipped_recent"`
	SkippedEmpty      int       `json:"skipped_empty"`
	SkippedAutomation int       `json:"skipped_automation"`
	Complete          bool      `json:"complete"`
}

type contactDossierBackfillState struct {
	Version       int       `json:"version"`
	Cutoff        time.Time `json:"cutoff"`
	Phase         string    `json:"phase"`
	ContactCursor string    `json:"contact_cursor,omitempty"`
	SessionCursor string    `json:"session_cursor,omitempty"`
	Complete      bool      `json:"complete"`
}

// NewContactDossierBackfill constructs the one-time contact-dossier backfill.
func NewContactDossierBackfill(
	contactSource *contacts.Store,
	sessionSource *memory.ArchiveStore,
	queue *loopqueue.Store,
	state *opstate.Store,
	logger *slog.Logger,
) *ContactDossierBackfill {
	backfill := &ContactDossierBackfill{
		logger: logger,
		now:    time.Now,
	}
	if contactSource != nil {
		backfill.listContacts = contactSource.ListActivePage
	}
	if sessionSource != nil {
		backfill.listSessions = sessionSource.ListClosedSessionsPage
	}
	if queue != nil {
		backfill.hasRecent = queue.HasRecentWork
		backfill.enqueue = queue.Enqueue
	}
	if state != nil {
		backfill.getState = state.Get
		backfill.setState = state.Set
	}
	return backfill
}

// Run advances at most limit source records and durably records the resulting
// cursor. Limits outside the supported range are clamped to safe defaults.
func (b *ContactDossierBackfill) Run(ctx context.Context, limit int) (ContactDossierBackfillResult, error) {
	if b == nil || b.listContacts == nil || b.listSessions == nil || b.hasRecent == nil ||
		b.enqueue == nil || b.getState == nil || b.setState == nil {
		return ContactDossierBackfillResult{}, fmt.Errorf("contact dossier backfill is not configured")
	}
	if limit <= 0 {
		limit = DefaultContactBackfillLimit
	}
	if limit > MaxContactBackfillLimit {
		limit = MaxContactBackfillLimit
	}
	if err := ctx.Err(); err != nil {
		return ContactDossierBackfillResult{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	state, initialized, err := b.loadState()
	if err != nil {
		return ContactDossierBackfillResult{}, err
	}
	if initialized {
		// Freeze the traversal boundary before any queue mutation. If the
		// process stops mid-page, retrying may revisit work but can never widen
		// the snapshot to sessions or contacts that arrived afterward.
		if err := b.saveState(state); err != nil {
			return ContactDossierBackfillResult{}, err
		}
	}
	if state.Complete {
		return ContactDossierBackfillResult{
			Phase:    backfillPhaseComplete,
			Cutoff:   state.Cutoff,
			Complete: true,
		}, nil
	}

	result := ContactDossierBackfillResult{
		Phase:  state.Phase,
		Cutoff: state.Cutoff,
	}
	switch state.Phase {
	case backfillPhaseContacts:
		err = b.runContactPage(ctx, &state, &result, limit)
	case backfillPhaseSessions:
		err = b.runSessionPage(ctx, &state, &result, limit)
	default:
		return ContactDossierBackfillResult{}, fmt.Errorf("contact dossier backfill has invalid phase %q", state.Phase)
	}
	if err != nil {
		return ContactDossierBackfillResult{}, err
	}
	if err := b.saveState(state); err != nil {
		return ContactDossierBackfillResult{}, err
	}

	result.NextPhase = state.Phase
	result.Complete = state.Complete
	if b.logger != nil {
		b.logger.Info("contact dossier backfill advanced",
			"phase", result.Phase,
			"next_phase", result.NextPhase,
			"scanned", result.Scanned,
			"enqueued", result.Enqueued,
			"skipped_recent", result.SkippedRecent,
			"skipped_empty", result.SkippedEmpty,
			"skipped_automation", result.SkippedAutomation,
			"complete", result.Complete,
		)
	}
	return result, nil
}

func (b *ContactDossierBackfill) runContactPage(
	ctx context.Context,
	state *contactDossierBackfillState,
	result *ContactDossierBackfillResult,
	limit int,
) error {
	page, hasMore, err := b.listContacts(state.Cutoff, state.ContactCursor, limit)
	if err != nil {
		return fmt.Errorf("page active contacts: %w", err)
	}
	for _, contact := range page {
		if err := ctx.Err(); err != nil {
			return err
		}
		if contact == nil {
			return fmt.Errorf("page active contacts: nil contact")
		}
		id := contact.ID.String()
		result.Scanned++
		if err := b.enqueueSubject(ctx, "contact:"+id, "contact", id,
			"Active contact seeded for one-time dossier backfill.", result); err != nil {
			return err
		}
		state.ContactCursor = id
	}
	if !hasMore {
		state.Phase = backfillPhaseSessions
	}
	return nil
}

func (b *ContactDossierBackfill) runSessionPage(
	ctx context.Context,
	state *contactDossierBackfillState,
	result *ContactDossierBackfillResult,
	limit int,
) error {
	page, hasMore, err := b.listSessions(state.Cutoff, state.SessionCursor, limit)
	if err != nil {
		return fmt.Errorf("page closed sessions: %w", err)
	}
	for _, session := range page {
		if err := ctx.Err(); err != nil {
			return err
		}
		if session == nil {
			return fmt.Errorf("page closed sessions: nil session")
		}
		result.Scanned++
		switch {
		case session.MessageCount == 0:
			result.SkippedEmpty++
		case !IsArchivableSession(session.ConversationID):
			result.SkippedAutomation++
		default:
			if err := b.enqueueSubject(ctx, "session:"+session.ID, "session", session.ID,
				"Historical closed session seeded for one-time contact dossier backfill.", result); err != nil {
				return err
			}
		}
		state.SessionCursor = session.ID
	}
	if !hasMore {
		state.Phase = backfillPhaseComplete
		state.Complete = true
	}
	return nil
}

func (b *ContactDossierBackfill) enqueueSubject(
	ctx context.Context,
	key, eventType, id, summary string,
	result *ContactDossierBackfillResult,
) error {
	seen, err := b.hasRecent(ctx, DefinitionName, key)
	if err != nil {
		return fmt.Errorf("inspect archivist queue key %s: %w", key, err)
	}
	if seen {
		result.SkippedRecent++
		return nil
	}

	payload := messages.LoopNotifyPayload{
		Events: []messages.LoopEventPayload{{
			Source:     "contact_dossier_backfill",
			Type:       eventType,
			ID:         id,
			Summary:    summary,
			ObservedAt: b.now().UTC(),
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal archivist queue key %s: %w", key, err)
	}
	if err := b.enqueue(ctx, DefinitionName, key, contactBackfillPriority, raw); err != nil {
		return fmt.Errorf("enqueue archivist queue key %s: %w", key, err)
	}
	result.Enqueued++
	return nil
}

func (b *ContactDossierBackfill) loadState() (contactDossierBackfillState, bool, error) {
	raw, err := b.getState(contactBackfillNamespace, contactBackfillStateKey)
	if err != nil {
		return contactDossierBackfillState{}, false, fmt.Errorf("load contact dossier backfill state: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return contactDossierBackfillState{
			Version: contactBackfillVersion,
			Cutoff:  b.now().UTC(),
			Phase:   backfillPhaseContacts,
		}, true, nil
	}

	var state contactDossierBackfillState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return contactDossierBackfillState{}, false, fmt.Errorf("decode contact dossier backfill state: %w", err)
	}
	if state.Version != contactBackfillVersion {
		return contactDossierBackfillState{}, false, fmt.Errorf("unsupported contact dossier backfill state version %d", state.Version)
	}
	if state.Cutoff.IsZero() {
		return contactDossierBackfillState{}, false, fmt.Errorf("contact dossier backfill state is missing cutoff")
	}
	if state.Complete {
		state.Phase = backfillPhaseComplete
	}
	return state, false, nil
}

func (b *ContactDossierBackfill) saveState(state contactDossierBackfillState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode contact dossier backfill state: %w", err)
	}
	if err := b.setState(contactBackfillNamespace, contactBackfillStateKey, string(raw)); err != nil {
		return fmt.Errorf("save contact dossier backfill state: %w", err)
	}
	return nil
}

// IsArchivableSession reports whether a closed session is worth folding into
// dossiers based on its deterministic conversation origin. Automation and
// auxiliary origins are skipped; unknown origins default to archivable so
// potentially substantive evidence is not silently discarded.
func IsArchivableSession(conversationID string) bool {
	for _, prefix := range automationSessionOrigins {
		if strings.HasPrefix(conversationID, prefix) {
			return false
		}
	}
	return true
}
