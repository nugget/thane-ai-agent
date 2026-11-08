package facets

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateSizesTheLeverToTheOverage pins the two repairs an
// over-budget projection can be offered, in the unit of the limit it broke.
// A gap within a tenth of the limit, or within 30 characters or five words
// on a short one, closes by rewording; a larger one rarely does, and saying
// "tighten it" there is what sent a publish loop round the same rejection
// until the loop guard stopped it.
func TestValidateSizesTheLeverToTheOverage(t *testing.T) {
	t.Parallel()

	const (
		rewordLever = "reword to cut at least"
		removeLever = "remove whole items"
	)
	valid := Payload{StatusLine: "Current.", Teaser: "Open for the hook.", Digest: "Enough to act on.", Full: "Complete detail."}
	tests := []struct {
		name       string
		mutate     func(*Payload)
		want       []string
		wantAbsent []string
	}{
		{
			name:       "one word over is rewordable",
			mutate:     func(p *Payload) { p.StatusLine = wordsOf(statusLineMaxWords+1, "s") },
			want:       []string{"status_line is 21 words", "the limit is 20", "(1 over)", rewordLever + " 1 word —"},
			wantAbsent: []string{removeLever, "characters"},
		},
		{
			// A tenth of status_line's word limit is 2; the five-word floor
			// keeps a one-line projection rewordable past that.
			name:       "five words over a status line is still rewordable",
			mutate:     func(p *Payload) { p.StatusLine = wordsOf(statusLineMaxWords+5, "s") },
			want:       []string{"status_line is 25 words", "(5 over)", rewordLever + " 5 words"},
			wantAbsent: []string{removeLever},
		},
		{
			name:   "just past the word floor needs whole items removed",
			mutate: func(p *Payload) { p.StatusLine = wordsOf(statusLineMaxWords+6, "s") },
			want: []string{
				"status_line is 26 words", "the limit is 20", "(6 over)",
				"rewording alone rarely closes a gap this large",
				removeLever + " totalling at least 6 words",
				"resolved, superseded, or already said elsewhere leaves this field entirely",
			},
			wantAbsent: []string{rewordLever},
		},
		{
			// Within its words, a status line can still run past the
			// character backstop on long tokens; that refusal counts
			// characters, the unit it enforced.
			name:       "one character past the status line backstop",
			mutate:     func(p *Payload) { p.StatusLine = strings.Repeat("s", statusLineMaxRunes+1) },
			want:       []string{"status_line is 181 characters", "the limit is 180", "(1 over)", rewordLever + " 1 character —"},
			wantAbsent: []string{removeLever, "words"},
		},
		{
			// The 30-character floor now governs only the backstop, where
			// a tenth of the limit is 18.
			name:       "thirty characters past the backstop is still rewordable",
			mutate:     func(p *Payload) { p.StatusLine = strings.Repeat("s", statusLineMaxRunes+30) },
			want:       []string{"status_line is 210 characters", "(30 over)", rewordLever + " 30 characters"},
			wantAbsent: []string{removeLever},
		},
		{
			name:       "just past the character floor needs whole items removed",
			mutate:     func(p *Payload) { p.StatusLine = strings.Repeat("s", statusLineMaxRunes+31) },
			want:       []string{"status_line is 211 characters", "(31 over)", removeLever + " totalling at least 31 characters"},
			wantAbsent: []string{rewordLever},
		},
		{
			// Cutting words is the repair a writer can count, and naming
			// the backstop beside it would hand back a character target,
			// so the cut is sized to clear both: 4 of these words, not 2.
			name:       "over both status line limits names only its words, cut to clear both",
			mutate:     func(p *Payload) { p.StatusLine = wordsOf(statusLineMaxWords+2, "ninechars") },
			want:       []string{"status_line is 22 words and the limit is 20 (2 over), and its words run long", rewordLever + " 4 words"},
			wantAbsent: []string{"characters", "180"},
		},
		{
			name:       "teaser at its tier edge rewords",
			mutate:     func(p *Payload) { p.Teaser = strings.Repeat("t", teaserMaxRunes+50) },
			want:       []string{"teaser is 550 characters", "the limit is 500", "(50 over)", rewordLever + " 50 characters"},
			wantAbsent: []string{removeLever},
		},
		{
			name:       "digest at its tier edge rewords",
			mutate:     func(p *Payload) { p.Digest = strings.Repeat("d", digestMaxRunes+204) },
			want:       []string{"digest is 2252 characters", "the limit is 2048", "(204 over)", rewordLever + " 204 characters"},
			wantAbsent: []string{removeLever},
		},
		{
			name:       "digest one past its tier edge removes items",
			mutate:     func(p *Payload) { p.Digest = strings.Repeat("d", digestMaxRunes+205) },
			want:       []string{"digest is 2253 characters", "(205 over)", removeLever + " totalling at least 205 characters"},
			wantAbsent: []string{rewordLever},
		},
		{
			name:       "digest far over removes items",
			mutate:     func(p *Payload) { p.Digest = strings.Repeat("d", 3100) },
			want:       []string{"digest is 3100 characters", "(1052 over)", "rewording alone rarely closes", removeLever},
			wantAbsent: []string{rewordLever},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := valid
			tt.mutate(&payload)
			err := DefaultContract().Validate(payload)
			if err == nil {
				t.Fatal("Validate() accepted an over-budget projection")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error = %q, want it to contain %q", err, want)
				}
			}
			// No write path truncates, so the error must never suggest one does.
			for _, absent := range append(tt.wantAbsent, "truncat") {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("Validate() error = %q, must not contain %q", err, absent)
				}
			}
		})
	}
}

// TestInvalidProjectionsErrorFramesOneCompleteCorrection pins the frame every
// faceted writer wraps its violations in: nothing landed, every listed field
// is corrected in one call, and no retry count stands in for that.
func TestInvalidProjectionsErrorFramesOneCompleteCorrection(t *testing.T) {
	t.Parallel()

	first := errors.New("status_line is 121 characters and the limit is 120 (1 over)")
	second := errors.New("digest is required")
	err := InvalidProjectionsError("contact dossier projections", first, second)

	for _, want := range []string{
		"contact dossier projections are invalid and nothing was written",
		"correct every listed field in your next call",
		"a listed field resent unchanged is refused the same way",
		first.Error(),
		second.Error(),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("InvalidProjectionsError() = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "retry once") {
		t.Errorf("InvalidProjectionsError() = %q, must not cap retries", err)
	}
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("InvalidProjectionsError() does not wrap every violation: %v", err)
	}
}

// TestDigestGuidanceTeachesBoundedCurrentState pins the write-side half of
// the budget contract: a digest that accumulates "resolved" annotations is
// the value that outgrows its budget, so the guidance every faceted writer's
// schema renders says items leave rather than being marked.
func TestDigestGuidanceTeachesBoundedCurrentState(t *testing.T) {
	t.Parallel()

	field, ok := FieldByKey(string(Digest))
	if !ok {
		t.Fatal("digest field not found")
	}
	for _, want := range []string{
		"bounded current state",
		"rewritten whole on every write rather than appended to",
		"remove an item once it is resolved or superseded instead of marking it resolved",
	} {
		if !strings.Contains(field.Guidance, want) {
			t.Errorf("digest guidance = %q, want it to contain %q", field.Guidance, want)
		}
	}
}
