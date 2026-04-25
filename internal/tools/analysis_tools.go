package tools

import (
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
)

// SetMediaAnalysisTools adds the media analysis persistence tool to the
// registry. The tool lets the agent save structured analysis to an
// Obsidian-compatible vault and track engagement.
func (r *Registry) SetMediaAnalysisTools(at *media.AnalysisTools) {
	r.registerMediaAnalysisTools(at)
}

func (r *Registry) registerMediaAnalysisTools(at *media.AnalysisTools) {
	if at == nil {
		return
	}

	r.Register(&Tool{
		Name:        "media_save_analysis",
		Description: "Save a structured media analysis to the knowledge vault. Writes a markdown file with YAML frontmatter to the configured output directory and records the engagement for future reference. Use after analyzing content from media_transcript.",
		Parameters:  media.SaveDefinition(),
		Handler:     at.SaveHandler(),
	})
}
