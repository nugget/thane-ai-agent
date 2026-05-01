package loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

func TestSpecValidate(t *testing.T) {
	t.Run("minimal request reply spec is valid", func(t *testing.T) {
		spec := &Spec{
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
		spec := &Spec{
			Name:      "bad-op",
			Task:      "Do something.",
			Operation: Operation("launch_and_vibe"),
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "unsupported operation") {
			t.Fatalf("Validate() error = %v, want unsupported operation", err)
		}
	})

	t.Run("invalid completion is rejected", func(t *testing.T) {
		spec := &Spec{
			Name:       "bad-completion",
			Task:       "Do something.",
			Completion: Completion("callback"),
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "unsupported completion") {
			t.Fatalf("Validate() error = %v, want unsupported completion", err)
		}
	})

	t.Run("invalid profile is rejected", func(t *testing.T) {
		spec := &Spec{
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

	t.Run("missing name is rejected", func(t *testing.T) {
		spec := &Spec{
			Task:       "Summarize what you find.",
			Operation:  OperationRequestReply,
			Completion: CompletionReturn,
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "spec name is required") {
			t.Fatalf("Validate() error = %v, want missing name rejection", err)
		}
	})
}

func TestSpecToConfigCopiesMutableFields(t *testing.T) {
	jitter := 0.4
	spec := &Spec{
		Name:            "copy-test",
		Task:            "Watch the room.",
		Tags:            []string{"monitoring"},
		ExcludeTools:    []string{"shell_exec"},
		Jitter:          &jitter,
		FallbackContent: "please try again",
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
	if cfg.FallbackContent != "please try again" {
		t.Fatalf("cfg.FallbackContent = %q, want %q", cfg.FallbackContent, "please try again")
	}
}

func TestSpecToConfigAppliesOneShotOperationDefaults(t *testing.T) {
	spec := &Spec{
		Name:       "delegate-like",
		Task:       "Do one thing.",
		Operation:  OperationRequestReply,
		Completion: CompletionReturn,
	}

	cfg := spec.ToConfig()
	if cfg.MaxIter != 1 {
		t.Fatalf("MaxIter = %d, want 1", cfg.MaxIter)
	}
	if cfg.SleepMin != time.Millisecond || cfg.SleepMax != time.Millisecond || cfg.SleepDefault != time.Millisecond {
		t.Fatalf("sleep defaults = min %v max %v default %v, want all 1ms", cfg.SleepMin, cfg.SleepMax, cfg.SleepDefault)
	}
	if cfg.Jitter == nil || *cfg.Jitter != 0 {
		t.Fatalf("Jitter = %v, want 0", cfg.Jitter)
	}
}

func TestSpecProfileRequestSeedsInitialTagsFromSpecTags(t *testing.T) {
	t.Run("Spec.Tags seeds Request.InitialTags", func(t *testing.T) {
		spec := &Spec{
			Name:       "weekend_observer",
			Task:       "watch the weekend",
			Operation:  OperationService,
			Completion: CompletionConversation,
			Tags:       []string{"ha", "hpde", "documents"},
			Profile: router.LoopProfile{
				Mission: "background",
			},
		}

		got := spec.profileRequest().InitialTags

		want := []string{"ha", "hpde", "documents"}
		if len(got) != len(want) {
			t.Fatalf("InitialTags = %v, want %v", got, want)
		}
		for i, tag := range want {
			if got[i] != tag {
				t.Fatalf("InitialTags[%d] = %q, want %q", i, got[i], tag)
			}
		}
	})

	t.Run("empty Spec.Tags yields empty InitialTags", func(t *testing.T) {
		spec := &Spec{
			Name:       "weekend_observer",
			Task:       "watch the weekend",
			Operation:  OperationService,
			Completion: CompletionConversation,
			Profile: router.LoopProfile{
				Mission: "background",
			},
		}

		if got := spec.profileRequest().InitialTags; len(got) != 0 {
			t.Fatalf("InitialTags = %v, want empty", got)
		}
	})

	t.Run("seeded slice does not alias Spec.Tags", func(t *testing.T) {
		spec := &Spec{
			Name:       "weekend_observer",
			Task:       "watch the weekend",
			Operation:  OperationService,
			Completion: CompletionConversation,
			Tags:       []string{"ha", "hpde"},
		}

		req := spec.profileRequest()
		req.InitialTags[0] = "mutated"

		if spec.Tags[0] != "ha" {
			t.Fatalf("spec.Tags[0] = %q, want ha (slice was aliased)", spec.Tags[0])
		}
	})
}

func TestSpecValidatePersistableRejectsRuntimeHooks(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{
			name: "task builder",
			mutate: func(spec *Spec) {
				spec.TaskBuilder = func(_ context.Context, _ bool) (string, error) {
					return "dynamic", nil
				}
			},
			wantErr: "cannot set TaskBuilder",
		},
		{
			name: "turn builder",
			mutate: func(spec *Spec) {
				spec.TurnBuilder = func(context.Context, TurnInput) (*AgentTurn, error) {
					return nil, nil
				}
			},
			wantErr: "cannot set TurnBuilder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &Spec{
				Name: "dynamic-loop",
				Task: "Do useful background work.",
			}
			tt.mutate(spec)

			err := spec.ValidatePersistable()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidatePersistable() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSpecJSONRoundTripUsesHumanFacingFields(t *testing.T) {
	spec := Spec{
		Name:       "room_monitor",
		Enabled:    true,
		Task:       "Watch the office.",
		Operation:  OperationService,
		Completion: CompletionConversation,
		Tags:       []string{"homeassistant"},
		Profile: router.LoopProfile{
			Mission:      "background",
			Instructions: "Be concise.",
		},
		Conditions: Conditions{
			Schedule: &ScheduleCondition{
				Timezone: "America/Chicago",
				Windows: []ScheduleWindow{{
					Days:  []string{"mon", "tue", "wed", "thu", "fri"},
					Start: "09:00",
					End:   "17:00",
				}},
			},
		},
		SleepMin:     5 * time.Minute,
		SleepMax:     30 * time.Minute,
		SleepDefault: 10 * time.Minute,
		MaxDuration:  time.Hour,
		OnRetrigger:  RetriggerRestart,
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	gotJSON := string(data)
	for _, want := range []string{`"enabled":true`, `"sleep_min":"5m0s"`, `"sleep_max":"30m0s"`, `"max_duration":"1h0m0s"`, `"on_retrigger":"restart"`, `"conditions":{"schedule":{"timezone":"America/Chicago"`} {
		if !strings.Contains(gotJSON, want) {
			t.Fatalf("json = %s, want substring %s", gotJSON, want)
		}
	}

	var roundTrip Spec
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if roundTrip.SleepMin != 5*time.Minute || roundTrip.SleepMax != 30*time.Minute || roundTrip.MaxDuration != time.Hour {
		t.Fatalf("roundTrip durations = min %v max %v maxDuration %v", roundTrip.SleepMin, roundTrip.SleepMax, roundTrip.MaxDuration)
	}
	if roundTrip.OnRetrigger != RetriggerRestart {
		t.Fatalf("OnRetrigger = %v, want restart", roundTrip.OnRetrigger)
	}
	if !roundTrip.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if roundTrip.Conditions.Schedule == nil || roundTrip.Conditions.Schedule.Timezone != "America/Chicago" {
		t.Fatalf("Conditions = %+v, want America/Chicago schedule", roundTrip.Conditions)
	}
}

func TestSpecJSONInvalidOnRetriggerNamesField(t *testing.T) {
	var spec Spec
	err := json.Unmarshal([]byte(`{"name":"bad","task":"watch","on_retrigger":"bogus"}`), &spec)
	if err == nil {
		t.Fatal("expected error for invalid on_retrigger")
	}
	if !strings.Contains(err.Error(), "on_retrigger") {
		t.Fatalf("error = %v, want on_retrigger context", err)
	}
}

func TestSpecJSONMarshalRejectsUnsupportedRetriggerMode(t *testing.T) {
	spec := Spec{
		Name:        "bad",
		Task:        "watch",
		OnRetrigger: RetriggerMode(99),
	}

	_, err := json.Marshal(spec)
	if err == nil {
		t.Fatal("expected error for unsupported retrigger mode")
	}
	if !strings.Contains(err.Error(), "unsupported retrigger mode") {
		t.Fatalf("error = %v, want unsupported retrigger mode", err)
	}
}
