package messages

import (
	"strings"
	"testing"
)

func TestReactionEventPrompt(t *testing.T) {
	event := ReactionEvent{
		ChannelName:     "Signal",
		SenderID:        "+15551234567",
		SenderName:      "Alice",
		Emoji:           "❤️",
		TargetAuthor:    "+15559999999",
		TargetTimestamp: 1700000099000,
	}

	got := event.Prompt()
	for _, want := range []string{
		"Signal reaction from Alice (+15551234567)",
		"❤️",
		"[ts:1700000099000]",
		"+15559999999",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Prompt() = %q, missing %q", got, want)
		}
	}
}

func TestReactionEventHints(t *testing.T) {
	event := ReactionEvent{
		Emoji:           "👍",
		TargetTimestamp: 12345,
		Removed:         true,
	}

	hints := event.Hints()
	if hints["event_type"] != "reaction_removed" {
		t.Fatalf("event_type = %q, want reaction_removed", hints["event_type"])
	}
	if hints["reaction_emoji"] != "👍" {
		t.Fatalf("reaction_emoji = %q, want 👍", hints["reaction_emoji"])
	}
	if hints["target_sent_timestamp"] != "12345" {
		t.Fatalf("target_sent_timestamp = %q, want 12345", hints["target_sent_timestamp"])
	}
}
