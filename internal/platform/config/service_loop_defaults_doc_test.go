package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// docDefaultsPattern matches a documented service-loop sleep envelope in
// a config.go doc comment.
var docDefaultsPattern = regexp.MustCompile(`Defaults: min_sleep (\S+), max_sleep (\S+), default_sleep (\S+?),`)

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
	// Every "Defaults:" line in the file, not just the first match per
	// loop. config.go carries these twice per loop — once on the Config
	// field and once on the type alias — so a check that only asks
	// whether the right string appears somewhere is satisfied by the copy
	// that did not drift, while the one that did goes on lying.
	documented := docDefaultsPattern.FindAllStringSubmatch(
		regexp.MustCompile(`\s+`).ReplaceAllString(strings.ReplaceAll(string(source), "//", " "), " "), -1)
	if len(documented) == 0 {
		t.Fatal("no documented service-loop defaults found; this guard is not looking at anything")
	}

	// The example config is a third copy of these numbers, and the one an
	// operator actually reads: config.example.yaml renders it verbatim.
	example := ExampleConfig()

	loops := []struct {
		name    string
		cfg     ServiceLoopConfig
		example ServiceLoopConfig
	}{
		{"metacognitive", applied.Metacognitive, example.Metacognitive},
		{"ego", applied.Ego, example.Ego},
		{"archivist", applied.Archivist, example.Archivist},
	}

	// Every documented triple must be some loop's real defaults. This is
	// the half that catches a drifted copy rather than a missing one.
	valid := make(map[string]string, len(loops))
	for _, l := range loops {
		valid[fmt.Sprintf("%s|%s|%s", l.cfg.MinSleep, l.cfg.MaxSleep, l.cfg.DefaultSleep)] = l.name
	}
	seen := make(map[string]int, len(loops))
	for _, m := range documented {
		key := fmt.Sprintf("%s|%s|%s", m[1], m[2], m[3])
		name, ok := valid[key]
		if !ok {
			t.Errorf("a doc comment states defaults min_sleep %s, max_sleep %s, default_sleep %s, which is no loop's applied value; a copy has drifted", m[1], m[2], m[3])
			continue
		}
		seen[name]++
	}

	for _, tt := range loops {
		t.Run(tt.name, func(t *testing.T) {
			if seen[tt.name] == 0 {
				t.Errorf("no doc comment states the applied defaults for %s (min_sleep %s, max_sleep %s, default_sleep %s)", tt.name, tt.cfg.MinSleep, tt.cfg.MaxSleep, tt.cfg.DefaultSleep)
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
