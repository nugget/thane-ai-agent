package talents

import (
	"strings"
	"testing"
)

// talentTextByName returns the named in-tree talents with whitespace
// collapsed, so a phrase check does not depend on where the markdown
// wraps. A name that does not load fails the test: a pin on a talent
// that is not there would pass by checking nothing.
func talentTextByName(t *testing.T, names ...string) map[string]string {
	t.Helper()
	all := make(map[string]string)
	for _, talent := range loadRepoTalents(t) {
		all[talent.Name] = strings.Join(strings.Fields(talent.Content), " ")
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		text, ok := all[name]
		if !ok {
			t.Fatalf("talent %q not loaded; the pin would be meaningless", name)
		}
		out[name] = text
	}
	return out
}

// TestSignalTalentTeachesWakeHold pins how a Signal loop-notification
// wake ends without messaging the person on the thread: an explicit
// signal_hold_reply call, never an empty final text. The empty ending
// cannot express silence — the engine nudges it and sends what the model
// writes next — so a talent that taught it would route the model back
// into the placeholder it replaced. It also pins the border sentence in
// the loops doctrine that keeps the hold apart from the loop_wake reply.
func TestSignalTalentTeachesWakeHold(t *testing.T) {
	text := talentTextByName(t, "signal", "loops")
	present := []struct {
		name   string
		talent string
		want   string
	}{
		{"wake final text is sent verbatim", "signal", "your final text is sent to them word for word, even a note to yourself"},
		{"hold sends nothing whatever the text", "signal", "call `signal_hold_reply` with your reason, and the bridge sends nothing, whatever the final text says"},
		{"empty is not a hold", "signal", "Leaving the final text empty is not a hold: the runtime asks you to respond, and what you write then is sent."},
		{"offered only on wake turns", "signal", "The hold is offered only on those wake turns, because every other Signal turn answers something they sent."},
		{"hold does not answer the requester", "signal", "Holding the Signal message does not answer the loop that woke you; that reply still goes through `loop_wake`."},
		{"situation table row", "signal", "| A loop woke you and nothing should reach them right now | `signal_hold_reply` with your reason — otherwise your final text is sent |"},
		{"cleanest test names the hold", "signal", "If a loop woke you and the right thing to send is nothing, `signal_hold_reply` is."},
		{"loops border keeps the decisions apart", "loops", "whether the person there hears anything is a separate decision: your final text is sent to them unless you hold it with `signal_hold_reply`, and neither the message nor the hold answers the requester."},
	}
	for _, tt := range present {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(text[tt.talent], tt.want) {
				t.Errorf("talent %s must contain %q", tt.talent, tt.want)
			}
		})
	}

	// No talent may teach the empty ending as a way to stay quiet.
	absent := []struct {
		name string
		gone string
	}{
		{"the old wake prompt's empty instruction", "leave it empty"},
		{"the older wake wording", "leave the final response empty"},
		{"empty text as the no-message path", "leave the final text empty"},
		{"empty response as the no-message path", "empty final response"},
	}
	for _, tt := range absent {
		t.Run(tt.name, func(t *testing.T) {
			for _, talent := range loadRepoTalents(t) {
				body := strings.ToLower(strings.Join(strings.Fields(talent.Content), " "))
				if strings.Contains(body, tt.gone) {
					t.Errorf("talent %s still contains %q", talent.Name, tt.gone)
				}
			}
		})
	}
}
