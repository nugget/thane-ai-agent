package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

func chicagoZone(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	return loc
}

// calendarFake wires a snapshot to a scripted provider list and call
// response without a registry.
type calendarFake struct {
	infos    []ProviderInfo
	response snapshotCalendarResponse
	err      error
	calls    []CallRequest
	callN    atomic.Int32
}

func (f *calendarFake) list() []ProviderInfo { return f.infos }

func (f *calendarFake) call(_ context.Context, req CallRequest) (json.RawMessage, error) {
	f.calls = append(f.calls, req)
	f.callN.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	raw, err := json.Marshal(f.response)
	return raw, err
}

func calendarProvider(account, clientID string) ProviderInfo {
	return calendarProviderIncarnation(account, clientID, "conn-"+clientID)
}

func calendarProviderIncarnation(account, clientID, id string) ProviderInfo {
	return ProviderInfo{
		ID:       id,
		Account:  account,
		ClientID: clientID,
		Capabilities: []Capability{{
			Name:    "macos.calendar",
			Methods: []string{"list_events", "create_event"},
		}},
	}
}

func snapshotUnderTest(t *testing.T, fake *calendarFake, now time.Time) *CalendarSnapshot {
	t.Helper()
	s := newCalendarSnapshot(fake.list, fake.call, chicagoZone(t), nil)
	s.now = func() time.Time { return now }
	return s
}

