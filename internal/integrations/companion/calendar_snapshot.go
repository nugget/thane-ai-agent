package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// Calendar snapshot defaults. The window and limit describe what ambient
// context needs — today and tomorrow, a few dozen events — not what the
// calendar tools can reach; anything beyond this is a pull through
// macos_calendar_events.
const (
	calendarSnapshotWindow   = 48 * time.Hour
	calendarSnapshotLimit    = 30
	calendarSnapshotInterval = 15 * time.Minute

	// calendarSnapshotCallTimeout bounds one refresh call, mirroring the
	// dispatch bound the calendar tools use: a Mac blocked on a
	// permission prompt must cost a refresh tick, never wedge the runner.
	calendarSnapshotCallTimeout = 30 * time.Second

	// calendarSnapshotMaxBackoff caps how long a failing account waits
	// between attempts. Failure is the normal overnight state of a
	// lid-closed laptop, so backoff exists to quiet the attempts, not to
	// give up: even at the cap the account retries every two hours, and
	// a reconnect clears the wait entirely. Denominated in time, not
	// refresh passes — nudges from unrelated companions' churn must not
	// erode a wedged Mac's backoff into a retry storm.
	calendarSnapshotMaxBackoff = 2 * time.Hour

	// calendarSnapshotStaleAfter is the age past which a rendered
	// snapshot must carry the stale flag. Twice the refresh interval:
	// one missed tick is jitter, two is a story the reader needs.
	calendarSnapshotStaleAfter = 2 * calendarSnapshotInterval

	// calendarSnapshotRenderCutoff is how long a departed account's
	// snapshot keeps rendering. The morning-after scenario needs hours;
	// a Mac gone for good needs to stop haunting every prompt.
	calendarSnapshotRenderCutoff = 24 * time.Hour
)

// snapshotCalendarEvent mirrors the companion calendar wire shape. The
// field vocabulary deliberately tracks the tool renderer's
// (internal/tools/companion_calendar_format.go): the same event must not
// reach the model under two names depending on whether it arrived
// ambiently or through a tool call.
type snapshotCalendarEvent struct {
	Title    string `json:"title"`
	Calendar string `json:"calendar,omitempty"`
	Start    string `json:"start"`
	End      string `json:"end,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`
	TimeZone string `json:"time_zone,omitempty"`
	Location string `json:"location,omitempty"`
}

type snapshotCalendarResponse struct {
	Events    []snapshotCalendarEvent `json:"events"`
	Truncated bool                    `json:"truncated,omitempty"`
}

// accountCalendarSnapshot is one account's most recent successful fetch.
// A snapshot outlives its companion's connection on purpose: the morning's
// first turns after a lid-closed night legitimately render yesterday
// evening's events with an honest age, which beats rendering nothing.
type accountCalendarSnapshot struct {
	Account   string
	ClientID  string
	FetchedAt time.Time
	Events    []snapshotCalendarEvent
	Truncated bool
}

// calendarProviderLister enumerates the connected providers; a func
// rather than an interface, matching the registrar's injection style, so
// tests supply a fake list without a fake registry.
type calendarProviderLister func() []ProviderInfo

// CalendarSnapshot keeps an in-memory, background-refreshed view of every
// connected companion account's near-term calendar, and renders it as an
// always-on Live State block — the wall-clock-truth half of the #1432
// design. Assembly only ever reads memory; the companion is called on the
// runner's own clock, under its own bound, never inside a turn.
type CalendarSnapshot struct {
	list     calendarProviderLister
	call     func(ctx context.Context, req CallRequest) (json.RawMessage, error)
	homeZone *time.Location
	now      func() time.Time
	logger   *slog.Logger

	mu        sync.RWMutex
	snapshots map[string]accountCalendarSnapshot
	pinned    map[string]string // account → client_id, held while connected
	failures  map[string]int    // account → consecutive refresh failures
	retryAt   map[string]time.Time
	capable   map[string]string // account → sorted capable client_ids fingerprint
	attempts  map[string]time.Time
	// sharingOff marks accounts whose companion refused with
	// calendar_sharing_disabled — an operator setting, not a fault.
	// Deliberately NOT cleared by the capability-fingerprint reset:
	// connection churn on a still-disabled account must stay silent,
	// while the reset's retryAt clearing already re-probes each fresh
	// connection immediately.
	sharingOff map[string]bool

	nudge chan struct{}
}

