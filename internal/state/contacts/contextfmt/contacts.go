// Package contextfmt renders contact records into compact, model-facing
// context. Callers build a [Match] slice from domain types (Contact,
// Property, similarity score) and call [Format] — the JSON projection is
// schema-stable across turns so the model can compare snapshots, sort by
// score, and key off trust zone without parsing prose.
//
// The package mirrors the shape of
// internal/integrations/homeassistant/contextfmt: the parent package
// (contacts) imports this subpackage to render, but this subpackage
// does not import its parent — Match is a value type built at the call
// site to avoid the cycle and keep the formatter trivially testable.
package contextfmt

import (
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// Match is one contact ready for rendering: contact-card fields plus the
// similarity score that selected it and any structured properties to
// surface. Score is a similarity in [0, 1]; the renderer emits it as a
// float so the model can sort or threshold without re-deriving it from
// prose.
type Match struct {
	Name       string     `json:"name"`
	Org        string     `json:"org,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	TrustZone  string     `json:"trust_zone,omitempty"`
	Score      float32    `json:"score"`
	Properties []Property `json:"properties,omitempty"`
}

// Property is one structured property of a contact. Kind is the property
// name — usually a vCard property (e.g. "EMAIL", "TEL", "URL", "IMPP"), but
// may be an app-specific key such as "timezone"; Type carries the vCard
// subtype when present (e.g. "INTERNET", "HOME"); Value is the property value.
type Property struct {
	Kind  string `json:"kind"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value"`
}

// Format renders matches as a heading-framed compact JSON projection
// suitable for the system prompt. Returns "" when no matches are given,
// so callers can decide whether to emit anything without parsing the
// JSON envelope. The heading is markdown (a section boundary); the
// payload is JSON (typed runtime data).
func Format(matches []Match) string {
	if len(matches) == 0 {
		return ""
	}
	envelope := struct {
		Contacts []Match `json:"contacts"`
	}{Contacts: matches}

	var sb strings.Builder
	sb.WriteString("### Relevant Contacts\n\n")
	sb.WriteString(promptfmt.MarshalCompact(envelope))
	return sb.String()
}