func TestCalendarSnapshotRefreshTargetsPinnedClientWithOverlapWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fake := &calendarFake{infos: []ProviderInfo{
		calendarProvider("aimee", "pocket-b"),
		calendarProvider("aimee", "pocket-a"),
		{Account: "nugget", ClientID: "deepslate"}, // no calendar capability
	}}
	s := snapshotUnderTest(t, fake, now)

	s.refreshAll(context.Background())

	if len(fake.calls) != 1 {
		t.Fatalf("calls = %d, want exactly one (capability-less accounts are skipped)", len(fake.calls))
	}
	call := fake.calls[0]
	if call.Account != "aimee" || call.ClientID != "pocket-a" {
		t.Fatalf("routed to %s/%s, want aimee's lexicographically first capable client pocket-a", call.Account, call.ClientID)
	}
	var params struct {
		Start string `json:"start"`
		End   string `json:"end"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	start, _ := time.Parse(time.RFC3339, params.Start)
	end, _ := time.Parse(time.RFC3339, params.End)
	if !start.Equal(now) || end.Sub(start) != calendarSnapshotWindow {
		t.Fatalf("window = [%s, %s], want [now, now+%s]", params.Start, params.End, calendarSnapshotWindow)
	}
	if params.Limit != calendarSnapshotLimit {
		t.Fatalf("limit = %d, want %d", params.Limit, calendarSnapshotLimit)
	}
}

func TestCalendarSnapshotPinSurvivesWhileClientConnected(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fake := &calendarFake{infos: []ProviderInfo{calendarProvider("aimee", "pocket-b")}}
	s := snapshotUnderTest(t, fake, now)

	s.refreshAll(context.Background())
	// A second, alphabetically earlier Mac connects. The pin must hold:
	// two EventKit stores differ, and alternating between them would
	// flap the snapshot between two truths.
	fake.infos = append(fake.infos, calendarProvider("aimee", "pocket-a"))
	s.refreshAll(context.Background())

	if got := fake.calls[1].ClientID; got != "pocket-b" {
		t.Fatalf("second refresh routed to %s, want the pinned pocket-b", got)
	}

	// The pinned Mac drops; the hand-off is deterministic.
	fake.infos = []ProviderInfo{calendarProvider("aimee", "pocket-a")}
	s.refreshAll(context.Background())
	if got := fake.calls[2].ClientID; got != "pocket-a" {
		t.Fatalf("post-drop refresh routed to %s, want pocket-a", got)
	}
}

func TestCalendarSnapshotRendersWallClockTruth(t *testing.T) {
	// 10:00 Saturday morning in Chicago.
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Standup", Calendar: "Work", Start: "2026-08-29T09:30:00-05:00", End: "2026-08-29T10:30:00-05:00"},
			{Title: "Dentist", Calendar: "Personal", Start: "2026-08-29T14:00:00-05:00", End: "2026-08-29T15:00:00-05:00"},
			{Title: "Sunday brunch", Start: "2026-08-30T11:00:00-05:00", End: "2026-08-30T12:00:00-05:00"},
			{Title: "Conference", AllDay: true, Start: "2026-08-28", End: "2026-08-30"},
			{Title: "Next week", AllDay: true, Start: "2026-09-02", End: "2026-09-02"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}

	// The standup started before now and ends after: active, with an
	// ends_in delta — when it ends is the actionable fact.
	for _, want := range []string{
		`"zone":"America/Chicago"`,
		`"account":"aimee"`, `"client":"pocket"`,
		`"active_now":[{"title":"Standup","calendar":"Work"`, `"ends_in":"+1800s"`,
		`"next":{"title":"Dentist"`, `"start_delta":"+4h"`,
		`"today_remaining":[{"title":"Dentist"`,
		`"today_all_day":[{"title":"Conference"`, `"first_day":"2026-08-28","last_day":"2026-08-30"`,
		`"snapshot_age":"-0s"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered block missing %s:\n%s", want, out)
		}
	}
	// Tomorrow's brunch is next-day: not in today_remaining. Next week's
	// all-day event does not include today: absent entirely.
	if strings.Contains(out, `"today_remaining":[{"title":"Sunday brunch"`) || strings.Contains(out, "Next week") {
		t.Fatalf("events outside today leaked into today's view:\n%s", out)
	}
	if strings.Contains(out, `"stale":true`) {
		t.Fatalf("fresh snapshot must not read stale:\n%s", out)
	}
}

func TestCalendarSnapshotActiveNowCatchesAnEventStartedBeforeTheWindow(t *testing.T) {
	// The overlap contract: a meeting already in progress at refresh time
	// is exactly what active_now exists to show. The fake returns it the
	// way EventKit's overlap predicate would; the renderer must classify
	// it active even though its start precedes the request window.
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "All-hands offsite", Start: "2026-08-29T08:00:00-05:00", End: "2026-08-29T17:00:00-05:00"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"active_now":[{"title":"All-hands offsite"`) {
		t.Fatalf("in-progress event missing from active_now:\n%s", out)
	}
	if !strings.Contains(out, `"ends_in":"+7h"`) {
		t.Fatalf("active event should carry ends_in, got:\n%s", out)
	}
}

func TestCalendarSnapshotStaleAndOfflineAreHonest(t *testing.T) {
	fetchedAt := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos:    []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: nil},
	}
	s := snapshotUnderTest(t, fake, fetchedAt)
	s.refreshAll(context.Background())

	// Seven hours pass; the Mac has gone offline (lid closed). The
	// snapshot outlives the connection and says exactly what it is:
	// seven hours old, stale, from an offline companion.
	s.now = func() time.Time { return fetchedAt.Add(7 * time.Hour) }
	fake.infos = nil

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	for _, want := range []string{`"snapshot_age":"-7h"`, `"stale":true`, `"offline":true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("stale block missing %s:\n%s", want, out)
		}
	}
}

func TestCalendarSnapshotRendersNothingBeforeAnyFetch(t *testing.T) {
	fake := &calendarFake{}
	s := snapshotUnderTest(t, fake, time.Now())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if out != "" {
		t.Fatalf("a household with no calendar snapshot should carry no block, got: %q", out)
	}
}

func TestCalendarSnapshotBacksOffInTimeNotPasses(t *testing.T) {
	base := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	now := base
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		err:   fmt.Errorf("companion did not respond"),
	}
	s := snapshotUnderTest(t, fake, base)
	s.now = func() time.Time { return now }

	s.refreshAll(context.Background()) // fails; retry not before one interval

	// A storm of passes at the same instant — the registry-churn shape a
	// flapping second companion produces — must not erode the backoff.
	for i := 0; i < 10; i++ {
		s.refreshAll(context.Background())
	}
	if len(fake.calls) != 1 {
		t.Fatalf("backoff eroded by passes: %d calls, want 1", len(fake.calls))
	}

	// Time, not passes, releases the retry — and the companion answering
	// clears the failure state entirely.
	fake.err = nil
	now = base.Add(calendarSnapshotInterval + time.Second)
	s.refreshAll(context.Background())
	if len(fake.calls) != 2 {
		t.Fatalf("retry did not land after the wait: %d calls", len(fake.calls))
	}
	s.mu.RLock()
	_, ok := s.snapshots["aimee"]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("snapshot should recover once the companion answers again")
	}
}

func TestCalendarSnapshotReconnectClearsBackoff(t *testing.T) {
	// The headline scenario: a lid-closed Mac times out all night and
	// earns the maximum backoff; morning comes, it reconnects, the
	// registry change nudges a refresh — and that refresh must fetch NOW,
	// not serve out a sentence earned while asleep. The backoff state and
	// the nudge were tested separately before this test existed, which is
	// exactly how their broken interaction slipped through.
	base := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	now := base
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		err:   fmt.Errorf("companion did not respond"),
	}
	s := snapshotUnderTest(t, fake, base)
	s.now = func() time.Time { return now }

	// Accrue failures into deep backoff, advancing just far enough each
	// time for the next attempt to land — after the last failure the
	// clock moves only a minute, so the account still owes nearly the
	// whole two-hour wait when the reconnect arrives. That's the pin:
	// if time alone released the retry, this test would prove nothing.
	for i := 0; i < 6; i++ {
		s.refreshAll(context.Background())
		if i < 5 {
			now = now.Add(calendarSnapshotMaxBackoff + time.Minute)
		}
	}
	now = now.Add(time.Minute)

	// The Mac drops off the registry, then reconnects healthy.
	fake.infos = nil
	s.refreshAll(context.Background())
	fake.infos = []ProviderInfo{calendarProvider("aimee", "pocket")}
	fake.err = nil
	before := len(fake.calls)

	s.mu.RLock()
	blocked := now.Before(s.retryAt["aimee"])
	s.mu.RUnlock()
	if !blocked {
		t.Fatal("test setup broken: the account must still be inside its backoff window for the reconnect to prove anything")
	}

	s.refreshAll(context.Background()) // the reconnect nudge's refresh

	if len(fake.calls) != before+1 {
		t.Fatalf("reconnect refresh was skipped by stale backoff (%d calls, want %d)", len(fake.calls), before+1)
	}
	s.mu.RLock()
	_, ok := s.snapshots["aimee"]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("reconnect should repopulate the snapshot immediately")
	}
}

func TestCalendarSnapshotShutdownIsNotAnAccountFailure(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		err:   context.Canceled,
	}
	s := snapshotUnderTest(t, fake, now)

	s.refreshAll(ctx)

	s.mu.RLock()
	failures := s.failures["aimee"]
	s.mu.RUnlock()
	if failures != 0 {
		t.Fatalf("the app dying charged the account %d failures; shutdown is not the companion's fault", failures)
	}
}
func TestCalendarSnapshotNudgeCoalesces(t *testing.T) {
	s := snapshotUnderTest(t, &calendarFake{}, time.Now())
	s.NudgeRefresh()
	s.NudgeRefresh()
	s.NudgeRefresh()
	if len(s.nudge) != 1 {
		t.Fatalf("nudges should coalesce to one pending refresh, got %d", len(s.nudge))
	}
}

