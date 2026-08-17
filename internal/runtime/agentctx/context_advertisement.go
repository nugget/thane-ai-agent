package agentctx

import (
	"fmt"
	"math"
	"strings"
)

// ContextProjectionRole describes how a projection participates in context
// selection. The role is a consumer contract, not a relevance score: the
// final discriminator combines it with request-relative match signals and
// its own limits.
type ContextProjectionRole string

const (
	// ContextRoleSignal is a compact outward-facing reason to spend more
	// attention on an item. A document's signal and teaser are both
	// signal-shaped: ambient-versus-request relevance belongs to match
	// evidence, not to two different consumer roles.
	ContextRoleSignal ContextProjectionRole = "signal"

	// ContextRoleContext is a standalone payload with enough substance to
	// use directly in a turn. A digest is the canonical example.
	ContextRoleContext ContextProjectionRole = "context"

	// ContextRoleDetail is the complete source material. Automatic context
	// selection does not choose detail; it is reached through an explicit
	// read or a later escalation path.
	ContextRoleDetail ContextProjectionRole = "detail"
)

// Valid reports whether r is a recognized context projection role.
func (r ContextProjectionRole) Valid() bool {
	switch r {
	case ContextRoleSignal, ContextRoleContext, ContextRoleDetail:
		return true
	default:
		return false
	}
}

// ContextMatchKind names one kind of request-relative evidence offered by a
// context subsystem. Providers perform their domain-specific matching; the
// discriminator owns the shared ordering between evidence kinds.
type ContextMatchKind string

const (
	// ContextMatchExactSubject means the request names the advertised subject
	// directly using its canonical identity.
	ContextMatchExactSubject ContextMatchKind = "exact_subject"

	// ContextMatchAlias means the request names a known alias for the
	// advertised subject.
	ContextMatchAlias ContextMatchKind = "alias"

	// ContextMatchSemantic means domain retrieval found a semantic match.
	ContextMatchSemantic ContextMatchKind = "semantic"

	// ContextMatchLexical means domain retrieval found a token or full-text
	// match.
	ContextMatchLexical ContextMatchKind = "lexical"

	// ContextMatchAmbient means the item is useful without matching the
	// request, subject to the discriminator's ambient budget.
	ContextMatchAmbient ContextMatchKind = "ambient"
)

// Valid reports whether k is a recognized context match kind.
func (k ContextMatchKind) Valid() bool {
	switch k {
	case ContextMatchExactSubject, ContextMatchAlias, ContextMatchSemantic, ContextMatchLexical, ContextMatchAmbient:
		return true
	default:
		return false
	}
}

// ContextMatchSignal is provider-owned evidence that an advertisement fits
// the current request. Strength is meaningful only within one evidence kind;
// the discriminator decides how kinds compare.
type ContextMatchSignal struct {
	Kind     ContextMatchKind `json:"kind"`
	Strength float64          `json:"strength"`
}

// ContextProjection describes one bounded representation that an advertiser
// can materialize if selected. EstimatedBytes includes any framing the
// materializer adds and lets the discriminator choose a projection without
// first paying to render it.
type ContextProjection struct {
	Name           string                `json:"name"`
	Role           ContextProjectionRole `json:"role"`
	Format         string                `json:"format"`
	EstimatedBytes int                   `json:"estimated_bytes"`
}

// ContextAdvertisement is a cheap, structured offer of model context. It
// identifies the source, records why it matches this request, and lists the
// projections that can be materialized. It intentionally carries no global
// rank or injection flag; those are consumer policy owned by the final
// discriminator.
type ContextAdvertisement struct {
	ID          string               `json:"id"`
	Source      string               `json:"source"`
	Kind        string               `json:"kind"`
	Ref         string               `json:"ref,omitempty"`
	Bucket      ContextBucket        `json:"bucket"`
	Summary     string               `json:"summary"`
	Matches     []ContextMatchSignal `json:"matches"`
	Projections []ContextProjection  `json:"projections"`
}

// Validate checks that an advertisement is complete enough for deterministic
// selection and bounded materialization.
func (a ContextAdvertisement) Validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if a.ID != strings.TrimSpace(a.ID) || strings.ContainsRune(a.ID, '\x00') {
		return fmt.Errorf("id must be a stable value without surrounding whitespace or NUL bytes")
	}
	if strings.TrimSpace(a.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if a.Source != strings.TrimSpace(a.Source) || strings.ContainsRune(a.Source, '\x00') {
		return fmt.Errorf("source must be a stable value without surrounding whitespace or NUL bytes")
	}
	if strings.TrimSpace(a.Kind) == "" {
		return fmt.Errorf("kind is required")
	}
	if !a.Bucket.Valid() {
		return fmt.Errorf("bucket %q is not recognized", a.Bucket)
	}
	if strings.TrimSpace(a.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if len(a.Matches) == 0 {
		return fmt.Errorf("at least one match signal is required")
	}
	for i, match := range a.Matches {
		if !match.Kind.Valid() {
			return fmt.Errorf("matches[%d]: kind %q is not recognized", i, match.Kind)
		}
		if math.IsNaN(match.Strength) || math.IsInf(match.Strength, 0) || match.Strength <= 0 || match.Strength > 1 {
			return fmt.Errorf("matches[%d]: strength must be finite and greater than 0 through 1", i)
		}
	}
	if len(a.Projections) == 0 {
		return fmt.Errorf("at least one projection is required")
	}
	seen := make(map[string]struct{}, len(a.Projections))
	for i, projection := range a.Projections {
		name := strings.TrimSpace(projection.Name)
		if name == "" {
			return fmt.Errorf("projections[%d]: name is required", i)
		}
		if projection.Name != name || strings.ContainsRune(projection.Name, '\x00') {
			return fmt.Errorf("projections[%d]: name must be stable without surrounding whitespace or NUL bytes", i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("projections[%d]: duplicate name %q", i, name)
		}
		seen[name] = struct{}{}
		if !projection.Role.Valid() {
			return fmt.Errorf("projections[%d]: role %q is not recognized", i, projection.Role)
		}
		if strings.TrimSpace(projection.Format) == "" {
			return fmt.Errorf("projections[%d]: format is required", i)
		}
		if projection.EstimatedBytes <= 0 {
			return fmt.Errorf("projections[%d]: estimated_bytes must be positive", i)
		}
	}
	return nil
}

// ContextSelection is the exact advertisement projection chosen for
// materialization. Passing the complete advertisement back lets a provider
// verify identity and retain any source-specific routing metadata.
type ContextSelection struct {
	Advertisement ContextAdvertisement `json:"advertisement"`
	Projection    ContextProjection    `json:"projection"`
}
