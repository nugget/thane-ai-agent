package logging

import (
	"log/slog"
	"strings"
)

// modulePrefix is the Go module path stripped from source file
// locations to keep log lines compact.
const modulePrefix = "github.com/nugget/thane-ai-agent/"

// ShortenSource is a [slog.HandlerOptions.ReplaceAttr] function that
// strips the module prefix from source file paths when AddSource is
// enabled. This yields compact paths like "internal/agent/loop.go:730"
// instead of the fully qualified module path.
func ShortenSource(_ []string, a slog.Attr) slog.Attr {
	if a.Key != slog.SourceKey {
		return a
	}
	src, ok := a.Value.Any().(*slog.Source)
	if !ok {
		return a
	}
	src.File = strings.TrimPrefix(src.File, modulePrefix)
	return slog.Any(slog.SourceKey, src)
}

// ChainReplaceAttr composes multiple [slog.HandlerOptions.ReplaceAttr]
// functions into one. Each function is applied in order, so earlier
// functions can transform the attribute before later ones see it.
func ChainReplaceAttr(fns ...func([]string, slog.Attr) slog.Attr) func([]string, slog.Attr) slog.Attr {
	return func(groups []string, a slog.Attr) slog.Attr {
		for _, fn := range fns {
			a = fn(groups, a)
		}
		return a
	}
}
