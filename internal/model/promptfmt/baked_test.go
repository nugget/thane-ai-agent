package promptfmt

import (
	"strings"
	"testing"
)

// TestFindBakedDeltasTiers pins what each tier claims. Tier 1 is this
// package's own output shape and gets refused by callers, so its
// boundary rules carry the weight: a false positive there blocks a
// legitimate write.
func TestFindBakedDeltasTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want []BakedDelta
	}{
		// The shapes the formatters actually emit, branch by branch.
		{"seconds", "left -3247s", []BakedDelta{{Text: "-3247s", Tier: BakedDeltaFormatted, Line: 1}}},
		{"hours and minutes", "away -26h45m", []BakedDelta{{Text: "-26h45m", Tier: BakedDeltaFormatted, Line: 1}}},
		{"days and hours", "due +5d9h", []BakedDelta{{Text: "+5d9h", Tier: BakedDeltaFormatted, Line: 1}}},
		{"whole days", "opens +20d", []BakedDelta{{Text: "+20d", Tier: BakedDeltaFormatted, Line: 1}}},
		{"zero", "stamped -0s", []BakedDelta{{Text: "-0s", Tier: BakedDeltaFormatted, Line: 1}}},

		// A bullet's own dash is not a sign, and the delta after it is.
		{"inside a bullet", "- state home since -2h2m", []BakedDelta{{Text: "-2h2m", Tier: BakedDeltaFormatted, Line: 1}}},

		// Boundary rules. The corpus this guards is full of coordinates,
		// so a longitude must never read as a delta.
		{"longitude", "at 29.797003, -98.41852298892312 now", nil},
		{"signed integer", "delta of -98 units", nil},
		{"offset in a zone name", "recorded UTC-5 sharp", nil},
		// A valid delta glued to the end of an identifier is the case
		// the look-behind exists for: every other guard passes it.
		{"suffix on an identifier", "the retention-30d policy applies", nil},
		{"countdown notation", "at T-5m we abort", nil},
		// Boundaries are runes, not ASCII bytes: an accented identifier
		// must be exempt exactly as its plain-ASCII twin is.
		{"non-ASCII identifier", "the café-1h special", nil},
		{"CJK identifier", "the 保持-30d policy", nil},
		{"spelled-out unit", "waited +3days", nil},
		{"abbreviated unit", "waited -5min", nil},
		{"sign with no terms", "a - b and c + d", nil},
		{"unknown unit", "ran -5k", nil},

		// Natural language: stale in exactly the same way, but only a
		// warning, because English is not proof of dating.
		{"counted ago", "the panel cleared 21 minutes ago", []BakedDelta{{Text: "21 minutes ago", Tier: BakedDeltaNarrative, Line: 1}}},
		{"worded ago", "rebooted a few hours ago", []BakedDelta{{Text: "a few hours ago", Tier: BakedDeltaNarrative, Line: 1}}},
		{"forward looking", "lands in three days", []BakedDelta{{Text: "in three days", Tier: BakedDeltaNarrative, Line: 1}}},
		{"day word", "verified yesterday", []BakedDelta{{Text: "yesterday", Tier: BakedDeltaNarrative, Line: 1}}},
		{"named period", "shipped last week", []BakedDelta{{Text: "last week", Tier: BakedDeltaNarrative, Line: 1}}},
		{"time of day", "installed this morning", []BakedDelta{{Text: "this morning", Tier: BakedDeltaNarrative, Line: 1}}},

		// Currency words name no distance, so they cannot decay into a
		// false statement and are deliberately left alone.
		{"vague currency", "the panel is currently clean and was recently rebuilt", nil},
		{"absolute date", "verified on 2026-09-06 at 11:13 CDT", nil},
		{"template is the fix, not the defect", "opens {{delta:2026-09-08}}", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FindBakedDeltas(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("FindBakedDeltas(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("finding %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestFindBakedDeltasSkipsNonProseRegions pins the one structural rule
// that makes the guard usable. All three exemptions were found by
// scanning the production corpus: frontmatter carries writer-stamped
// created_delta/updated_delta, documentation of the delta format is
// written in backticks, and a faceted document stores a JSON facet as
// a fenced block — so JSON needs no special case of its own.
func TestFindBakedDeltasSkipsNonProseRegions(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"---",
		`created_delta: "-747402s"`,
		`updated_delta: "-0s"`,
		"---",
		"",
		"## Digest",
		"",
		"```json",
		`{"room_since": "-513s", "state": "home"}`,
		"```",
		"",
		"Write `(-127s)` rather than a bare phrase.",
		"",
		"```",
		"Expected: [-47s] user: what time is it",
		"```",
		"",
		"- room Office since -513s",
	}, "\n")

	got := FindBakedDeltas(body)
	if len(got) != 1 {
		t.Fatalf("want exactly the prose finding, got %+v", got)
	}
	if got[0].Text != "-513s" || got[0].Tier != BakedDeltaFormatted {
		t.Errorf("finding = %+v, want the prose -513s as tier 1", got[0])
	}
	if got[0].Line != 18 {
		t.Errorf("line = %d, want 18 — line numbers must survive every skipped region", got[0].Line)
	}
}

// TestFindBakedDeltasUnterminatedFrontmatterIsProse pins the bypass this
// guard would otherwise hand any author: a body whose first line is a
// thematic break rather than an envelope. Skipping to EOF looking for a
// delimiter that never comes would make "---" at the top of a page a
// way to write anything at all past the guard.
func TestFindBakedDeltasUnterminatedFrontmatterIsProse(t *testing.T) {
	t.Parallel()

	got := FindBakedDeltas("---\narrived -2h\n")
	if len(got) != 1 || got[0].Text != "-2h" {
		t.Errorf("FindBakedDeltas = %+v, want the delta scanned as prose", got)
	}
}

// TestFindBakedDeltasFenceMarkersDoNotCross pins CommonMark's rule that
// a fence closes only with the character it opened with. A "```" line
// inside a "~~~" block is content, and treating it as a close drops the
// rest of that block back into the scan — refusing a write over a
// documented example.
func TestFindBakedDeltasFenceMarkersDoNotCross(t *testing.T) {
	t.Parallel()

	body := "~~~\n```\n-2h\n~~~\nafter -3h\n"
	got := FindBakedDeltas(body)
	if len(got) != 1 {
		t.Fatalf("want only the prose after the block, got %+v", got)
	}
	if got[0].Text != "-3h" || got[0].Line != 5 {
		t.Errorf("finding = %+v, want -3h on line 5", got[0])
	}
}

// TestFindBakedDeltasMultiBacktickSpans pins the escape hatch the
// refusal message promises. CommonMark delimits a span with equal-length
// backtick runs, so a doubled span is code exactly as a single one is;
// pairing individual backticks would expose the inner text and refuse a
// write over the very escape the message offered.
func TestFindBakedDeltasMultiBacktickSpans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int
	}{
		{"single", "write `-2h` here", 0},
		{"doubled", "write ``-2h`` here", 0},
		{"tripled around a tick", "write ```-2h``` here", 0},
		{"doubled span containing a single tick", "write ``a ` -2h`` here", 0},
		{"unmatched width leaves prose", "write ``-2h` here", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := FindBakedDeltas(tc.body); len(got) != tc.want {
				t.Errorf("FindBakedDeltas(%q) = %+v, want %d finding(s)", tc.body, got, tc.want)
			}
		})
	}
}

