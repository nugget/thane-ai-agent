package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDocumentedServiceLoopDefaultsMatchTheApplied guards a lie that is
// easy to tell and expensive to find.
//
// The per-loop defaults are applied by a table in applyDefaults, and
// described separately in each config type's doc comment. Those comments
// are what generates config.example.yaml, so an operator reading the
// example is reading the comment, not the table. When the two disagree
// the example documents behaviour the binary does not have — which is
// exactly what happened when the metacognitive envelope was widened and
// only the table moved.
//
// Comparing against the values applyDefaults actually produces, rather
// than against a second copy of the table, keeps this from becoming one
// more thing to update in lockstep.
func TestDocumentedServiceLoopDefaultsMatchTheApplied(t *testing.T) {
	applied := &Config{}
	applied.applyDefaults()

	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	text := string(source)

	// The example config is a third copy of these numbers, and the one an
	// operator actually reads: config.example.yaml renders it verbatim.
	example := ExampleConfig()

	for _, tt := range []struct {
		name    string
		cfg     ServiceLoopConfig
		example ServiceLoopConfig
	}{
		{"metacognitive", applied.Metacognitive, example.Metacognitive},
		{"ego", applied.Ego, example.Ego},
		{"archivist", applied.Archivist, example.Archivist},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := fmt.Sprintf("Defaults: min_sleep %s, max_sleep %s, default_sleep %s",
				tt.cfg.MinSleep, tt.cfg.MaxSleep, tt.cfg.DefaultSleep)
			// Comments wrap, so compare on collapsed whitespace with the
			// comment markers removed.
			flat := regexp.MustCompile(`\s+`).ReplaceAllString(strings.ReplaceAll(text, "//", " "), " ")
			if strings.Count(flat, want) == 0 {
				t.Errorf("no doc comment states %q for %s; the documented defaults have drifted from applyDefaults", want, tt.name)
			}
			for _, field := range []struct{ name, got, expect string }{
				{"min_sleep", tt.example.MinSleep, tt.cfg.MinSleep},
				{"max_sleep", tt.example.MaxSleep, tt.cfg.MaxSleep},
				{"default_sleep", tt.example.DefaultSleep, tt.cfg.DefaultSleep},
			} {
				if field.got != field.expect {
					t.Errorf("config.example.yaml shows %s: %s for %s, but the applied default is %s; the example is what an operator copies", field.name, field.got, tt.name, field.expect)
				}
			}
		})
	}
}
