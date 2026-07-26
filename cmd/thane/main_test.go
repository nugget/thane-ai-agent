package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_RenamedConfigFlagTeachesTheNewName(t *testing.T) {
	// The old name is rejected rather than aliased: a config outside
	// core is insecure by construction, and an alias would let that keep
	// happening silently under a name that does not say so.
	for _, args := range [][]string{
		{"-config", "/tmp/whatever.yaml", "validate"},
		{"-config=/tmp/whatever.yaml", "validate"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), &stdout, &stderr, args)
		if err == nil {
			t.Fatalf("run(%v) = nil, want an error naming the new flag", args)
		}
		msg := err.Error()
		if !strings.Contains(msg, "-insecure-config") {
			t.Fatalf("error should name the new flag: %v", err)
		}
		if !strings.Contains(msg, "-workspace") {
			t.Fatalf("error should point at -workspace for the ordinary case: %v", err)
		}
	}
}

func TestRun_InsecureConfigWithoutValueSaysSo(t *testing.T) {
	// The missing-value case is likeliest during the migration, when
	// muscle memory types the old flag's shape. "unknown flag" would
	// send the operator looking for a typo that is not there.
	for _, args := range [][]string{
		{"-insecure-config", "validate"},
		{"-insecure-config="},
		{"-insecure-config"},
	} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), &stdout, &stderr, args)
		if err == nil {
			t.Fatalf("run(%v) = nil, want a missing-value error", args)
		}
		if !strings.Contains(err.Error(), "needs a path") {
			t.Fatalf("run(%v) error = %v, want it to say a path is required", args, err)
		}
	}
}

func TestRun_InsecureConfigFlagLoadsExactPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.yaml")
	if err := os.WriteFile(path, []byte(minimalValidConfig), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), &stdout, &stderr, []string{"-insecure-config", path, "validate"}); err != nil {
		t.Fatalf("run with -insecure-config: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), path) {
		t.Fatalf("output should name the loaded config:\n%s", stdout.String())
	}
}
