package documents

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// unchangedWriteWarning tells the author that a write changed nothing: what
// it sent matched the stored document byte for byte, so no revision was
// created, and the document still reads as last changed when it last did.
// The write succeeds and every counter reads it as a success, so a loop
// shown its own last publish can resend it for as long as nothing else
// breaks the habit; this is where the author learns it did.
func unchangedWriteWarning(existing *DocumentRecord, now time.Time) string {
	warning := "unchanged: everything you sent matches the stored document byte for byte, so no new revision was created"
	if updated, err := database.ParseTimestamp(firstValue(existing.Frontmatter, "updated")); err == nil {
		warning += " and it still reads as last changed " + promptfmt.FormatDeltaOnly(updated, now)
	}
	return warning + ". That is right only when nothing you have observed since then changes what it says. Text carried over from the copy you were shown is not a fresh judgment: compose from what you read this time."
}
