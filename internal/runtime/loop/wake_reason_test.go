package loop

import (
	"fmt"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

func notifyEnvelope(kind, fromName string) messages.Envelope {
	return messages.Envelope{
		From:    messages.Identity{Name: fromName},
		Payload: messages.LoopNotifyPayload{Kind: kind},
	}
}

func TestResolveWakeAttribution(t *testing.T) {
	item := MailboxItem{ID: "signal:abc", EnqueuedAt: time.Now()}

	tests := []struct {
		name           string
		causes         []wakeCause
		signals        []messages.Envelope
		mailboxItems   []MailboxItem
		event          any
		interrupted    bool
		firstIteration bool
		eventDriven    bool
		want           WakeAttribution
	}{
		{
			name:         "manual loop_wake outranks everything",
			signals:      []messages.Envelope{notifyEnvelope("event_source", "subscription_wake"), notifyEnvelope("loop_wake", "core")},
			causes:       []wakeCause{{reason: WakeReasonMailbox, source: "signal"}},
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonManual, Source: "core"},
		},
		{
			name:    "event_source envelope classifies as subscription",
			signals: []messages.Envelope{notifyEnvelope("event_source", "mqtt_wake")},
			want:    WakeAttribution{Reason: WakeReasonSubscription, Source: "mqtt_wake"},
		},
		{
			name:    "ordinary notification names its sender",
			signals: []messages.Envelope{notifyEnvelope("core_attention", "ranch_climate_watch")},
			want:    WakeAttribution{Reason: WakeReasonNotify, Source: "ranch_climate_watch"},
		},
		{
			name:         "signals outrank mailbox content in the same wake",
			signals:      []messages.Envelope{notifyEnvelope("core_attention", "archivist")},
			causes:       []wakeCause{{reason: WakeReasonMailbox, source: "signal"}},
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonNotify, Source: "archivist"},
		},
		{
			name:  "waitfunc payload attributes as event",
			event: struct{ payload string }{"tick"},
			want:  WakeAttribution{Reason: WakeReasonEvent},
		},
		{
			name:    "notify poke sentinel is not an event",
			event:   notifyWakeEvent{},
			signals: []messages.Envelope{notifyEnvelope("core_attention", "poller")},
			want:    WakeAttribution{Reason: WakeReasonNotify, Source: "poller"},
		},
		{
			name:         "fresh mailbox cause carries the producer label",
			causes:       []wakeCause{{reason: WakeReasonMailbox, source: "signal"}},
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonMailbox, Source: "signal"},
		},
		{
			name:         "fresh enqueue outranks a retained-row retry",
			causes:       []wakeCause{{reason: WakeReasonMailboxRetry, source: "retained"}, {reason: WakeReasonMailbox, source: "signal"}},
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonMailbox, Source: "signal"},
		},
		{
			name:         "retry-only causes attribute as mailbox_retry",
			causes:       []wakeCause{{reason: WakeReasonMailboxRetry, source: "retained"}},
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonMailboxRetry, Source: "retained"},
		},
		{
			name:         "items with no recorded cause still read as mailbox",
			mailboxItems: []MailboxItem{item},
			want:         WakeAttribution{Reason: WakeReasonMailbox},
		},
		{
			name:   "mailbox cause without drained items cannot claim the wake",
			causes: []wakeCause{{reason: WakeReasonMailbox, source: "signal"}},
			want:   WakeAttribution{Reason: WakeReasonTimer},
		},
		{
			name:   "retune-overdue return attributes as retune",
			causes: []wakeCause{{reason: WakeReasonRetune}},
			want:   WakeAttribution{Reason: WakeReasonRetune},
		},
		{
			name:        "interrupted sleep with nothing drained is an unexplained notify",
			interrupted: true,
			want:        WakeAttribution{Reason: WakeReasonNotify, Source: "unknown"},
		},
		{
			name:        "event-driven wake with nothing drained is an unexplained notify",
			eventDriven: true,
			want:        WakeAttribution{Reason: WakeReasonNotify, Source: "unknown"},
		},
		{
			name:           "first timer iteration after boot is startup",
			firstIteration: true,
			want:           WakeAttribution{Reason: WakeReasonStartup},
		},
		{
			name: "unremarkable wake is the timer",
			want: WakeAttribution{Reason: WakeReasonTimer},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveWakeAttribution(tt.causes, tt.signals, tt.mailboxItems, tt.event, tt.interrupted, tt.firstIteration, tt.eventDriven)
			if got != tt.want {
				t.Errorf("resolveWakeAttribution = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestBeginIterationWakeDrainsCausesAndStampsState covers the one
// critical section: recorded causes are consumed exactly once, the
// attribution lands on lastWakeAttr, and the wake ring records the
// resolved reason.
func TestBeginIterationWakeDrainsCausesAndStampsState(t *testing.T) {
	l := &Loop{}
	l.mu.Lock()
	l.recordWakeCauseLocked(wakeCause{reason: WakeReasonMailbox, source: "signal"})
	l.mu.Unlock()

	at := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	attr := l.beginIterationWake(at, nil, []MailboxItem{{ID: "signal:1"}}, nil, false, false)
	if want := (WakeAttribution{Reason: WakeReasonMailbox, Source: "signal"}); attr != want {
		t.Fatalf("attribution = %+v, want %+v", attr, want)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastWakeAttr != attr {
		t.Errorf("lastWakeAttr = %+v, want %+v", l.lastWakeAttr, attr)
	}
	if len(l.pendingWakeCauses) != 0 {
		t.Errorf("pending causes not drained: %+v", l.pendingWakeCauses)
	}
	if len(l.wakeHistory) != 1 || l.wakeHistory[0].reason != WakeReasonMailbox {
		t.Errorf("wake ring = %+v, want one mailbox record", l.wakeHistory)
	}
}

// TestWakeCauseCapDropsNewest: the first cause after a drain is the one
// that actually woke the loop, so overflow keeps the oldest. Every
// inserted cause carries a distinct source so a regression that drops
// the oldest instead of the newest cannot slip past the assertions.
func TestWakeCauseCapDropsNewest(t *testing.T) {
	l := &Loop{}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := 0; i < maxPendingWakeCauses+3; i++ {
		l.recordWakeCauseLocked(wakeCause{reason: WakeReasonMailbox, source: fmt.Sprintf("cause-%d", i)})
	}
	if len(l.pendingWakeCauses) != maxPendingWakeCauses {
		t.Fatalf("retained %d causes, want the cap %d", len(l.pendingWakeCauses), maxPendingWakeCauses)
	}
	if got := l.pendingWakeCauses[0].source; got != "cause-0" {
		t.Errorf("oldest cause = %q, want cause-0 retained", got)
	}
	wantNewest := fmt.Sprintf("cause-%d", maxPendingWakeCauses-1)
	if got := l.pendingWakeCauses[len(l.pendingWakeCauses)-1].source; got != wantNewest {
		t.Errorf("newest retained cause = %q, want %s (overflow past the cap dropped)", got, wantNewest)
	}
}
