package prompts

import (
	"strings"
	"testing"
)

// TestCoreAttentionSignalWakeInstruction pins the Signal wake prompt's
// two decisions. The loop_wake reply duty stays whole, and the Signal
// side names signal_hold_reply as the only way to end without messaging
// the person. It must not ask for an empty final response, which the
// engine nudges and then fills, or say "silence", which echoes the
// reply contract rendered beside it ("Silence is indistinguishable from
// a dropped thread") and is the word the delivered placeholder used.
func TestCoreAttentionSignalWakeInstruction(t *testing.T) {
	present := []struct {
		name string
		want string
	}{
		{"loop_wake reply is the work", "reaching it and sending it back to the requester with loop_wake (its reply_to.loop_id) is the work of this turn, not an optional courtesy"},
		{"Signal side is a separate decision", "Whether anything reaches the person on this Signal thread is a separate decision, and yours to make."},
		{"final text is delivered verbatim", "Your final response text is delivered to them as a Signal message, word for word, so a note to yourself or a stage direction arrives as a message too."},
		{"hold names the tool and a reason", "When nothing should reach them right now, call signal_hold_reply with your reason"},
		{"hold is the only quiet ending", "it is the only way this turn ends without messaging them"},
		{"hold never answers the requester", "Holding the Signal message never stands in for a reply a requesting loop is owed."},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(coreAttentionSignalWakeInstruction, tt.want) {
				t.Errorf("wake instruction must contain %q\ngot: %s", tt.want, coreAttentionSignalWakeInstruction)
			}
		})
	}

	lower := strings.ToLower(coreAttentionSignalWakeInstruction)
	for _, gone := range []string{"leave it empty", "empty final response", "silence"} {
		if strings.Contains(lower, gone) {
			t.Errorf("wake instruction still contains %q\ngot: %s", gone, coreAttentionSignalWakeInstruction)
		}
	}
}

func TestCoreAttentionSignalWakePrompt(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{name: "no notification, no prompt", summary: " \n", want: ""},
		{name: "instruction then summary", summary: "  concern: the reading looks wrong\n", want: coreAttentionSignalWakeInstruction + "\n\nconcern: the reading looks wrong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CoreAttentionSignalWakePrompt(tt.summary); got != tt.want {
				t.Errorf("CoreAttentionSignalWakePrompt(%q) = %q, want %q", tt.summary, got, tt.want)
			}
		})
	}
}
