package router

import (
	"encoding/json"
	"testing"
)

func TestLoopProfileHints(t *testing.T) {
	tests := []struct {
		name string
		seed LoopProfile
		want map[string]string
	}{
		{
			name: "empty seed produces empty map",
			seed: LoopProfile{},
			want: map[string]string{},
		},
		{
			name: "all typed fields populated",
			seed: LoopProfile{
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
			seed: LoopProfile{
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
			seed: LoopProfile{
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
			seed: LoopProfile{
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
			seed: LoopProfile{
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

func TestLoopProfileValidate(t *testing.T) {
	tests := []struct {
		name    string
		seed    LoopProfile
		wantErr bool
	}{
		{name: "empty seed is valid", seed: LoopProfile{}},
		{name: "fully populated valid seed", seed: LoopProfile{
			QualityFloor:     "7",
			Mission:          "automation",
			LocalOnly:        "false",
			DelegationGating: "disabled",
			PreferSpeed:      "true",
		}},
		{name: "quality_floor boundary low", seed: LoopProfile{QualityFloor: "1"}},
		{name: "quality_floor boundary high", seed: LoopProfile{QualityFloor: "10"}},
		{name: "quality_floor zero", seed: LoopProfile{QualityFloor: "0"}, wantErr: true},
		{name: "quality_floor eleven", seed: LoopProfile{QualityFloor: "11"}, wantErr: true},
		{name: "quality_floor non-numeric", seed: LoopProfile{QualityFloor: "high"}, wantErr: true},
		{name: "quality_floor negative", seed: LoopProfile{QualityFloor: "-1"}, wantErr: true},
		{name: "mission conversation", seed: LoopProfile{Mission: "conversation"}},
		{name: "mission device_control", seed: LoopProfile{Mission: "device_control"}},
		{name: "mission background", seed: LoopProfile{Mission: "background"}},
		{name: "mission metacognitive", seed: LoopProfile{Mission: "metacognitive"}},
		{name: "mission reflect", seed: LoopProfile{Mission: "reflect"}},
		{name: "mission with spaces", seed: LoopProfile{Mission: "my mission"}, wantErr: true},
		{name: "mission with uppercase", seed: LoopProfile{Mission: "Automation"}, wantErr: true},
		{name: "mission starts with digit", seed: LoopProfile{Mission: "1automation"}, wantErr: true},
		{name: "mission empty string", seed: LoopProfile{Mission: ""}},
		{name: "local_only invalid", seed: LoopProfile{LocalOnly: "yes"}, wantErr: true},
		{name: "local_only true", seed: LoopProfile{LocalOnly: "true"}},
		{name: "prefer_speed invalid", seed: LoopProfile{PreferSpeed: "fast"}, wantErr: true},
		{name: "delegation_gating invalid", seed: LoopProfile{DelegationGating: "partial"}, wantErr: true},
		{name: "delegation_gating enabled", seed: LoopProfile{DelegationGating: "enabled"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.seed.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoopProfileRequestOptions(t *testing.T) {
	seed := LoopProfile{
		Model:            "claude-sonnet-4-20250514",
		QualityFloor:     "7",
		Mission:          "automation",
		LocalOnly:        "false",
		DelegationGating: "disabled",
		ExcludeTools:     []string{"resolve_anticipation", "cancel_anticipation"},
		InitialTags:      []string{"scheduler", "wake"},
		ExtraHints: map[string]string{
			"source": "scheduler",
		},
	}

	opts := seed.RequestOptions()

	if opts.Model != seed.Model {
		t.Fatalf("Model = %q, want %q", opts.Model, seed.Model)
	}
	if opts.Hints[HintMission] != seed.Mission {
		t.Fatalf("Hints[%q] = %q, want %q", HintMission, opts.Hints[HintMission], seed.Mission)
	}
	if opts.Hints["source"] != "scheduler" {
		t.Fatalf("Hints[source] = %q, want %q", opts.Hints["source"], "scheduler")
	}
	if len(opts.ExcludeTools) != len(seed.ExcludeTools) {
		t.Fatalf("ExcludeTools len = %d, want %d", len(opts.ExcludeTools), len(seed.ExcludeTools))
	}
	if len(opts.InitialTags) != len(seed.InitialTags) {
		t.Fatalf("InitialTags len = %d, want %d", len(opts.InitialTags), len(seed.InitialTags))
	}

	opts.ExcludeTools[0] = "changed"
	opts.InitialTags[0] = "changed"
	if seed.ExcludeTools[0] != "resolve_anticipation" {
		t.Fatalf("seed ExcludeTools mutated to %q", seed.ExcludeTools[0])
	}
	if seed.InitialTags[0] != "scheduler" {
		t.Fatalf("seed InitialTags mutated to %q", seed.InitialTags[0])
	}
}

func TestValidateTopicFilter(t *testing.T) {
	tests := []struct {
		filter  string
		wantErr bool
	}{
		{"foo/bar", false},
		{"foo/+/bar", false},
		{"foo/#", false},
		{"#", false},
		{"+/+/+", false},
		{"foo/bar/baz", false},

		// Invalid cases.
		{"", true},
		{"foo/ba#r", true},
		{"foo/#/bar", true},
		{"foo/b+r", true},
		{"foo/bar\x00/baz", true},
	}

	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			err := ValidateTopicFilter(tt.filter)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTopicFilter(%q) error = %v, wantErr %v", tt.filter, err, tt.wantErr)
			}
		})
	}
}

func TestLoopProfileJSONRoundTrip(t *testing.T) {
	original := LoopProfile{
		Model:            "gpt-4o",
		QualityFloor:     "8",
		Mission:          "automation",
		LocalOnly:        "false",
		DelegationGating: "disabled",
		PreferSpeed:      "true",
		ExcludeTools:     []string{"shell_exec", "web_fetch"},
		InitialTags:      []string{"homeassistant", "security"},
		ExtraHints:       map[string]string{"source": "frigate"},
		Instructions:     "Analyse the camera event and decide if action is needed.",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored LoopProfile
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

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
	if len(restored.InitialTags) != len(original.InitialTags) {
		t.Fatalf("InitialTags len = %d, want %d", len(restored.InitialTags), len(original.InitialTags))
	}
	for i, v := range original.InitialTags {
		if restored.InitialTags[i] != v {
			t.Errorf("InitialTags[%d] = %q, want %q", i, restored.InitialTags[i], v)
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
