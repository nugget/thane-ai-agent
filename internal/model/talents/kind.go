package talents

import (
	"log/slog"
	"strings"
	"sync"
)

// KindTrailhead is the canonical frontmatter value that marks a talent
// or KB document as a decision-tree root — the first navigation or
// triage document a model meets when a capability activates. The legacy
// value [KindAliasEntryPoint] still loads for one migration cycle but
// emits a deprecation warning; new content must use KindTrailhead.
const (
	KindTrailhead       = "trailhead"
	KindAliasEntryPoint = "entry_point"
)

// trailheadAliasWarned dedupes the kind: entry_point deprecation
// warning so the message fires once per file path per process even
// though the talents loader and KB scanner re-read on every loop turn.
var trailheadAliasWarned sync.Map

// CanonicalKind normalizes a frontmatter kind value. It returns the
// canonical form ([KindTrailhead] when raw is either "trailhead" or the
// legacy "entry_point" alias, the trimmed raw value otherwise) and
// reports whether the legacy alias was used. Callers responsible for a
// file path should pair this with [WarnIfKindAlias] to surface the
// deprecation once per source.
func CanonicalKind(raw string) (canonical string, deprecated bool) {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case KindAliasEntryPoint:
		return KindTrailhead, true
	default:
		return trimmed, false
	}
}

// WarnIfKindAlias emits a one-time deprecation warning when raw is the
// legacy kind: entry_point alias. The warning is keyed by path so each
// source produces at most one log line per process — important because
// the talents loader and KB scanner re-parse files on every loop turn.
// Callers should pass the file path that produced raw; empty paths
// still warn (deduped under an empty key).
func WarnIfKindAlias(path, raw string) {
	if _, deprecated := CanonicalKind(raw); !deprecated {
		return
	}
	if _, already := trailheadAliasWarned.LoadOrStore(path, struct{}{}); already {
		return
	}
	slog.Default().Warn("talent frontmatter uses deprecated kind: entry_point; rename to kind: trailhead",
		"path", path)
}
