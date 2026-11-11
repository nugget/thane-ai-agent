package facets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const shrinkAttempts = 2

// shrinkCallTimeouts bounds one Shrink call for each projection the shrinker
// shortens, sized to the longest reply that projection accepts: at about
// three characters a token and 20 tokens a second, the slow end of a local
// model's decode, a status line's 180 characters take about three seconds
// and a teaser's 500 about eight, each with a second more for the first
// token. A projection absent here is never shrunk, and its refusal reaches
// the author as written. A digest's 2,048 characters would take about 35
// seconds a call, so two calls could hold a write for over a minute, and
// whether a shrink keeps the facts of a summary that long is unmeasured;
// full is longer still.
var shrinkCallTimeouts = map[string]time.Duration{
	string(StatusLine): 5 * time.Second,
	string(Teaser):     10 * time.Second,
}

// Shrinker lets a faceted write land when all that is wrong with it is a
// projection a little over its budget: an author that has stalled just over
// the limit trims a little per refused retry, while a fast model closes the
// gap in one call. A nil Shrinker shrinks nothing.
type Shrinker struct {
	// Model names the model Shrink calls, for the log.
	Model string
	// Shrink returns text, field's value, rewritten to at most limits: the
	// limits the text was refused over, such as "20 words", "180
	// characters" or "20 words and 180 characters". App wiring builds it
	// from a model client; this package sees only the func. Text returned
	// alongside an error is logged with it and never used.
	Shrink func(ctx context.Context, text string, field Field, limits string) (string, error)
	// Logger receives the INFO records Fit's contract lists.
	Logger *slog.Logger
}

// Shortened reports one projection a write saved in shortened form.
type Shortened struct {
	Projection string `json:"projection"`
	// Limit is the limit the sent text was reported over, as "20 words" or
	// "500 characters".
	Limit string `json:"limit"`
	// Sent is the length of the text the writer sent, in Limit's unit.
	Sent string `json:"sent"`
	// Saved is the length of SavedText, in Limit's unit.
	Saved     string `json:"saved"`
	SavedText string `json:"saved_text"`
	over      *overBudget
	sent      string
	attempts  int
}

// Fit runs validate, the writer's full check returning its refusal exactly as
// the writer sends it, and returns the payload to write:
//
//  1. A payload validate accepts, or any refusal from a nil Shrinker, comes
//     back unchanged.
//  2. Shrinking is attempted only when every violation in the refusal is a
//     projection the shrinker shortens (status_line or teaser) over its
//     budget and over none of its limits by more than half that limit.
//  3. Each such projection gets at most two Shrink calls, each bounded by a
//     deadline sized to the projection; the first result that is
//     non-empty, within every limit of the projection, and single-line
//     where the projection requires it replaces the projection.
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
		entry := Shortened{Projection: over.key, Limit: over.unit.count(over.limit), Sent: over.unit.count(over.count), over: over, sent: strings.TrimSpace(*item.value(&adjusted))}
		saved, err := s.shrink(ctx, &entry)
		if err != nil {
			s.abandon(ctx, append(shortened, entry), fmt.Sprintf("%s was not shortened: %v", over.key, err))
			return payload, nil, refusal
		}
		*item.value(&adjusted) = saved
		entry.save(saved)
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
// reports whether every violation is one in a projection the shrinker
// shortens ([shrinkCallTimeouts]), over by at most half of each of its
// limits, the one reported and any other.
// Wrappers (a frame, a label, a mark) are transparent; any other leaf is a
// violation shrinking cannot repair.
func smallOverruns(err error, found *[]*overBudget) bool {
	switch e := err.(type) {
	case *overBudget:
		*found = append(*found, e)
		_, shortens := shrinkCallTimeouts[e.key]
		return shortens && e.withinHalf()
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

// withinHalf reports whether e's value is over none of its projection's
// limits by more than half that limit. A value over its words is reported
// in words alone, so its characters are weighed here too.
func (e *overBudget) withinHalf() bool {
	within := func(count, limit int) bool { return limit == 0 || count <= limit+limit/2 }
	return within(e.words, e.field.MaxWords) && within(e.runes, e.field.MaxRunes)
}

// save records saved as the text the projection was shortened to.
func (e *Shortened) save(saved string) {
	e.SavedText, e.Saved = saved, e.over.unit.count(e.over.unit.measure(saved))
}

// shrink returns the first Shrink result entry's field accepts, counting
// the calls it made on entry.
func (s *Shrinker) shrink(ctx context.Context, entry *Shortened) (string, error) {
	field := entry.over.field
	var reason error
	for entry.attempts < shrinkAttempts {
		entry.attempts++
		callCtx, cancel := context.WithTimeout(ctx, shrinkCallTimeouts[field.Key])
		candidate, err := s.Shrink(callCtx, entry.sent, field, entry.over.ask())
		cancel()
		candidate = strings.TrimSpace(candidate)
		over := field.overBudget(candidate)
		switch {
		case err != nil:
			reason = err
		case candidate == "":
			reason = errors.New("the shortened text is empty")
		case over != nil:
			reason = fmt.Errorf("the shortened text is %s, still over %d", over.unit.count(over.count), over.limit)
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
	u := entry.over.unit
	attrs := []slog.Attr{
		slog.String("facet", entry.Projection), slog.Int("limit", entry.over.limit), slog.String("unit", string(u)),
		slog.Int("original_length", entry.over.count), slog.String("model", s.Model),
		slog.Int("attempts", entry.attempts), slog.String("original_text", entry.sent),
	}
	if newText != "" {
		attrs = append(attrs, slog.Int("new_length", u.measure(newText)), slog.String("new_text", newText))
	}
	if reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	logger.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}
