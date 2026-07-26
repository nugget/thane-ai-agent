package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
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

func TestGateOnCoreIntegrity_RefusesUnverifiedCore(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "core"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &config.Config{}
	cfg.Workspace.Path = workspace

	err := gateOnCoreIntegrity(context.Background(), slog.New(slog.DiscardHandler), cfg, "")
	if err == nil {
		t.Fatal("gate should refuse a core that is not a repository")
	}
	msg := err.Error()
	for _, want := range []string{"refusing to start", "core_repository", "fix:", "thane validate"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal should carry %q:\n%s", want, msg)
		}
	}
}

func TestGateOnCoreIntegrity_InsecureConfigBypassesButAnnounces(t *testing.T) {
	// A config outside the boundary cannot be verified against it, so
	// the gate steps aside — but the instance should be visibly
	// unverified without anyone going looking.
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg := &config.Config{}
	cfg.Workspace.Path = t.TempDir()

	if err := gateOnCoreIntegrity(context.Background(), logger, cfg, "/etc/thane/config.yaml"); err != nil {
		t.Fatalf("gate should not refuse an explicitly loaded config: %v", err)
	}
	if !strings.Contains(logged.String(), "outside the trust boundary") {
		t.Fatalf("bypass must be announced:\n%s", logged.String())
	}
}
