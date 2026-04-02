package router

import (
	"encoding/json"
	"testing"
)

func TestLoopSeedHints(t *testing.T) {
	tests := []struct {
		name string
		seed LoopSeed
		want map[string]string
	}{
		{
			name: "empty seed produces empty map",
			seed: LoopSeed{},
			want: map[string]string{},
		},
		{
			name: "all typed fields populated",
			seed: LoopSeed{
				QualityFloor:     "7",
				Mission:          "automation",
				LocalOnly:        "false",
				DelegationGating: "disabled",
				PreferSpeed:      "true",
			},
			want: map[string]string{
				HintQualityFloor:     "7",
				HintMission:          "automation",
				HintLocalOnly:        "false",
				HintDelegationGating: "disabled",
				HintPreferSpeed:      "true",
			},
		},
		{
			name: "only populated fields appear",
			seed: LoopSeed{
				QualityFloor: "5",
				Mission:      "conversation",
			},
			want: map[string]string{
				HintQualityFloor: "5",
				HintMission:      "conversation",
			},
		},
		{
			name: "extra hints merged",
			seed: LoopSeed{
				Mission: "automation",
				ExtraHints: map[string]string{
					"source": "frigate",
					"custom": "value",
				},
			},
			want: map[string]string{
				HintMission: "automation",
				"source":    "frigate",
				"custom":    "value",
			},
		},
		{
			name: "extra hints override typed fields",
			seed: LoopSeed{
				Mission: "automation",
				ExtraHints: map[string]string{
					HintMission: "device_control",
				},
			},
			want: map[string]string{
				HintMission: "device_control",
			},
		},
		{
			name: "model not in hints",
			seed: LoopSeed{
				Model:        "claude-3-opus",
				QualityFloor: "10",
			},
			want: map[string]string{
				HintQualityFloor: "10",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.seed.Hints()
			if len(got) != len(tt.want) {
				t.Fatalf("len(Hints()) = %d, want %d\n  got:  %v\n  want: %v", len(got), len(tt.want), got, tt.want)
			}
			for k, wv := range tt.want {
				if gv, ok := got[k]; !ok {
					t.Errorf("missing key %q", k)
				} else if gv != wv {
					t.Errorf("key %q = %q, want %q", k, gv, wv)
				}
			}
		})
	}
}

func TestLoopSeedJSONRoundTrip(t *testing.T) {
	original := LoopSeed{
		Model:            "gpt-4o",
		QualityFloor:     "8",
		Mission:          "automation",
		LocalOnly:        "false",
		DelegationGating: "disabled",
		PreferSpeed:      "true",
		ExcludeTools:     []string{"shell_exec", "web_fetch"},
		SeedTags:         []string{"homeassistant", "security"},
		ExtraHints:       map[string]string{"source": "frigate"},
		Instructions:     "Analyse the camera event and decide if action is needed.",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored LoopSeed
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Compare fields.
	if restored.Model != original.Model {
		t.Errorf("Model = %q, want %q", restored.Model, original.Model)
	}
	if restored.QualityFloor != original.QualityFloor {
		t.Errorf("QualityFloor = %q, want %q", restored.QualityFloor, original.QualityFloor)
	}
	if restored.Mission != original.Mission {
		t.Errorf("Mission = %q, want %q", restored.Mission, original.Mission)
	}
	if restored.LocalOnly != original.LocalOnly {
		t.Errorf("LocalOnly = %q, want %q", restored.LocalOnly, original.LocalOnly)
	}
	if restored.DelegationGating != original.DelegationGating {
		t.Errorf("DelegationGating = %q, want %q", restored.DelegationGating, original.DelegationGating)
	}
	if restored.PreferSpeed != original.PreferSpeed {
		t.Errorf("PreferSpeed = %q, want %q", restored.PreferSpeed, original.PreferSpeed)
	}
	if restored.Instructions != original.Instructions {
		t.Errorf("Instructions = %q, want %q", restored.Instructions, original.Instructions)
	}
	if len(restored.ExcludeTools) != len(original.ExcludeTools) {
		t.Fatalf("ExcludeTools len = %d, want %d", len(restored.ExcludeTools), len(original.ExcludeTools))
	}
	for i, v := range original.ExcludeTools {
		if restored.ExcludeTools[i] != v {
			t.Errorf("ExcludeTools[%d] = %q, want %q", i, restored.ExcludeTools[i], v)
		}
	}
	if len(restored.SeedTags) != len(original.SeedTags) {
		t.Fatalf("SeedTags len = %d, want %d", len(restored.SeedTags), len(original.SeedTags))
	}
	for i, v := range original.SeedTags {
		if restored.SeedTags[i] != v {
			t.Errorf("SeedTags[%d] = %q, want %q", i, restored.SeedTags[i], v)
		}
	}
	if len(restored.ExtraHints) != len(original.ExtraHints) {
		t.Fatalf("ExtraHints len = %d, want %d", len(restored.ExtraHints), len(original.ExtraHints))
	}
	for k, v := range original.ExtraHints {
		if restored.ExtraHints[k] != v {
			t.Errorf("ExtraHints[%q] = %q, want %q", k, restored.ExtraHints[k], v)
		}
	}
}
