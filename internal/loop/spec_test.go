package loop

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/router"
)

func TestLoopSpecValidate(t *testing.T) {
	t.Run("minimal request reply spec is valid", func(t *testing.T) {
		spec := &LoopSpec{
			Name:       "delegate-like",
			Task:       "Summarize what you find.",
			Operation:  OperationRequestReply,
			Completion: CompletionReturn,
			Profile: router.LoopProfile{
				Mission: "automation",
			},
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("invalid operation is rejected", func(t *testing.T) {
		spec := &LoopSpec{
			Name:      "bad-op",
			Task:      "Do something.",
			Operation: LoopOperation("launch_and_vibe"),
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "unsupported operation") {
			t.Fatalf("Validate() error = %v, want unsupported operation", err)
		}
	})

	t.Run("invalid completion is rejected", func(t *testing.T) {
		spec := &LoopSpec{
			Name:       "bad-completion",
			Task:       "Do something.",
			Completion: LoopCompletion("callback"),
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "unsupported completion") {
			t.Fatalf("Validate() error = %v, want unsupported completion", err)
		}
	})

	t.Run("invalid profile is rejected", func(t *testing.T) {
		spec := &LoopSpec{
			Name: "bad-profile",
			Task: "Do something.",
			Profile: router.LoopProfile{
				QualityFloor: "99",
			},
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "profile") {
			t.Fatalf("Validate() error = %v, want profile validation", err)
		}
	})
}

func TestLoopSpecToConfigCopiesMutableFields(t *testing.T) {
	jitter := 0.4
	spec := &LoopSpec{
		Name:         "copy-test",
		Task:         "Watch the room.",
		Tags:         []string{"monitoring"},
		ExcludeTools: []string{"shell_exec"},
		Jitter:       &jitter,
		Hints: map[string]string{
			"source": "loop",
		},
		Metadata: map[string]string{
			"room": "office",
		},
	}

	cfg := spec.ToConfig()
	cfg.Tags[0] = "changed"
	cfg.ExcludeTools[0] = "other"
	cfg.Hints["source"] = "changed"
	cfg.Metadata["room"] = "changed"
	*cfg.Jitter = 0.9

	if spec.Tags[0] != "monitoring" {
		t.Fatalf("spec.Tags mutated = %q", spec.Tags[0])
	}
	if spec.ExcludeTools[0] != "shell_exec" {
		t.Fatalf("spec.ExcludeTools mutated = %q", spec.ExcludeTools[0])
	}
	if spec.Hints["source"] != "loop" {
		t.Fatalf("spec.Hints mutated = %q", spec.Hints["source"])
	}
	if spec.Metadata["room"] != "office" {
		t.Fatalf("spec.Metadata mutated = %q", spec.Metadata["room"])
	}
	if *spec.Jitter != 0.4 {
		t.Fatalf("spec.Jitter mutated = %v", *spec.Jitter)
	}
}