func TestCalendarSnapshotLegacyAllDayEndStaysExclusive(t *testing.T) {
	// A pre-#49 companion sends all-day bounds as zone-discarded UTC
	// midnights with an EventKit-exclusive end. The block must render the
	// same inclusive last day the tool renderer derives — one event, one
	// shape — not claim the extra day a literal read of the end implies.
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Legacy conference", AllDay: true, Start: "2026-08-28T05:00:00Z", End: "2026-08-31T05:00:00Z"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"first_day":"2026-08-28","last_day":"2026-08-30"`) {
		t.Fatalf("legacy exclusive end should render inclusive last day 08-30:\n%s", out)
	}
}

func TestCalendarSnapshotNeverPublishesAFabricatedEnd(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 30, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			// Started a moment ago, no end: classified active via the
			// synthetic interval, but no invented end or ends_in may be
			// stated as fact.
			{Title: "Open-ended ping", Start: "2026-08-29T15:00:00Z"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"active_now":[{"title":"Open-ended ping"`) {
		t.Fatalf("endless event should still classify active:\n%s", out)
	}
	if strings.Contains(out, `"end"`) || strings.Contains(out, `"ends_in"`) {
		t.Fatalf("a time nobody scheduled must not be rendered:\n%s", out)
	}
}

func TestCalendarSnapshotEventLocalReadingMatchesToolRule(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Berlin sync", TimeZone: "Europe/Berlin", Start: "2026-08-29T21:00:00+02:00", End: "2026-08-29T22:00:00+02:00"},
			{Title: "Winnipeg call", TimeZone: "America/Winnipeg", Start: "2026-08-29T16:00:00-05:00", End: "2026-08-29T17:00:00-05:00"},
			{Title: "Legacy Z", Start: "2026-08-29T21:30:00Z", End: "2026-08-29T22:30:00Z"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"event_zone":"Europe/Berlin"`) || !strings.Contains(out, `"event_local_start":"2026-08-29T21:00:00+02:00"`) {
		t.Fatalf("a diverging declared zone should carry the event-local reading:\n%s", out)
	}
	// Winnipeg keeps Chicago's clock: no annotation. A bare Z is an older
	// companion discarding the zone, not a UTC-scheduled event: suppressed.
	if strings.Contains(out, "America/Winnipeg") || strings.Contains(out, `"event_zone":"UTC"`) {
		t.Fatalf("same-clock and bare-Z events must not carry event-local noise:\n%s", out)
	}
	// Berlin sync renders twice — as next and in today_remaining — and
	// both rows carry the reading; no other event contributes one.
	if c := strings.Count(out, "event_local_start"); c != 2 {
		t.Fatalf("only Berlin sync's two rows should carry local readings, got %d:\n%s", c, out)
	}
}

func TestCalendarSnapshotEventsSortByStart(t *testing.T) {
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Later", Start: "2026-08-29T16:00:00-05:00", End: "2026-08-29T17:00:00-05:00"},
			{Title: "Sooner", Start: "2026-08-29T14:00:00-05:00", End: "2026-08-29T15:00:00-05:00"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Index(out, "Sooner") > strings.Index(out, "Later") {
		t.Fatalf("today_remaining should sort by start regardless of wire order:\n%s", out)
	}
	if !strings.Contains(out, `"next":{"title":"Sooner"`) {
		t.Fatalf("next should be the earliest upcoming event:\n%s", out)
	}
}

func TestCalendarSnapshotUnavailableRowForNeverFetchedAccount(t *testing.T) {
	// A capable companion is connected but every fetch has failed since
	// boot — the permission-prompt-blocked Mac. Silence would read
	// identically to having no calendar capability; the block says so
	// instead.
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		err:   fmt.Errorf("companion did not respond"),
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	for _, want := range []string{`"account":"aimee"`, `"unavailable":true`, `"last_attempt_age":"-`} {
		if !strings.Contains(out, want) {
			t.Fatalf("unavailable row missing %s:\n%s", want, out)
		}
	}
}

func TestCalendarSnapshotDepartedAccountStopsRenderingAfterCutoff(t *testing.T) {
	fetched := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos:    []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{},
	}
	s := snapshotUnderTest(t, fake, fetched)
	s.refreshAll(context.Background())

	fake.infos = nil // the account departs for good

	// Within the cutoff: the morning-after scenario renders, honestly aged.
	s.now = func() time.Time { return fetched.Add(8 * time.Hour) }
	out, _ := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if !strings.Contains(out, `"offline":true`) {
		t.Fatalf("recent departed snapshot should render offline:\n%s", out)
	}

	// Past the cutoff: no eternal tombstone.
	s.now = func() time.Time { return fetched.Add(calendarSnapshotRenderCutoff + time.Hour) }
	out, _ = s.TagContext(context.Background(), agentctx.ContextRequest{})
	if out != "" {
		t.Fatalf("a long-departed account must stop haunting the prompt, got:\n%s", out)
	}
}

func TestCalendarSnapshotFastReconnectClearsBackoffByIncarnation(t *testing.T) {
	// The nudge channel coalesces, so a disconnect/reconnect pair can
	// arrive as one refresh that never samples the empty provider set.
	// The client_id is stable across the bounce; only the per-connection
	// incarnation betrays that this is a fresh Mac deserving a fresh try.
	base := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	now := base
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProviderIncarnation("aimee", "pocket", "conn-1")},
		err:   fmt.Errorf("companion did not respond"),
	}
	s := snapshotUnderTest(t, fake, base)
	s.now = func() time.Time { return now }

	for i := 0; i < 6; i++ {
		s.refreshAll(context.Background())
		if i < 5 {
			now = now.Add(calendarSnapshotMaxBackoff + time.Minute)
		}
	}
	now = now.Add(time.Minute)

	// Reconnect: same account, same client_id, new incarnation — and the
	// runner never sees the empty set in between.
	fake.infos = []ProviderInfo{calendarProviderIncarnation("aimee", "pocket", "conn-2")}
	fake.err = nil
	before := len(fake.calls)

	s.mu.RLock()
	blocked := now.Before(s.retryAt["aimee"])
	s.mu.RUnlock()
	if !blocked {
		t.Fatal("test setup broken: account must still be inside its backoff window")
	}

	s.refreshAll(context.Background())

	if len(fake.calls) != before+1 {
		t.Fatalf("fast reconnect inherited the old backoff (%d calls, want %d)", len(fake.calls), before+1)
	}
}

