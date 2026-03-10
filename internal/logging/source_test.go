package logging

import (
	"log/slog"
	"testing"
)

func TestShortenSource_StripsModulePrefix(t *testing.T) {
	src := &slog.Source{
		File:     "github.com/nugget/thane-ai-agent/internal/agent/loop.go",
		Line:     730,
		Function: "Run",
	}
	a := slog.Any(slog.SourceKey, src)

	got := ShortenSource(nil, a)

	gotSrc, ok := got.Value.Any().(*slog.Source)
	if !ok {
		t.Fatal("expected *slog.Source in result")
	}
	want := "internal/agent/loop.go"
	if gotSrc.File != want {
		t.Errorf("File = %q, want %q", gotSrc.File, want)
	}
	if gotSrc.Line != 730 {
		t.Errorf("Line = %d, want 730", gotSrc.Line)
	}
}

func TestShortenSource_NoPrefix(t *testing.T) {
	src := &slog.Source{
		File: "some/other/module/foo.go",
		Line: 42,
	}
	a := slog.Any(slog.SourceKey, src)

	got := ShortenSource(nil, a)

	gotSrc := got.Value.Any().(*slog.Source)
	if gotSrc.File != "some/other/module/foo.go" {
		t.Errorf("File = %q, should be unchanged", gotSrc.File)
	}
}

func TestShortenSource_NonSourceKey(t *testing.T) {
	a := slog.String("msg", "hello")
	got := ShortenSource(nil, a)

	if got.Key != "msg" || got.Value.String() != "hello" {
		t.Error("non-source attribute should pass through unchanged")
	}
}

func TestChainReplaceAttr(t *testing.T) {
	// First function uppercases the value of "msg" keys.
	upper := func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == "msg" {
			return slog.String("msg", "UPPER")
		}
		return a
	}

	// Second function adds a suffix.
	suffix := func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == "msg" {
			return slog.String("msg", a.Value.String()+"_suffix")
		}
		return a
	}

	chained := ChainReplaceAttr(upper, suffix)
	got := chained(nil, slog.String("msg", "hello"))

	if got.Value.String() != "UPPER_suffix" {
		t.Errorf("chained result = %q, want %q", got.Value.String(), "UPPER_suffix")
	}
}
