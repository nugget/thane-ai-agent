package provenance

import (
	"fmt"
	"strings"
	"time"
)

// trailerFormat renders a commit's trailers onto a single line: only the
// trailer block, folded continuations joined, entries separated by
// trailerSeparator.
const trailerFormat = "%(trailers:only=true,unfold=true,separator=%x1f)"

// trailerSeparator divides trailers within one log line. A unit separator
// cannot appear in a trailer git is willing to parse.
const trailerSeparator = "\x1f"

// parseTrailers turns git's rendered trailer block into a keyed map. A trailer
// git recognised always has a colon; anything else is skipped rather than
// guessed at. Returns nil when the commit carries no trailers, so the absence
// stays distinguishable from an empty set.
func parseTrailers(rendered string) map[string]string {
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return nil
	}
	var trailers map[string]string
	for _, entry := range strings.Split(rendered, trailerSeparator) {
		key, value, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if trailers == nil {
			trailers = make(map[string]string)
		}
		trailers[key] = value
	}
	return trailers
}

// parseRevisionLine parses a log line
// "hash\x00RFC3339\x00subject\x00trailers", optionally followed by
// "\x00%G?\x00%GS\x00%GF" when withSigner is set. Trailers arrive unfolded and
// joined by trailerSeparator so one commit stays one line.
func parseRevisionLine(line string, withSigner bool) (Revision, error) {
	fields := 4
	if withSigner {
		fields = 7
	}
	parts := strings.SplitN(line, "\x00", fields)
	if len(parts) < 4 {
		return Revision{}, fmt.Errorf("malformed revision line %q", line)
	}
	t, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return Revision{}, fmt.Errorf("parse revision timestamp %q: %w", parts[1], err)
	}
	rev := Revision{
		Commit:    parts[0],
		Short:     shorten(parts[0]),
		Timestamp: t,
		Message:   parts[2],
		Trailers:  parseTrailers(parts[3]),
	}
	if withSigner && len(parts) == 7 {
		cs := parseCommitSigner(parts[4], parts[5], parts[6])
		rev.Signer = &cs
	}
	return rev, nil
}
