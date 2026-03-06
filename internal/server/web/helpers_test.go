package web

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/scheduler"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
		{"n equals 3", "hello", 3, "hel"},
		{"n less than 3", "hello", 2, "he"},
		{"n equals 1", "hello", 1, "h"},
		{"empty string", "", 5, ""},
		{"unicode preserved", "café latte", 6, "caf..."},
		{"unicode exact", "café", 4, "café"},
		{"unicode truncated mid", "日本語テスト", 5, "日本..."},
		{"emoji safe", "👋🌍🎉✨🔥", 4, "👋..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}

func TestConfidence(t *testing.T) {
	tests := []struct {
		name string
		f    float64
		want string
	}{
		{"zero", 0, "—"},
		{"negative", -0.1, "—"},
		{"half", 0.5, "50%"},
		{"full", 1.0, "100%"},
		{"typical", 0.85, "85%"},
		{"low", 0.123, "12%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := confidence(tt.f)
			if got != tt.want {
				t.Errorf("confidence(%v) = %q, want %q", tt.f, got, tt.want)
			}
		})
	}
}

func TestTimeAgo(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero time", time.Time{}, "—"},
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"minutes ago", now.Add(-15 * time.Minute), "15m ago"},
		{"hours ago", now.Add(-3 * time.Hour), "3h ago"},
		{"one day ago", now.Add(-24 * time.Hour), "1d ago"},
		{"several days ago", now.Add(-72 * time.Hour), "3d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timeAgo(tt.t)
			if got != tt.want {
				t.Errorf("timeAgo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDescribeSchedule(t *testing.T) {
	at := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	every := scheduler.Duration{Duration: 2 * time.Hour}

	tests := []struct {
		name     string
		schedule scheduler.Schedule
		want     string
	}{
		{
			"at with time",
			scheduler.Schedule{Kind: "at", At: &at},
			"once at 2026-03-15 14:30",
		},
		{
			"at without time",
			scheduler.Schedule{Kind: "at"},
			"once (no time set)",
		},
		{
			"every with interval",
			scheduler.Schedule{Kind: "every", Every: &every},
			"every 2h0m0s",
		},
		{
			"every without interval",
			scheduler.Schedule{Kind: "every"},
			"recurring (no interval set)",
		},
		{
			"cron with expression",
			scheduler.Schedule{Kind: "cron", Cron: "0 9 * * *"},
			"cron: 0 9 * * *",
		},
		{
			"cron without expression",
			scheduler.Schedule{Kind: "cron"},
			"cron (no expression)",
		},
		{
			"unknown kind",
			scheduler.Schedule{Kind: "custom"},
			"custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeSchedule(tt.schedule)
			if got != tt.want {
				t.Errorf("describeSchedule() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDescribeTrigger(t *testing.T) {
	afterTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		trigger scheduler.Trigger
		want    string
	}{
		{
			"empty trigger",
			scheduler.Trigger{},
			"—",
		},
		{
			"time only",
			scheduler.Trigger{AfterTime: &afterTime},
			"after 2026-06-01 12:00",
		},
		{
			"entity without state",
			scheduler.Trigger{EntityID: "person.dan"},
			"person.dan",
		},
		{
			"entity with state",
			scheduler.Trigger{EntityID: "person.dan", EntityState: "home"},
			"person.dan = home",
		},
		{
			"zone without action",
			scheduler.Trigger{Zone: "airport"},
			"zone:airport",
		},
		{
			"zone with action",
			scheduler.Trigger{Zone: "airport", ZoneAction: "enter"},
			"zone:airport (enter)",
		},
		{
			"event type",
			scheduler.Trigger{EventType: "presence_change"},
			"event:presence_change",
		},
		{
			"expression short",
			scheduler.Trigger{Expression: "temp > 80"},
			"expr:temp > 80",
		},
		{
			"expression truncated",
			scheduler.Trigger{Expression: "this is a very long expression that exceeds thirty characters"},
			"expr:this is a very long express...",
		},
		{
			"multiple fields combined",
			scheduler.Trigger{
				EntityID:    "sensor.temp",
				EntityState: "high",
				EventType:   "state_changed",
			},
			"sensor.temp = high; event:state_changed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeTrigger(tt.trigger)
			if got != tt.want {
				t.Errorf("describeTrigger() = %q, want %q", got, tt.want)
			}
		})
	}
}
