// Package contextfmt renders knowledge facts into compact, model-facing
// context. Callers convert their domain Fact values into one of the
// view types here (SimilarityFact or SubjectFact) and call the matching
// Format function — the JSON projection is schema-stable across turns
// so the model can sort by score, filter by subjects, or chase a Ref
// into the KB without parsing prose.
//
// The package mirrors the shape of
// internal/integrations/homeassistant/contextfmt: the parent package
// (knowledge) imports this subpackage to render, but this subpackage
// does not import its parent — view types are values built at the call
// site to avoid the cycle and keep the formatter trivially testable.
package contextfmt

import (
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// SimilarityFact is one fact selected by semantic similarity search.
// Score is the cosine similarity in [0, 1]; the model can read it as a
// sortable number rather than the "(60% relevant)" prose the older
// renderer produced.
type SimilarityFact struct {
	Category string  `json:"category"`
	Key      string  `json:"key"`
	Value    string  `json:"value"`
	Score    float32 `json:"score"`
}

// SubjectFact is one fact retrieved by subject-key match. Subjects
// lists the keys this fact was indexed by (entity:foo, zone:bar,
// contact:dan@example.com). Ref is the canonical KB-document reference
// when present; the model can chase it for full details instead of
// relying on the short Value text.
type SubjectFact struct {
	Category string   `json:"category"`
	Key      string   `json:"key"`
	Value    string   `json:"value"`
	Subjects []string `json:"subjects,omitempty"`
	Ref      string   `json:"ref,omitempty"`
}

// FormatSimilarity renders similarity-matched facts as a heading-framed
// compact JSON projection. Returns "" when no facts are given.
func FormatSimilarity(facts []SimilarityFact) string {
	if len(facts) == 0 {
		return ""
	}
	envelope := struct {
		Facts []SimilarityFact `json:"facts"`
	}{Facts: facts}

	var sb strings.Builder
	sb.WriteString("### Relevant Facts\n\n")
	sb.WriteString(promptfmt.MarshalCompact(envelope))
	return sb.String()
}

// FormatSubjectKeyed renders subject-keyed facts as a heading-framed
// compact JSON projection. Returns "" when no facts are given.
func FormatSubjectKeyed(facts []SubjectFact) string {
	if len(facts) == 0 {
		return ""
	}
	envelope := struct {
		Facts []SubjectFact `json:"facts"`
	}{Facts: facts}

	var sb strings.Builder
	sb.WriteString("### Subject-Keyed Facts\n\n")
	sb.WriteString(promptfmt.MarshalCompact(envelope))
	return sb.String()
}