// NewCalendarSnapshot builds the snapshot service over a live registry.
func NewCalendarSnapshot(registry *Registry, homeZone *time.Location, logger *slog.Logger) *CalendarSnapshot {
	return newCalendarSnapshot(registry.List, registry.Call, homeZone, logger)
}

// newCalendarSnapshot is the func-injected constructor tests use.
func newCalendarSnapshot(list calendarProviderLister, call func(context.Context, CallRequest) (json.RawMessage, error), homeZone *time.Location, logger *slog.Logger) *CalendarSnapshot {
	if logger == nil {
		logger = slog.Default()
	}
	if homeZone == nil {
		homeZone = time.Local
	}
	return &CalendarSnapshot{
		list:       list,
		call:       call,
		homeZone:   homeZone,
		now:        time.Now,
		logger:     logger.With("component", "calendar_snapshot"),
		snapshots:  make(map[string]accountCalendarSnapshot),
		pinned:     make(map[string]string),
		failures:   make(map[string]int),
		retryAt:    make(map[string]time.Time),
		capable:    make(map[string]string),
		attempts:   make(map[string]time.Time),
		sharingOff: make(map[string]bool),
		nudge:      make(chan struct{}, 1),
	}
}

// NudgeRefresh asks the runner to refresh soon — wired to the registry's
// change callback so a companion connecting after a night away repopulates
// the snapshot without waiting out the interval. Non-blocking and
// coalescing: a burst of reconnects is one refresh.
func (s *CalendarSnapshot) NudgeRefresh() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Run refreshes on the interval and on nudges until ctx ends.
func (s *CalendarSnapshot) Run(ctx context.Context) {
	ticker := time.NewTicker(calendarSnapshotInterval)
	defer ticker.Stop()

	s.refreshAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshAll(ctx)
		case <-s.nudge:
			s.refreshAll(ctx)
		}
	}
}

// refreshAll fetches every calendar-capable connected account, honoring
// per-account backoff.
func (s *CalendarSnapshot) refreshAll(ctx context.Context) {
	now := s.now()
	for account, clientID := range s.calendarAccounts() {
		s.mu.Lock()
		waiting := now.Before(s.retryAt[account])
		if !waiting {
			s.attempts[account] = now
		}
		s.mu.Unlock()
		if waiting {
			continue
		}
		s.refreshAccount(ctx, account, clientID)
	}
}

