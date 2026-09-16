package documents

import (
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// bakedDeltaReportLimit caps how many findings a refusal or warning
// names. A document that has been rotting for weeks can carry dozens;
// listing them all buries the instruction that tells the author what
// to do instead. The count of the rest still ships, because silently
// truncating reads as "that was all of them".
const bakedDeltaReportLimit = 5

// BakedDeltaRefusedError refuses a write whose prose carries this
// project's own delta vocabulary. The rendered form is true at the
// instant it is written and false immediately after, which is the
// whole reason {{delta:...}} exists: the template re-renders on every
// read, so an authored document stays true between rewrites.
type BakedDeltaRefusedError struct {
	Ref      string
	Findings []promptfmt.BakedDelta
}

func (e *BakedDeltaRefusedError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"%s: this write bakes a rendered delta into stored prose (%s). A delta is true only at the moment it is written. "+
			"Write the absolute value wrapped in a template instead — {{delta:2026-09-18T14:30:00-05:00}} for an instant, "+
			"{{delta:2026-09-18}} for a date — and it re-renders correctly on every read. "+
			"To store the literal text (documenting the format, quoting tool output), put it in backticks or a fenced block, which are not scanned.",
		e.Ref, describeBakedDeltas(e.Findings))
}

// describeBakedDeltas renders findings as a quoted, line-numbered list,
// capped and counted.
func describeBakedDeltas(findings []promptfmt.BakedDelta) string {
	shown := findings
	if len(shown) > bakedDeltaReportLimit {
		shown = shown[:bakedDeltaReportLimit]
	}
	parts := make([]string, 0, len(shown))
	for _, f := range shown {
		parts = append(parts, fmt.Sprintf("%q on line %d", f.Text, f.Line))
	}
	out := strings.Join(parts, ", ")
	if rest := len(findings) - len(shown); rest > 0 {
		out += fmt.Sprintf(", and %d more", rest)
	}
	return out
}

// guardAuthoredProse inspects the text a single write introduces and
// decides what to do about relative times in it.
//
// It scans what this call supplies — never the merged document —
// because a document that already carries a baked delta must stay
// writable. Scanning the merged result would refuse every future write
// on account of prose the author did not touch, and the loop whose
// document rotted would be the one loop unable to repair it.
//
// It also sits above Write/Edit/JournalUpdate rather than at the file
// writer, which is what keeps Move, Copy and the section transfers out
// of scope: those relocate prose that already exists instead of
// authoring it, and refusing them would leave a rotted document
// permanently unmovable — unable to be filed, archived, or split apart
// on the way to being fixed.
//
// Tier 1 refuses: those shapes are this package's own formatter output,
// and prose almost always acquires one by transcribing a tool result
// verbatim. Tier 2 warns and lets the write through: "yesterday" decays
// exactly as fast, but it is ordinary English and a matcher cannot know
// whether it dates anything.
func guardAuthoredProse(ref string, texts ...string) ([]string, error) {
	var refuse, warn []promptfmt.BakedDelta
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		for _, finding := range promptfmt.FindBakedDeltas(text) {
			switch finding.Tier {
			case promptfmt.BakedDeltaFormatted:
				refuse = append(refuse, finding)
			case promptfmt.BakedDeltaNarrative:
				warn = append(warn, finding)
			}
		}
	}
	if len(refuse) > 0 {
		return nil, &BakedDeltaRefusedError{Ref: ref, Findings: refuse}
	}
	if len(warn) == 0 {
		return nil, nil
	}
	return []string{fmt.Sprintf(
		"%s: stored prose carries a relative time that will not stay true (%s). "+
			"Prefer {{delta:<absolute date or RFC3339 instant>}}, which re-renders on every read. Written as-is.",
		ref, describeBakedDeltas(warn))}, nil
}
