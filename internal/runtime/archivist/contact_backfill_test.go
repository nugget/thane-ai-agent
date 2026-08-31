package archivist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type fakeContactBackfillSource struct {
	contacts []*contacts.Contact
	limits   []int
}

func (f *fakeContactBackfillSource) ListActivePage(_ time.Time, afterID string, limit int) ([]*contacts.Contact, bool, error) {
	f.limits = append(f.limits, limit)
	var page []*contacts.Contact
	for _, contact := range f.contacts {
		if contact.ID.String() > afterID {
			page = append(page, contact)
		}
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	return page, hasMore, nil
}

type fakeSessionBackfillSource struct {
	sessions []*memory.Session
	limits   []int
}

func (f *fakeSessionBackfillSource) ListClosedSessionsPage(_ time.Time, afterID string, limit int) ([]*memory.Session, bool, error) {
	f.limits = append(f.limits, limit)
	var page []*memory.Session
	for _, session := range f.sessions {
		if session.ID > afterID {
			page = append(page, session)
		}
	}
	hasMore := len(page) > limit
	if hasMore {
		page = page[:limit]
	}
	return page, hasMore, nil
}

type fakeBackfillEnqueue struct {
	consumer string
	key      string
	priority int
	payload  []byte
}

type fakeContactBackfillQueue struct {
	seen     map[string]bool
	enqueues []fakeBackfillEnqueue
}

func (f *fakeContactBackfillQueue) HasRecentWork(_ context.Context, consumerLoop, dedupKey string) (bool, error) {
	return f.seen[consumerLoop+"/"+dedupKey], nil
}

func (f *fakeContactBackfillQueue) Enqueue(_ context.Context, consumerLoop, dedupKey string, priority int, payload []byte) error {
	f.enqueues = append(f.enqueues, fakeBackfillEnqueue{
		consumer: consumerLoop,
		key:      dedupKey,
		priority: priority,
		payload:  append([]byte(nil), payload...),
	})
	f.seen[consumerLoop+"/"+dedupKey] = true
	return nil
}

type fakeContactBackfillState struct {
	value    string
	setCalls int
	failOn   int
}

func (f *fakeContactBackfillState) Get(_, _ string) (string, error) {
	return f.value, nil
}

func (f *fakeContactBackfillState) Set(_, _, value string) error {
	f.setCalls++
	if f.failOn == f.setCalls {
		return errors.New("state unavailable")
	}
	f.value = value
	return nil
}

func TestContactDossierBackfill_BoundedPhasesAndDurableCompletion(t *testing.T) {
	fixedNow := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	contactA := uuid.MustParse("019c0000-0000-7000-8000-000000000001")
	contactB := uuid.MustParse("019c0000-0000-7000-8000-000000000002")
	contactsSource := &fakeContactBackfillSource{contacts: []*contacts.Contact{
		{ID: contactA},
		{ID: contactB},
	}}
	sessionsSource := &fakeSessionBackfillSource{sessions: []*memory.Session{
		{ID: "019d0000-0000-7000-8000-000000000001", ConversationID: "signal-contact", MessageCount: 3},
		{ID: "019d0000-0000-7000-8000-000000000002", ConversationID: "signal-empty", MessageCount: 0},
		{ID: "019d0000-0000-7000-8000-000000000003", ConversationID: "loop-archivist-1", MessageCount: 5},
	}}
	queue := &fakeContactBackfillQueue{seen: map[string]bool{
		DefinitionName + "/contact:" + contactB.String(): true,
	}}
	state := &fakeContactBackfillState{}
	backfill := newFakeContactDossierBackfill(contactsSource, sessionsSource, queue, state)
	backfill.now = func() time.Time { return fixedNow }

	first, err := backfill.Run(t.Context(), 2)
	if err != nil {
		t.Fatalf("contact page: %v", err)
	}
	if first.Phase != backfillPhaseContacts || first.NextPhase != backfillPhaseSessions {
		t.Fatalf("contact page phases = %q -> %q", first.Phase, first.NextPhase)
	}
	if first.Scanned != 2 || first.Enqueued != 1 || first.SkippedRecent != 1 || first.Complete {
		t.Fatalf("contact page result = %+v", first)
	}
	if !first.Cutoff.Equal(fixedNow) {
		t.Fatalf("cutoff = %v, want %v", first.Cutoff, fixedNow)
	}

	second, err := backfill.Run(t.Context(), 2000)
	if err != nil {
		t.Fatalf("session page: %v", err)
	}
	if second.Phase != backfillPhaseSessions || second.NextPhase != backfillPhaseComplete || !second.Complete {
		t.Fatalf("session page phases = %q -> %q, complete=%v", second.Phase, second.NextPhase, second.Complete)
	}
	if second.Scanned != 3 || second.Enqueued != 1 || second.SkippedEmpty != 1 || second.SkippedAutomation != 1 {
		t.Fatalf("session page result = %+v", second)
	}
	if len(sessionsSource.limits) != 1 || sessionsSource.limits[0] != MaxContactBackfillLimit {
		t.Fatalf("session limits = %v, want [%d]", sessionsSource.limits, MaxContactBackfillLimit)
	}

	third, err := backfill.Run(t.Context(), 10)
	if err != nil {
		t.Fatalf("completed no-op: %v", err)
	}
	if !third.Complete || third.Phase != backfillPhaseComplete || third.Scanned != 0 || third.Enqueued != 0 {
		t.Fatalf("completed result = %+v", third)
	}
	if len(queue.enqueues) != 2 {
		t.Fatalf("enqueues = %d, want 2", len(queue.enqueues))
	}
	for _, enqueue := range queue.enqueues {
		if enqueue.consumer != DefinitionName || enqueue.priority != contactBackfillPriority {
			t.Errorf("enqueue = %+v", enqueue)
		}
		var payload messages.LoopNotifyPayload
		if err := json.Unmarshal(enqueue.payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if bytes.Contains(enqueue.payload, []byte(`"observed_at"`)) {
			t.Errorf("payload = %s, want observed_at omitted", enqueue.payload)
		}
		if len(payload.Events) != 1 {
			t.Fatalf("payload events = %+v, want one", payload.Events)
		}
		if payload.Events[0].Source != "contact_dossier_backfill" {
			t.Errorf("payload source = %q, want contact_dossier_backfill", payload.Events[0].Source)
		}
		if !payload.Events[0].ObservedAt.IsZero() {
			t.Errorf("payload observed_at = %v, want omitted", payload.Events[0].ObservedAt)
		}
	}
}

func TestContactDossierBackfill_StateFailureRetriesWithoutDuplicate(t *testing.T) {
	contactID := uuid.MustParse("019c0000-0000-7000-8000-000000000001")
	contactsSource := &fakeContactBackfillSource{contacts: []*contacts.Contact{{ID: contactID}}}
	queue := &fakeContactBackfillQueue{seen: make(map[string]bool)}
	state := &fakeContactBackfillState{failOn: 2}
	backfill := newFakeContactDossierBackfill(contactsSource, &fakeSessionBackfillSource{}, queue, state)

	if _, err := backfill.Run(t.Context(), 10); err == nil || !strings.Contains(err.Error(), "state unavailable") {
		t.Fatalf("first Run error = %v, want state failure", err)
	}
	if len(queue.enqueues) != 1 {
		t.Fatalf("first Run enqueues = %d, want 1", len(queue.enqueues))
	}

	retry, err := backfill.Run(t.Context(), 10)
	if err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if retry.Enqueued != 0 || retry.SkippedRecent != 1 {
		t.Fatalf("retry result = %+v", retry)
	}
	if len(queue.enqueues) != 1 {
		t.Fatalf("retry duplicated enqueue: got %d total", len(queue.enqueues))
	}
}

func newFakeContactDossierBackfill(
	contactSource *fakeContactBackfillSource,
	sessionSource *fakeSessionBackfillSource,
	queue *fakeContactBackfillQueue,
	state *fakeContactBackfillState,
) *ContactDossierBackfill {
	return &ContactDossierBackfill{
		listContacts: contactSource.ListActivePage,
		listSessions: sessionSource.ListClosedSessionsPage,
		hasRecent:    queue.HasRecentWork,
		enqueue:      queue.Enqueue,
		getState:     state.Get,
		setState:     state.Set,
		now:          time.Now,
	}
}