// calendarAccounts maps each connected account that offers
// macos.calendar/list_events to its pinned client. Pinning matters when
// one account has two Macs online: their EventKit stores differ, and a
// snapshot that alternated between them would flap between two truths.
// The pin holds while that client stays connected; when it drops, the
// lexicographically first capable client takes over — a deterministic
// hand-off the snapshot's provenance makes visible.
func (s *CalendarSnapshot) calendarAccounts() map[string]string {
	capable := make(map[string][]string)
	incarnations := make(map[string][]string)
	for _, info := range s.list() {
		for _, capability := range info.Capabilities {
			if capability.Name != "macos.calendar" {
				continue
			}
			for _, method := range capability.Methods {
				if method == "list_events" {
					capable[info.Account] = append(capable[info.Account], info.ClientID)
					// The provider ID is minted fresh per connection —
					// useless for stable identity, which is exactly why
					// it belongs in the change fingerprint: a fast
					// disconnect/reconnect can coalesce into one nudge
					// that never samples the empty set, and the same
					// client_id would then read as no change at all.
					// The incarnation cannot lie about that.
					incarnations[info.Account] = append(incarnations[info.Account], info.ID)
					break
				}
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Forget the fingerprint of any account that departed entirely, so
	// its eventual reconnect — even with the identical client — reads as
	// the set change it is. Without this, a Mac that fully disconnects
	// and returns with the same client_id matches its old fingerprint
	// and inherits a backoff sentence earned while unreachable.
	for account := range s.capable {
		if _, still := capable[account]; !still {
			delete(s.capable, account)
		}
	}

	out := make(map[string]string, len(capable))
	for account, clients := range capable {
		sort.Strings(clients)

		// A change in the capable-client set is new evidence that
		// invalidates the backoff: the Mac that was timing out all
		// night is not the Mac that just connected, even when it is —
		// a fresh connection means a fresh chance to answer. Without
		// this, the reconnect nudge arrives, finds the account still
		// serving out a two-hour sentence earned while asleep, and the
		// snapshot stays stale with the Mac sitting right there.
		ids := incarnations[account]
		sort.Strings(ids)
		fingerprint := strings.Join(clients, "\x00") + "\x00\x00" + strings.Join(ids, "\x00")
		if s.capable[account] != fingerprint {
			s.capable[account] = fingerprint
			s.failures[account] = 0
			delete(s.retryAt, account)
		}

		pinnedClient := s.pinned[account]
		stillConnected := false
		for _, c := range clients {
			if c == pinnedClient {
				stillConnected = true
				break
			}
		}
		if !stillConnected {
			pinnedClient = clients[0]
			s.pinned[account] = pinnedClient
		}
		out[account] = pinnedClient
	}
	return out
}

// refreshAccount fetches one account's window. EventKit's range predicate
// matches events that overlap the window, so a meeting already in
// progress at refresh time — the one thing active-now exists to show —
// comes back even though its start precedes the window's.
func (s *CalendarSnapshot) refreshAccount(ctx context.Context, account, clientID string) {
	now := s.now().In(s.homeZone)
	params, err := json.Marshal(map[string]any{
		"start": now.Format(time.RFC3339),
		"end":   now.Add(calendarSnapshotWindow).Format(time.RFC3339),
		"limit": calendarSnapshotLimit,
	})
	if err != nil {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, calendarSnapshotCallTimeout)
	raw, err := s.call(callCtx, CallRequest{
		Account:    account,
		ClientID:   clientID,
		Capability: "macos.calendar",
		Method:     "list_events",
		Params:     params,
	})
	cancel()
	if err != nil {
		// The app dying is not the companion failing: a shutdown that
		// coincides with a tick must not warn about the account or
		// charge it backoff (the retry paths are where mistakes like
		// that live longest).
		if ctx.Err() != nil {
			return
		}
		// A refusal that encodes configuration is not a fault: the
		// operator switched calendar sharing off on purpose, and no
		// amount of retrying changes a setting.
		var refusal *Error
		if errors.As(err, &refusal) && refusal.Code == calendarErrCodeSharingDisabled {
			s.recordSharingDisabled(account)
			return
		}
		s.recordFailure(account, err)
		return
	}

	var resp snapshotCalendarResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		s.recordFailure(account, fmt.Errorf("decode calendar snapshot: %w", err))
		return
	}

	s.mu.Lock()
	recovered := s.failures[account] > 0
	restored := s.sharingOff[account]
	s.failures[account] = 0
	delete(s.retryAt, account)
	delete(s.sharingOff, account)
	s.snapshots[account] = accountCalendarSnapshot{
		Account:   account,
		ClientID:  clientID,
		FetchedAt: now,
		Events:    resp.Events,
		Truncated: resp.Truncated,
	}
	s.mu.Unlock()
	if restored {
		s.logger.Info("calendar sharing re-enabled", "account", account, "events", len(resp.Events))
	}
	if recovered {
		s.logger.Info("calendar snapshot recovered", "account", account, "events", len(resp.Events))
	}
}

// calendarErrCodeSharingDisabled is the companion app's refusal code for
// an account whose operator has calendar sharing switched off.
const calendarErrCodeSharingDisabled = "calendar_sharing_disabled"

// recordSharingDisabled handles that refusal as what it is — a chosen
// setting, not a fault. The account takes no failure count and no WARN;
// the transition logs once at Info, and the account re-probes quietly at
// the backoff cap so a flipped setting is noticed within the cap even
// without a reconnect (a reconnect re-probes immediately: the
// capability-fingerprint reset clears retryAt but not sharingOff, so
// connection churn on a still-disabled account stays silent).
func (s *CalendarSnapshot) recordSharingDisabled(account string) {
	s.mu.Lock()
	transition := !s.sharingOff[account]
	s.sharingOff[account] = true
	s.failures[account] = 0
	s.retryAt[account] = s.now().Add(calendarSnapshotMaxBackoff)
	// The operator's choice takes effect in the prompt immediately:
	// events fetched while sharing was still on must not keep rendering
	// beside the disabled flag — a connected Mac never hits the render
	// cutoff, so without this the stale snapshot would outlive the
	// setting indefinitely.
	delete(s.snapshots, account)
	s.mu.Unlock()
	if transition {
		s.logger.Info("calendar sharing disabled in the companion app; probing quietly",
			"account", account)
	}
}

// recordFailure backs the account off without log spam: the first failure
// warns, later ones only extend the skip count. An overnight lid-closed
// Mac makes failure the normal state, and a log line per tick would bury
// the signal a real fault carries.
func (s *CalendarSnapshot) recordFailure(account string, err error) {
	s.mu.Lock()
	s.failures[account]++
	failures := s.failures[account]
	// One interval after the first failure — a single miss is jitter and
	// costs nothing visible — then doubling to the cap.
	wait := calendarSnapshotInterval << min(failures-1, 4)
	if wait > calendarSnapshotMaxBackoff {
		wait = calendarSnapshotMaxBackoff
	}
	s.retryAt[account] = s.now().Add(wait)
	s.mu.Unlock()
	if failures == 1 {
		s.logger.Warn("calendar snapshot refresh failed; backing off",
			"account", account, "error", err)
	}
}

// TagContextBucket places the calendar block in live state beside the
// watchlist: current operational world-state, uncached by design.
func (s *CalendarSnapshot) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// TagContext renders the block from memory and the registry's in-memory
// provider list; nothing here can block on a companion or the disk.
func (s *CalendarSnapshot) TagContext(context.Context, agentctx.ContextRequest) (string, error) {
	now := s.now().In(s.homeZone)
	capableClients := s.calendarAccounts()

	s.mu.RLock()
	snaps := make([]accountCalendarSnapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		snaps = append(snaps, snap)
	}
	attempts := make(map[string]time.Time, len(s.attempts))
	for account, at := range s.attempts {
		attempts[account] = at
	}
	sharingOff := make(map[string]bool, len(s.sharingOff))
	for account, off := range s.sharingOff {
		sharingOff[account] = off
	}
	s.mu.RUnlock()

	// Connectivity means calendar-capable connectivity: an account whose
	// only remaining companion has no calendar would otherwise read as
	// online forever, its stale snapshot bypassing the departure cutoff
	// on the strength of a Mac that cannot answer.
	connected := make(map[string]bool, len(capableClients))
	for account := range capableClients {
		connected[account] = true
	}
	haveSnapshot := make(map[string]bool, len(snaps))

	var accounts []renderedCalendarAccount
	for _, snap := range snaps {
		haveSnapshot[snap.Account] = true
		// A departed account's snapshot serves the morning-after
		// scenario for a day; past that it is an eternal tombstone,
		// which the block stops carrying.
		if !connected[snap.Account] && now.Sub(snap.FetchedAt) > calendarSnapshotRenderCutoff {
			continue
		}
		row := s.renderAccount(snap, now, connected[snap.Account])
		row.SharingDisabled = sharingOff[snap.Account]
		accounts = append(accounts, row)
	}
	// The honest-absence row: a capable companion is connected but no
	// fetch has ever succeeded. Without it, a permission-blocked Mac and
	// a household with no calendar capability read identically as
	// silence — the third behavior the design forbids.
	for account, clientID := range capableClients {
		if haveSnapshot[account] {
			continue
		}
		row := renderedCalendarAccount{Account: account, Client: clientID, Unavailable: true}
		row.SharingDisabled = sharingOff[account]
		if at, ok := attempts[account]; ok {
			row.LastAttemptAge = promptfmt.FormatDeltaOnly(at, now)
		}
		accounts = append(accounts, row)
	}
	if len(accounts) == 0 {
		// Nothing fetched and nothing capable: a household with no
		// calendar-capable companion carries no calendar block, rather
		// than a standing apology for a capability it does not have.
		return "", nil
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Account < accounts[j].Account })

	payload := struct {
		Zone     string                    `json:"zone"`
		Accounts []renderedCalendarAccount `json:"accounts"`
	}{
		Zone:     s.homeZone.String(),
		Accounts: accounts,
	}
	return "### Calendar\n\n" + promptfmt.MarshalCompact(payload), nil
}

// renderedCalendarAccount is one account's block as the model reads it.
type renderedCalendarAccount struct {
	Account     string                  `json:"account"`
	Client      string                  `json:"client,omitempty"`
	SnapshotAge string                  `json:"snapshot_age,omitempty"`
	Stale       bool                    `json:"stale,omitempty"`
	Offline     bool                    `json:"offline,omitempty"`
	ActiveNow   []renderedCalendarEvent `json:"active_now,omitempty"`
	Next        *renderedCalendarEvent  `json:"next,omitempty"`
	TodayLeft   []renderedCalendarEvent `json:"today_remaining,omitempty"`
	TodayAllDay []renderedCalendarEvent `json:"today_all_day,omitempty"`
	Truncated   bool                    `json:"truncated,omitempty"`

	// Unavailable marks a capable, connected companion that has never
	// produced a snapshot — a Mac blocked on a permission prompt, most
	// likely. Silence here would be indistinguishable from having no
	// calendar capability at all, which is the third behavior the
	// design forbids.
	Unavailable    bool   `json:"unavailable,omitempty"`
	LastAttemptAge string `json:"last_attempt_age,omitempty"`

	// SharingDisabled names the cause when the companion is connected
	// but refuses calendar reads: the operator has calendar sharing
	// switched off in the companion app. This absence is chosen —
	// don't suggest fixing connectivity or permissions; the operator
	// can enable it in the companion app's Settings > Calendar if they
	// want this account's calendar visible.
	SharingDisabled bool `json:"sharing_disabled,omitempty"`
}

// renderedCalendarEvent reuses the tool renderer's field vocabulary. The
// derivations differ by role: an active event's useful delta is when it
// ends, an upcoming one's is when it starts.
type renderedCalendarEvent struct {
	Title      string `json:"title"`
	Calendar   string `json:"calendar,omitempty"`
	Start      string `json:"start,omitempty"`
	End        string `json:"end,omitempty"`
	FirstDay   string `json:"first_day,omitempty"`
	LastDay    string `json:"last_day,omitempty"`
	StartDelta string `json:"start_delta,omitempty"`
	// EndsIn is this block's one addition to the tool vocabulary: an
	// active event's actionable delta is when it ends, a shape the tool
	// renderer never needs because it renders windows, not "now".
	EndsIn          string `json:"ends_in,omitempty"`
	EventZone       string `json:"event_zone,omitempty"`
	EventLocalStart string `json:"event_local_start,omitempty"`
	EventLocalEnd   string `json:"event_local_end,omitempty"`
	Location        string `json:"location,omitempty"`
}

// renderAccount derives the account's view at one instant. Events are
// parsed first and ordered by instant — RFC3339 text with mixed offsets
// does not sort as time sorts, and a Berlin-offset event must not trail a
// Chicago one it precedes. A rendered event carries the event's own clock
// beside home's whenever the two disagree, the same rule and field names
// as the tool renderer — one event, one vocabulary, however it reaches
// the model.
func (s *CalendarSnapshot) renderAccount(snap accountCalendarSnapshot, now time.Time, connected bool) renderedCalendarAccount {
	out := renderedCalendarAccount{
		Account:     snap.Account,
		Client:      snap.ClientID,
		SnapshotAge: promptfmt.FormatDeltaOnly(snap.FetchedAt, now),
		Stale:       now.Sub(snap.FetchedAt) > calendarSnapshotStaleAfter,
		Offline:     !connected,
		Truncated:   snap.Truncated,
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var timed []snapshotTimedEvent
	var nextAllDay *snapshotAllDayEvent
	for _, event := range snap.Events {
		if event.AllDay {
			first, last, ok := allDayRange(event)
			if !ok {
				continue
			}
			switch {
			case !today.Before(first) && !today.After(last):
				out.TodayAllDay = append(out.TodayAllDay, renderedCalendarEvent{
					Title: event.Title, Calendar: event.Calendar,
					FirstDay: first.Format("2006-01-02"), LastDay: last.Format("2006-01-02"),
					Location: event.Location,
				})
			case first.After(today):
				if nextAllDay == nil || first.Before(nextAllDay.first) {
					nextAllDay = &snapshotAllDayEvent{event: event, first: first, last: last}
				}
			}
			continue
		}
		start, end, endOK, ok := parseSnapshotSpan(event)
		if !ok {
			// A boundary that does not parse is a companion bug; the
			// ambient block is the wrong place to surface bytes, so the
			// event is omitted here and remains visible through the
			// calendar tool, which echoes malformed input loudly.
			continue
		}
		timed = append(timed, snapshotTimedEvent{event: event, start: start, end: end, endOK: endOK})
	}
	sort.SliceStable(timed, func(i, j int) bool {
		if !timed[i].start.Equal(timed[j].start) {
			return timed[i].start.Before(timed[j].start)
		}
		return timed[i].event.Title < timed[j].event.Title
	})
	sort.SliceStable(out.TodayAllDay, func(i, j int) bool {
		if out.TodayAllDay[i].FirstDay != out.TodayAllDay[j].FirstDay {
			return out.TodayAllDay[i].FirstDay < out.TodayAllDay[j].FirstDay
		}
		return out.TodayAllDay[i].Title < out.TodayAllDay[j].Title
	})

	var nextTimed *snapshotTimedEvent
	for i := range timed {
		item := &timed[i]
		switch {
		case !item.start.After(now) && item.end.After(now):
			rendered := s.renderTimed(item.event, item.start, item.end, item.endOK)
			if item.endOK {
				rendered.EndsIn = promptfmt.FormatDeltaOnly(item.end, now)
			}
			out.ActiveNow = append(out.ActiveNow, rendered)
		case item.start.After(now):
			if nextTimed == nil {
				nextTimed = item
			}
			if sameHomeDay(item.start.In(s.homeZone), now) {
				rendered := s.renderTimed(item.event, item.start, item.end, item.endOK)
				rendered.StartDelta = promptfmt.FormatDeltaOnly(item.start, now)
				out.TodayLeft = append(out.TodayLeft, rendered)
			}
		}
	}

	// Next is the earliest upcoming event of either kind. An all-day
	// event's instant, for the comparison only, is home midnight of its
	// first day; its rendered delta stays day-granular, because a date
	// is not a moment to be early for.
	if nextAllDay != nil {
		allDayInstant := time.Date(nextAllDay.first.Year(), nextAllDay.first.Month(), nextAllDay.first.Day(), 0, 0, 0, 0, s.homeZone)
		if nextTimed == nil || allDayInstant.Before(nextTimed.start) {
			out.Next = &renderedCalendarEvent{
				Title: nextAllDay.event.Title, Calendar: nextAllDay.event.Calendar,
				FirstDay:   nextAllDay.first.Format("2006-01-02"),
				LastDay:    nextAllDay.last.Format("2006-01-02"),
				StartDelta: promptfmt.FormatDayDelta(nextAllDay.first, now),
				Location:   nextAllDay.event.Location,
			}
		}
	}
	if out.Next == nil && nextTimed != nil {
		rendered := s.renderTimed(nextTimed.event, nextTimed.start, nextTimed.end, nextTimed.endOK)
		rendered.StartDelta = promptfmt.FormatDeltaOnly(nextTimed.start, now)
		out.Next = &rendered
	}
	return out
}

type snapshotAllDayEvent struct {
	event snapshotCalendarEvent
	first time.Time
	last  time.Time
}

// allDayRange resolves an all-day event's inclusive date range: date-form
// bounds directly (the Mac already resolved inclusivity), instant-form
// legacy bounds via midnight rounding with the EventKit-exclusive end
// converted — the same rules the tool renderer applies to the same wire.
func allDayRange(event snapshotCalendarEvent) (first, last time.Time, ok bool) {
	first, _, ok = parseSnapshotDate(event.Start)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	last = first
	if event.End != "" {
		parsed, dateOnly, ok := parseSnapshotDate(event.End)
		if !ok {
			// A non-empty end that does not parse is companion drift,
			// not an omission — rendering a confident one-day event from
			// it would state a range nobody published. The tool renderer
			// marks the same payload unparsed; the ambient block's
			// equivalent of loud is absence.
			return time.Time{}, time.Time{}, false
		}
		if !dateOnly {
			parsed = parsed.AddDate(0, 0, -1)
		}
		if !parsed.Before(first) {
			last = parsed
		}
	}
	return first, last, true
}

// renderTimed fills the fields every timed row shares. The end appears
// only when the companion supplied one, and the event-local reading
// appears only when the event's own validated clock disagrees with home's
// at either end — mirroring the tool renderer's rule, bare-Z suppression
// included: a zone-less UTC instant is an older companion discarding the
// zone, not an event scheduled on UTC's clock.
func (s *CalendarSnapshot) renderTimed(event snapshotCalendarEvent, start, end time.Time, endOK bool) renderedCalendarEvent {
	rendered := renderedCalendarEvent{
		Title: event.Title, Calendar: event.Calendar,
		Start:    start.In(s.homeZone).Format(time.RFC3339),
		Location: event.Location,
	}
	if endOK {
		rendered.End = end.In(s.homeZone).Format(time.RFC3339)
	}

	awayStart, awayEnd, zoneName, diverges := s.eventLocalReading(event, start, end, endOK)
	if diverges {
		rendered.EventZone = zoneName
		rendered.EventLocalStart = awayStart.Format(time.RFC3339)
		if endOK {
			rendered.EventLocalEnd = awayEnd.Format(time.RFC3339)
		}
	}
	return rendered
}

// eventLocalReading resolves the event on its own clock and reports
// whether that reading differs from home at either end. The declared zone
// is used only when it validates — an unloadable name must not be
// published as event_zone, and must not defeat the bare-Z suppression the
// way falling through to the embedded offset would.
func (s *CalendarSnapshot) eventLocalReading(event snapshotCalendarEvent, start, end time.Time, endOK bool) (time.Time, time.Time, string, bool) {
	awayStart, awayEnd := start, end
	zoneName := ""
	if name := strings.TrimSpace(event.TimeZone); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			awayStart, awayEnd = start.In(loc), end.In(loc)
			zoneName = name
		}
	}
	if zoneName == "" {
		if _, offset := start.Zone(); offset == 0 {
			return time.Time{}, time.Time{}, "", false
		}
	}

	if offsetsMatch(awayStart, awayStart.In(s.homeZone)) &&
		(!endOK || offsetsMatch(awayEnd, awayEnd.In(s.homeZone))) {
		return time.Time{}, time.Time{}, "", false
	}
	return awayStart, awayEnd, zoneName, true
}

