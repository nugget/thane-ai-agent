package metacognitive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// --- append_ego_observation tool tests ---

func TestAppendEgoObservation_NotRegisteredWithoutEgoFile(t *testing.T) {
	deps := testDeps(t, nil)
	// EgoFile is empty by default in testDeps.
	l := New(testConfig(), deps)

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")
	if tool != nil {
		t.Error("append_ego_observation should not be registered when EgoFile is empty")
	}
}

func TestAppendEgoObservation_RegisteredWithEgoFile(t *testing.T) {
	deps := testDeps(t, nil)
	deps.EgoFile = filepath.Join(deps.WorkspacePath, "ego.md")
	l := New(testConfig(), deps)

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")
	if tool == nil {
		t.Fatal("append_ego_observation should be registered when EgoFile is set")
	}
}

func TestAppendEgoObservation_CreatesNewFile(t *testing.T) {
	deps := testDeps(t, nil)
	egoPath := filepath.Join(deps.WorkspacePath, "ego.md")
	deps.EgoFile = egoPath
	l := New(testConfig(), deps)
	l.setCurrentConvID("metacog-ego-1")

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")

	observation := "The agent has developed a consistent pattern of shortening sleep intervals when garage activity is detected, even in quiet periods."
	result, err := tool.Handler(context.Background(), map[string]any{
		"observation": observation,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "core:ego.md") {
		t.Errorf("result = %q, want mention of core:ego.md", result)
	}

	// Verify file was created with the observation.
	data, err := os.ReadFile(egoPath)
	if err != nil {
		t.Fatalf("read ego file: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "Metacognitive Observation") {
		t.Error("ego file should contain observation heading")
	}
	if !strings.Contains(s, observation) {
		t.Error("ego file should contain the observation text")
	}
	if !strings.Contains(s, "iteration=metacog-ego-1") {
		t.Error("ego file should contain conversation ID in metadata")
	}
	if !strings.Contains(s, "observed=") {
		t.Error("ego file should contain observed timestamp")
	}
}

func TestAppendEgoObservation_AppendsToExisting(t *testing.T) {
	deps := testDeps(t, nil)
	egoPath := filepath.Join(deps.WorkspacePath, "ego.md")
	deps.EgoFile = egoPath
	l := New(testConfig(), deps)
	l.setCurrentConvID("metacog-ego-2")

	// Write existing ego content.
	existing := "# Ego\n\nI am an agent that monitors a household.\n"
	if err := os.WriteFile(egoPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing ego file: %v", err)
	}

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")

	observation := "Supervisor review reveals the agent consistently over-monitors the garage door compared to other systems. This bias may reflect training emphasis."
	_, err := tool.Handler(context.Background(), map[string]any{
		"observation": observation,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Verify existing content is preserved.
	data, err := os.ReadFile(egoPath)
	if err != nil {
		t.Fatalf("read ego file: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "I am an agent that monitors a household.") {
		t.Error("existing ego content should be preserved")
	}
	if !strings.Contains(s, observation) {
		t.Error("new observation should be appended")
	}
	if !strings.Contains(s, "iteration=metacog-ego-2") {
		t.Error("metadata should contain conversation ID")
	}
}

func TestAppendEgoObservation_RejectsShortContent(t *testing.T) {
	deps := testDeps(t, nil)
	deps.EgoFile = filepath.Join(deps.WorkspacePath, "ego.md")
	l := New(testConfig(), deps)

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")

	tests := []struct {
		name        string
		observation string
	}{
		{"empty", ""},
		{"too_short", "Short."},
		{"just_under_limit", strings.Repeat("x", minStateContentLen-1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), map[string]any{
				"observation": tt.observation,
			})
			if err == nil {
				t.Error("handler should reject short observation")
			}
			if err != nil && !strings.Contains(err.Error(), "too short") {
				t.Errorf("error = %v, want 'too short' message", err)
			}
		})
	}
}

func TestAppendEgoObservation_MultipleAppends(t *testing.T) {
	deps := testDeps(t, nil)
	egoPath := filepath.Join(deps.WorkspacePath, "ego.md")
	deps.EgoFile = egoPath
	l := New(testConfig(), deps)

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("append_ego_observation")

	// First observation.
	l.setCurrentConvID("metacog-first")
	_, err := tool.Handler(context.Background(), map[string]any{
		"observation": "First observation: the agent shows increasing confidence in pattern recognition over successive iterations.",
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	// Second observation.
	l.setCurrentConvID("metacog-second")
	_, err = tool.Handler(context.Background(), map[string]any{
		"observation": "Second observation: sleep durations have stabilized around 10 minutes during quiet periods, suggesting calibration convergence.",
	})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	// Verify both observations are in the file.
	data, err := os.ReadFile(egoPath)
	if err != nil {
		t.Fatalf("read ego file: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "iteration=metacog-first") {
		t.Error("file should contain first observation metadata")
	}
	if !strings.Contains(s, "iteration=metacog-second") {
		t.Error("file should contain second observation metadata")
	}
	if strings.Count(s, "### Metacognitive Observation") != 2 {
		t.Errorf("file should contain exactly 2 observation headings, got %d",
			strings.Count(s, "### Metacognitive Observation"))
	}
}