// TestFindBakedDeltasVocabularyIsWiderThanFormatterOutput pins a
// deliberate choice. The matcher confirms candidates with the parser
// ParseTimeOrDelta uses, not the narrower set FormatDeltaOnly emits:
// "-30m" and "-2w" never come off a formatter, but a model that writes
// either has still hardcoded a time that decays.
func TestFindBakedDeltasVocabularyIsWiderThanFormatterOutput(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"waited -30m", "waited -2w", "stamped +0s"} {
		got := FindBakedDeltas(body)
		if len(got) != 1 || got[0].Tier != BakedDeltaFormatted {
			t.Errorf("FindBakedDeltas(%q) = %+v, want one tier-1 finding", body, got)
		}
	}
}

// TestFindBakedDeltasStrayBacktickDoesNotBlindTheLine pins the failure
// mode of treating an unpaired tick as an opening: everything after it
// would stop being scanned, and the guard would go quiet on exactly the
// sloppily-written prose most likely to carry a baked delta.
func TestFindBakedDeltasStrayBacktickDoesNotBlindTheLine(t *testing.T) {
	t.Parallel()

	got := FindBakedDeltas("the `person.nugget state is home since -2h2m")
	if len(got) != 1 || got[0].Text != "-2h2m" {
		t.Errorf("FindBakedDeltas = %+v, want the delta after the stray tick", got)
	}
}

// TestFindBakedDeltasFencesReopen pins that a fence closes: prose after
// a code block is prose again.
func TestFindBakedDeltasFencesReopen(t *testing.T) {
	t.Parallel()

	body := "before -1h\n```\n-2h\n```\nafter -3h\n"
	got := FindBakedDeltas(body)
	if len(got) != 2 {
		t.Fatalf("want the two prose findings, got %+v", got)
	}
	if got[0].Text != "-1h" || got[0].Line != 1 {
		t.Errorf("first = %+v, want -1h on line 1", got[0])
	}
	if got[1].Text != "-3h" || got[1].Line != 5 {
		t.Errorf("second = %+v, want -3h on line 5", got[1])
	}
}

// TestFindBakedDeltasCleanProse pins the common case: prose with
// nothing to report allocates nothing.
func TestFindBakedDeltasCleanProse(t *testing.T) {
	t.Parallel()

	if got := FindBakedDeltas("Home since 12:06 CDT; appointment was 11:30-12:00.\n"); got != nil {
		t.Errorf("clean prose produced %+v, want nil", got)
	}
}