func offsetsMatch(a, b time.Time) bool {
	_, ao := a.Zone()
	_, bo := b.Zone()
	return ao == bo
}

type snapshotTimedEvent struct {
	event snapshotCalendarEvent
	start time.Time
	end   time.Time
	endOK bool
}

// parseSnapshotDate resolves an all-day boundary: the date form directly,
// and the legacy instant form (a pre-#49 companion's zone-discarded
// midnight) by rounding to the nearest UTC midnight — the same ±12h
// recovery the tool renderer uses. dateOnly distinguishes the two,
// because they carry different end conventions: a date-form end is
// already inclusive (the Mac resolved it), while an instant-form end is
// EventKit-exclusive and the day before it is the last one occupied.
func parseSnapshotDate(value string) (t time.Time, dateOnly, ok bool) {
	value = strings.TrimSpace(value)
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, true, true
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		rounded := t.UTC().Round(24 * time.Hour)
		return time.Date(rounded.Year(), rounded.Month(), rounded.Day(), 0, 0, 0, 0, time.UTC), false, true
	}
	return time.Time{}, false, false
}

// parseSnapshotSpan parses a timed event's boundaries. endOK reports
// whether the end is the companion's or a synthetic minute added so the
// active-now classification has an interval to test — the synthetic end
// is never rendered, because the ambient block must not state a time
// nobody scheduled.
func parseSnapshotSpan(event snapshotCalendarEvent) (start, end time.Time, endOK, ok bool) {
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(event.Start))
	if err != nil {
		return time.Time{}, time.Time{}, false, false
	}
	trimmedEnd := strings.TrimSpace(event.End)
	if trimmedEnd == "" {
		// A genuinely absent end gets the synthetic minute so active-now
		// has an interval to classify; the invention is never rendered.
		return start, start.Add(time.Minute), false, true
	}
	end, err = time.Parse(time.RFC3339, trimmedEnd)
	if err != nil {
		// A non-empty end that does not parse is drift, not omission —
		// classifying corrupted data as active would present it with
		// more confidence than the tool renderer, which marks the same
		// payload unparsed. Skip; the tool remains the loud surface.
		return time.Time{}, time.Time{}, false, false
	}
	endOK = end.After(start)
	if !endOK {
		end = start.Add(time.Minute)
	}
	return start, end, endOK, true
}

func sameHomeDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
