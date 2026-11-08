package facets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	shrinkAttempts       = 2
	shrinkAttemptTimeout = 5 * time.Second
)

// Shrinker lets a faceted write land when all that is wrong with it is a
// projection a little over its budget: a model cannot count characters and
// trims one or two per refused retry, while a fast model closes the gap in
// one call. A nil Shrinker shrinks nothing.
type Shrinker struct {
	// Model names the model Shrink calls, for the log.
	Model string
	// Shrink returns text rewritten to at most limit characters. App wiring
	// builds it from a model client; this package sees only the func. Text
	// returned alongside an error is logged with it and never used.
	Shrink func(ctx context.Context, text string, limit int) (string, error)
	// Logger receives the INFO records Fit's contract lists.
	Logger *slog.Logger
}

// Shortened reports one projection a write saved in shortened form.
type Shortened struct {
	Projection      string `json:"projection"`
	Limit           int    `json:"limit"`
	SentCharacters  int    `json:"sent_characters"`
	SavedCharacters int    `json:"saved_characters"`
	SavedText       string `json:"saved_text"`
	sent            string
	attempts        int
}

// Fit runs validate, the writer's full check returning its refusal exactly as
// the writer sends it, and returns the payload to write:
//
//  1. A payload validate accepts, or any refusal from a nil Shrinker, comes
//     back unchanged.
//  2. Shrinking is attempted only when every violation in the refusal is an
//     over-budget projection whose overage is at most half its limit.
//  3. Each such projection gets at most two Shrink calls of a few seconds
//     each; the first result that is non-empty, within the limit, and
//     single-line where the projection requires it replaces the projection.
//  4. validate runs again on the adjusted payload. If it passes, Fit returns
//     that payload and one [Shortened] per replaced projection; if anything
//     failed, the original payload and refusal, so the model only ever reads
//     errors about its own text.
//  5. The operator keeps everything, at INFO: each Shrink reply not used,
//     with its text and why, and one outcome per projection a shrink
//     touched, with the text it was shortened to: here, with the reason,
//     when the original refusal stands, and otherwise from [Shrinker.Report]
//     once the writer knows whether the store saved it.
func (s *Shrinker) Fit(ctx context.Context, payload Payload, validate func(Payload) error) (Payload, []Shortened, error) {
	refusal := validate(payload)
	var overruns []*overBudget
	if refusal == nil || s == nil || s.Shrink == nil || !smallOverruns(refusal, &overruns) || len(overruns) == 0 {
		return payload, nil, refusal
	}
	adjusted, shortened := payload, make([]Shortened, 0, len(overruns))
	for _, over := range overruns {
		item, _ := sectionByKey(over.key)
		entry := Shortened{Projection: over.key, Limit: over.limit, SentCharacters: over.runes, sent: strings.TrimSpace(*item.value(&adjusted))}
		saved, err := s.shrink(ctx, item.field, &entry)
		if err != nil {
			s.abandon(ctx, append(shortened, entry), fmt.Sprintf("%s was not shortened: %v", over.key, err))
			return payload, nil, refusal
		}
		*item.value(&adjusted), entry.SavedText, entry.SavedCharacters = saved, saved, utf8.RuneCountInString(saved)
		shortened = append(shortened, entry)
	}
	if err := validate(adjusted); err != nil {
		s.abandon(ctx, shortened, "the shortened payload failed validation: "+err.Error())
		return payload, nil, refusal
	}
	return adjusted, shortened, nil
}

// Report logs the outcome of writing a payload Fit shortened: each entry
// saved, or, when err refused the write, not saved and why.
func (s *Shrinker) Report(ctx context.Context, shortened []Shortened, err error) {
	for _, entry := range shortened {
		if err != nil {
			s.log(ctx, "shortened projection not saved", entry, entry.SavedText, err.Error())
			continue
		}
		s.log(ctx, "projection shortened to fit its budget", entry, entry.SavedText, "")
	}
}

// smallOverruns appends each over-budget violation in err's tree to found and
// reports whether every violation is one over by at most half its limit.
// Wrappers (a frame, a label, a mark) are transparent; any other leaf is a
// violation shrinking cannot repair.
func smallOverruns(err error, found *[]*overBudget) bool {
	switch e := err.(type) {
	case *overBudget:
		*found = append(*found, e)
		return e.runes <= e.limit+e.limit/2
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			if !smallOverruns(child, found) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return smallOverruns(e.Unwrap(), found)
	default:
		return false
	}
}

// shrink returns the first Shrink result field accepts, counting the calls
// it made on entry.
func (s *Shrinker) shrink(ctx context.Context, field Field, entry *Shortened) (string, error) {
	var reason error
	for entry.attempts < shrinkAttempts {
		entry.attempts++
		callCtx, cancel := context.WithTimeout(ctx, shrinkAttemptTimeout)
		candidate, err := s.Shrink(callCtx, entry.sent, field.MaxRunes)
		cancel()
		switch candidate = strings.TrimSpace(candidate); {
		case err != nil:
			reason = err
		case candidate == "":
			reason = errors.New("the shortened text is empty")
		case utf8.RuneCountInString(candidate) > field.MaxRunes:
			reason = fmt.Errorf("the shortened text is %d characters, still over %d", utf8.RuneCountInString(candidate), field.MaxRunes)
		case field.SingleLine && strings.ContainsAny(candidate, "\r\n"):
			reason = errors.New("the shortened text has a line break")
		default:
			return candidate, nil
		}
		s.log(ctx, "projection shrink reply rejected", *entry, candidate, reason.Error())
	}
	return "", reason
}

// abandon logs the outcome of each projection a failed Fit touched.
func (s *Shrinker) abandon(ctx context.Context, entries []Shortened, reason string) {
	for _, entry := range entries {
		s.log(ctx, "projection shrink failed; the original refusal stands", entry, entry.SavedText, reason)
	}
}

// log writes one INFO record about entry, with newText, the text the shrink
// model replied with, and reason when each is set.
func (s *Shrinker) log(ctx context.Context, msg string, entry Shortened, newText, reason string) {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []slog.Attr{
		slog.String("facet", entry.Projection), slog.Int("limit", entry.Limit),
		slog.Int("original_runes", entry.SentCharacters), slog.String("model", s.Model),
		slog.Int("attempts", entry.attempts), slog.String("original_text", entry.sent),
	}
	if newText != "" {
		attrs = append(attrs, slog.Int("new_runes", utf8.RuneCountInString(newText)), slog.String("new_text", newText))
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}
