package documents

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// unchangedWriteWarning renders [prompts.UnchangedWrite] for a write that
// matched existing byte for byte, naming how long ago the document last
// changed when its frontmatter records that.
func unchangedWriteWarning(existing *DocumentRecord, now time.Time) string {
	var lastChanged string
	if updated, err := database.ParseTimestamp(firstValue(existing.Frontmatter, "updated")); err == nil {
		lastChanged = promptfmt.FormatDeltaOnly(updated, now)
	}
	return prompts.UnchangedWrite(lastChanged)
}
