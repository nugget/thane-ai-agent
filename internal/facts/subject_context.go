package facts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// subjectCtxKey is the context key for subject-keyed fact injection.
type subjectCtxKey struct{}

// WithSubjects adds subject keys to the context for pre-warming.
// Downstream context providers can retrieve these via
// [SubjectsFromContext] to query subject-keyed facts.
func WithSubjects(ctx context.Context, subjects []string) context.Context {
	if len(subjects) == 0 {
		return ctx
	}
	return context.WithValue(ctx, subjectCtxKey{}, subjects)
}

// SubjectsFromContext extracts subject keys from the context.
// Returns nil if no subjects were set.
func SubjectsFromContext(ctx context.Context) []string {
	if subjects, ok := ctx.Value(subjectCtxKey{}).([]string); ok {
		return subjects
	}
	return nil
}

// SubjectContextProvider injects facts keyed to specific subjects into
// the system prompt. Used for pre-warming cold-start loops with relevant
// context before the model sees the triggering event.
//
// Subject keys are passed through the context via [WithSubjects].
// When no subjects are present in the context, GetContext returns empty.
type SubjectContextProvider struct {
	store    *Store
	maxFacts int
	logger   *slog.Logger
}

// NewSubjectContextProvider creates a subject context provider with
// default settings (maxFacts=10).
func NewSubjectContextProvider(store *Store, logger *slog.Logger) *SubjectContextProvider {
	return &SubjectContextProvider{
		store:    store,
		maxFacts: 10,
		logger:   logger,
	}
}

// SetMaxFacts configures the maximum number of subject-matched facts
// to include in the context.
func (p *SubjectContextProvider) SetMaxFacts(n int) {
	p.maxFacts = n
}

// GetContext returns subject-keyed facts formatted for the system
// prompt. Implements the agent.ContextProvider interface.
//
// Subjects are extracted from the context via [SubjectsFromContext].
// If no subjects are present, returns empty. The userMessage parameter
// is unused — subject matching is purely key-based, not semantic.
func (p *SubjectContextProvider) GetContext(ctx context.Context, _ string) (string, error) {
	subjects := SubjectsFromContext(ctx)
	if len(subjects) == 0 {
		return "", nil
	}

	facts, err := p.store.GetBySubjects(subjects)
	if err != nil {
		return "", fmt.Errorf("query subject facts: %w", err)
	}

	if len(facts) == 0 {
		return "", nil
	}

	// Cap at maxFacts.
	if len(facts) > p.maxFacts {
		facts = facts[:p.maxFacts]
	}

	var sb strings.Builder
	sb.WriteString("### Subject-Keyed Facts\n\n")
	for i, f := range facts {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("**%s/%s**", f.Category, f.Key))
		if len(f.Subjects) > 0 {
			sb.WriteString(fmt.Sprintf(" [%s]", strings.Join(f.Subjects, ", ")))
		}
		sb.WriteString("\n")
		sb.WriteString(f.Value)
		if f.Ref != "" {
			sb.WriteString(fmt.Sprintf("\n📎 Full details: kb:%s", f.Ref))
		}
	}

	p.logger.Debug("subject context injected",
		"subjects", subjects,
		"facts_matched", len(facts),
	)

	return sb.String(), nil
}