func TestCalendarSnapshotOfflineMeansCalendarCapableOffline(t *testing.T) {
	// The calendar Mac departs; a capability-less companion on the same
	// account stays. The snapshot must read offline — and age out at the
	// cutoff — on the strength of calendar availability, not account
	// presence.
	fetched := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos:    []ProviderInfo{calendarProvider("nugget", "macbook")},
		response: snapshotCalendarResponse{},
	}
	s := snapshotUnderTest(t, fake, fetched)
	s.refreshAll(context.Background())

	// Calendar Mac gone; a no-capability companion remains on the account.
	fake.infos = []ProviderInfo{{ID: "conn-x", Account: "nugget", ClientID: "deepslate"}}

	s.now = func() time.Time { return fetched.Add(time.Hour) }
	out, _ := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if !strings.Contains(out, `"offline":true`) {
		t.Fatalf("calendarless account presence must not read as online:\n%s", out)
	}

	s.now = func() time.Time { return fetched.Add(calendarSnapshotRenderCutoff + time.Hour) }
	out, _ = s.TagContext(context.Background(), agentctx.ContextRequest{})
	if strings.Contains(out, `"account":"nugget"`) {
		t.Fatalf("the cutoff must apply despite the capability-less companion:\n%s", out)
	}
}

func TestCalendarSnapshotNextCanBeAnAllDayEvent(t *testing.T) {
	// Tomorrow's holiday is the earliest upcoming event; an empty timed
	// schedule must not render an empty next. The delta is day-granular —
	// a date is not a moment to be early for.
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Labor Day", AllDay: true, Start: "2026-08-30", End: "2026-08-30"},
			{Title: "Monday standup", Start: "2026-08-31T09:00:00-05:00", End: "2026-08-31T09:30:00-05:00"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"next":{"title":"Labor Day"`) {
		t.Fatalf("the all-day event is the earliest upcoming and must be next:\n%s", out)
	}
	if !strings.Contains(out, `"first_day":"2026-08-30"`) || !strings.Contains(out, `"start_delta":"tomorrow"`) {
		t.Fatalf("an all-day next carries inclusive dates and a day-word delta:\n%s", out)
	}
}

func TestCalendarSnapshotInvalidZoneNeitherPublishesNorDefeatsSuppression(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			// An unloadable zone on a legacy bare-Z timestamp: the invalid
			// name must not be published, and it must not defeat the
			// bare-Z suppression by falling through to the embedded
			// offset.
			{Title: "Broken zone Z", TimeZone: "Mars/Olympus_Mons", Start: "2026-08-29T21:00:00Z", End: "2026-08-29T22:00:00Z"},
			// An unloadable zone on an offset-carrying timestamp: the
			// embedded offset is real evidence, so the local reading
			// renders — but with no event_zone name, matching the tool's
			// validated-declaration rule.
			{Title: "Broken zone offset", TimeZone: "Mars/Olympus_Mons", Start: "2026-08-29T21:00:00+02:00", End: "2026-08-29T22:00:00+02:00"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(out, "Mars/Olympus_Mons") {
		t.Fatalf("an unloadable zone name must never be published:\n%s", out)
	}
	if c := strings.Count(out, "event_local_start"); c != 2 {
		// "Broken zone offset" appears as next and in today_remaining;
		// both rows carry the offset-evidence reading. The bare-Z event
		// contributes none.
		t.Fatalf("only the offset-carrying event's rows may carry local readings, got %d:\n%s", c, out)
	}
	if strings.Contains(out, `"event_zone"`) {
		t.Fatalf("no validated zone name exists, so event_zone must be absent:\n%s", out)
	}
}

func TestCalendarSnapshotOrdersByInstantNotText(t *testing.T) {
	// RFC3339 text does not sort as time sorts: "16:00-05:00" is after
	// "21:00+02:00" (14:00 home) but sorts before it lexicographically.
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Chicago late", Start: "2026-08-29T16:00:00-05:00", End: "2026-08-29T17:00:00-05:00"},
			{Title: "Berlin early", Start: "2026-08-29T21:00:00+02:00", End: "2026-08-29T22:00:00+02:00"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.Contains(out, `"next":{"title":"Berlin early"`) {
		t.Fatalf("next must be the earlier instant, not the lexicographically smaller text:\n%s", out)
	}
	if strings.Index(out, "Berlin early") > strings.Index(out, "Chicago late") {
		t.Fatalf("today_remaining must order by instant:\n%s", out)
	}
}

func TestCalendarSnapshotMalformedEndsAreDriftNotOmission(t *testing.T) {
	// A non-empty end that does not parse is companion drift. The tool
	// renderer marks the same payload unparsed and loud; the ambient
	// block's equivalent is absence — never a confident one-day range or
	// a briefly-active classification fabricated from corrupt bytes.
	now := time.Date(2026, 8, 29, 15, 0, 30, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		response: snapshotCalendarResponse{Events: []snapshotCalendarEvent{
			{Title: "Broken all-day", AllDay: true, Start: "2026-08-29", End: "not-a-date"},
			{Title: "Broken timed", Start: "2026-08-29T15:00:00Z", End: "garbage"},
			{Title: "Genuinely endless", Start: "2026-08-29T15:00:00Z"},
		}},
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, err := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if strings.Contains(out, "Broken all-day") || strings.Contains(out, "Broken timed") {
		t.Fatalf("malformed non-empty ends must be omitted, not reshaped:\n%s", out)
	}
	// The truly absent end keeps its synthetic classification.
	if !strings.Contains(out, `"active_now":[{"title":"Genuinely endless"`) {
		t.Fatalf("an absent end still classifies through the synthetic interval:\n%s", out)
	}
}

func TestCalendarSnapshotUnavailableRowCarriesNoSnapshotAge(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	fake := &calendarFake{
		infos: []ProviderInfo{calendarProvider("aimee", "pocket")},
		err:   fmt.Errorf("companion did not respond"),
	}
	s := snapshotUnderTest(t, fake, now)
	s.refreshAll(context.Background())

	out, _ := s.TagContext(context.Background(), agentctx.ContextRequest{})
	if strings.Contains(out, `"snapshot_age"`) {
		t.Fatalf("a row stating no snapshot exists must not carry an empty age:\n%s", out)
	}
}
