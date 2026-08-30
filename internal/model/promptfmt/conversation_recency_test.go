package promptfmt

import (
	"testing"
	"time"
)

func chicagoTime(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	return loc
}

// TestConversationRecency locks the full sentence for every rhythm
// branch and clause combination. The rendered prose IS the contract —
// the model reads these sentences verbatim — so the expectations are
// exact strings, not substring probes.
func TestConversationRecency(t *testing.T) {
	loc := chicagoTime(t)
	// Wednesday 2026-08-26 09:00 in the household zone.
	morning := time.Date(2026, 8, 26, 9, 0, 0, 0, loc)
	afternoon := time.Date(2026, 8, 26, 15, 0, 0, 0, loc)

	tests := []struct {
		name  string
		facts ConversationRecencyFacts
		now   time.Time
		want  string
	}{
		{
			name:  "first conversation ever",
			facts: ConversationRecencyFacts{Kind: TurnContact},
			now:   morning,
			want:  "This is the first conversation on this channel.",
		},
		{
			name: "history exists but contact timing unknown",
			facts: ConversationRecencyFacts{
				Kind:                 TurnContact,
				HasHistory:           true,
				PreviousSessionEnd:   morning.Add(-15 * time.Hour),
				PreviousSessionTopic: "the garage lighting circuits",
			},
			now:  morning,
			want: "The previous conversation was last night, about the garage lighting circuits.",
		},
		{
			name: "active conversation with session suppresses previous-conversation clause",
			facts: ConversationRecencyFacts{
				Kind:                 TurnContact,
				PreviousContactAt:    morning.Add(-2 * time.Minute),
				ActiveSessionStart:   morning.Add(-25 * time.Minute),
				PreviousSessionEnd:   morning.Add(-15 * time.Hour),
				PreviousSessionTopic: "the garage lighting circuits",
				HasHistory:           true,
			},
			now:  morning,
			want: "You're in an active conversation that's been going on for about 25 minutes; the previous message was about 2 minutes before this one.",
		},
		{
			name: "active conversation without session",
			facts: ConversationRecencyFacts{
				Kind:              TurnContact,
				PreviousContactAt: morning.Add(-time.Minute),
				HasHistory:        true,
			},
			now:  morning,
			want: "The conversation is continuing; the previous message was moments ago.",
		},
		{
			name: "lull",
			facts: ConversationRecencyFacts{
				Kind:              TurnContact,
				PreviousContactAt: morning.Add(-45 * time.Minute),
				HasHistory:        true,
			},
			now:  morning,
			want: "The conversation is resuming after a quiet stretch — the previous message was about 45 minutes ago.",
		},
		{
			name: "first message in hours, same day",
			facts: ConversationRecencyFacts{
				Kind:              TurnContact,
				PreviousContactAt: morning.Add(-6 * time.Hour),
				HasHistory:        true,
			},
			now:  morning,
			want: "This is the first message in about 6 hours.",
		},
		{
			name: "first contact today with previous conversation last night",
			facts: ConversationRecencyFacts{
				Kind:                 TurnContact,
				PreviousContactAt:    time.Date(2026, 8, 25, 22, 0, 0, 0, loc),
				PreviousSessionEnd:   time.Date(2026, 8, 25, 22, 15, 0, 0, loc),
				PreviousSessionTopic: "the garage lighting circuits",
				HasHistory:           true,
			},
			now:  morning,
			want: "This is the first contact today. The previous conversation was last night, about the garage lighting circuits.",
		},
		{
			name: "small-hours session this morning is last night, not earlier today",
			facts: ConversationRecencyFacts{
				Kind:               TurnContact,
				PreviousContactAt:  time.Date(2026, 8, 26, 2, 0, 0, 0, loc),
				PreviousSessionEnd: time.Date(2026, 8, 26, 2, 0, 0, 0, loc),
				HasHistory:         true,
			},
			now:  morning,
			want: "This is the first message in about 7 hours. The previous conversation was last night.",
		},
		{
			name: "previous conversation yesterday when it is already afternoon",
			facts: ConversationRecencyFacts{
				Kind:               TurnContact,
				PreviousContactAt:  time.Date(2026, 8, 25, 20, 0, 0, 0, loc),
				PreviousSessionEnd: time.Date(2026, 8, 25, 20, 0, 0, 0, loc),
				HasHistory:         true,
			},
			now:  afternoon,
			want: "This is the first contact today. The previous conversation was yesterday.",
		},
		{
			name: "previous conversation on a weekday",
			facts: ConversationRecencyFacts{
				Kind:                 TurnContact,
				PreviousContactAt:    morning.AddDate(0, 0, -4),
				PreviousSessionEnd:   morning.AddDate(0, 0, -4),
				PreviousSessionTopic: "NAS snapshot pruning",
				HasHistory:           true,
			},
			now:  morning,
			want: "This is the first contact today. The previous conversation was on Saturday, about NAS snapshot pruning.",
		},
		{
			name: "previous conversation a week ago",
			facts: ConversationRecencyFacts{
				Kind:               TurnContact,
				PreviousContactAt:  morning.AddDate(0, 0, -10),
				PreviousSessionEnd: morning.AddDate(0, 0, -10),
				HasHistory:         true,
			},
			now:  morning,
			want: "This is the first contact today. The previous conversation was a week ago.",
		},
		{
			name: "previous conversation on an absolute date",
			facts: ConversationRecencyFacts{
				Kind:               TurnContact,
				PreviousContactAt:  morning.AddDate(0, 0, -40),
				PreviousSessionEnd: morning.AddDate(0, 0, -40),
				HasHistory:         true,
			},
			now:  morning,
			want: "This is the first contact today. The previous conversation was on July 17.",
		},
		{
			name: "wake into a quiet channel with loop name",
			facts: ConversationRecencyFacts{
				Kind:              TurnWake,
				WakeLoopName:      "annunciator",
				PreviousContactAt: morning.Add(-3 * time.Hour),
				HasHistory:        true,
			},
			now:  morning,
			want: "This turn is an internal wake from the annunciator loop, not a message from them. The previous contact was about 3 hours ago.",
		},
		{
			name: "wake mid-conversation",
			facts: ConversationRecencyFacts{
				Kind:              TurnWake,
				PreviousContactAt: morning.Add(-2 * time.Minute),
				HasHistory:        true,
			},
			now:  morning,
			want: "You're in an active conversation (previous contact about 2 minutes ago), but this turn is an internal wake, not a new message — they haven't said anything new.",
		},
		{
			name:  "wake with no contact ever",
			facts: ConversationRecencyFacts{Kind: TurnWake},
			now:   morning,
			want:  "This turn is an internal wake; there has been no contact on this channel yet.",
		},
		{
			name: "wake with history but unknown contact timing",
			facts: ConversationRecencyFacts{
				Kind:               TurnWake,
				HasHistory:         true,
				PreviousSessionEnd: morning.AddDate(0, 0, -1),
			},
			now:  afternoon,
			want: "This turn is an internal wake, not a message from them. The previous conversation was yesterday.",
		},
		{
			name: "wake previous contact last night uses day word",
			facts: ConversationRecencyFacts{
				Kind:              TurnWake,
				PreviousContactAt: time.Date(2026, 8, 25, 22, 0, 0, 0, loc),
				HasHistory:        true,
			},
			now:  morning,
			want: "This turn is an internal wake, not a message from them. The previous contact was last night.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConversationRecency(tt.facts, tt.now, loc)
			if got != tt.want {
				t.Errorf("ConversationRecency:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestFuzzySpan pins the vocabulary boundaries the sentences compose
// from.
func TestFuzzySpan(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "moments"},
		{89 * time.Second, "moments"},
		{95 * time.Second, "about 2 minutes"},
		{45 * time.Minute, "about 45 minutes"},
		{70 * time.Minute, "about an hour"},
		{5 * time.Hour, "about 5 hours"},
		{26 * time.Hour, "about a day"},
		{3 * 24 * time.Hour, "about 3 days"},
	}
	for _, tt := range tests {
		if got := fuzzySpan(tt.d); got != tt.want {
			t.Errorf("fuzzySpan(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
