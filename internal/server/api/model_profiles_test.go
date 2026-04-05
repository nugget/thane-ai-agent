package api

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/router"
)

func TestNormalizeModelSelection(t *testing.T) {
	tests := []struct {
		name             string
		rawModel         string
		hints            map[string]string
		premiumFloor     string
		wantModel        string
		wantSystemPrompt bool
		wantHintKey      string
		wantHintValue    string
	}{
		{
			name:         "default latest profile",
			rawModel:     "thane:latest",
			hints:        map[string]string{"channel": "api"},
			premiumFloor: "8",
			wantModel:    "",
		},
		{
			name:          "premium profile",
			rawModel:      "thane:premium",
			hints:         map[string]string{"channel": "api"},
			premiumFloor:  "8",
			wantModel:     "",
			wantHintKey:   router.HintQualityFloor,
			wantHintValue: "8",
		},
		{
			name:          "assist alias profile",
			rawModel:      "thane:command",
			hints:         map[string]string{"channel": "api"},
			premiumFloor:  "8",
			wantModel:     "",
			wantHintKey:   router.HintMission,
			wantHintValue: "device_control",
		},
		{
			name:          "ops profile",
			rawModel:      "thane:ops",
			hints:         map[string]string{"channel": "api"},
			premiumFloor:  "8",
			wantModel:     "",
			wantHintKey:   router.HintDelegationGating,
			wantHintValue: "disabled",
		},
		{
			name:         "explicit deployment preserved",
			rawModel:     "spark/gpt-oss:20b",
			hints:        map[string]string{"channel": "api"},
			premiumFloor: "8",
			wantModel:    "spark/gpt-oss:20b",
		},
		{
			name:         "unknown thane profile falls back",
			rawModel:     "thane:unknown",
			hints:        map[string]string{"channel": "api"},
			premiumFloor: "8",
			wantModel:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, hints, systemPrompt := normalizeModelSelection(tt.rawModel, tt.hints, tt.premiumFloor, testAPILogger())
			if model != tt.wantModel {
				t.Fatalf("model = %q, want %q", model, tt.wantModel)
			}
			if hints["channel"] != "api" {
				t.Fatalf("channel hint = %q, want api", hints["channel"])
			}
			if tt.rawModel == "thane:premium" {
				if hints[router.HintVirtualModel] != "thane:premium" {
					t.Fatalf("virtual model hint = %q, want thane:premium", hints[router.HintVirtualModel])
				}
				if got := hints[router.DelegateHintKey(router.HintQualityFloor)]; got != "" {
					t.Fatalf("delegate quality floor = %q, want empty", got)
				}
				if got := hints[router.DelegateHintKey(router.HintLocalOnly)]; got != "" {
					t.Fatalf("delegate local_only = %q, want empty", got)
				}
			}
			if tt.rawModel == "thane:ops" {
				if hints[router.HintVirtualModel] != "thane:ops" {
					t.Fatalf("virtual model hint = %q, want thane:ops", hints[router.HintVirtualModel])
				}
				if hints[router.DelegateHintKey(router.HintQualityFloor)] != "8" {
					t.Fatalf("delegate quality floor = %q, want 8", hints[router.DelegateHintKey(router.HintQualityFloor)])
				}
			}
			if tt.wantHintKey != "" && hints[tt.wantHintKey] != tt.wantHintValue {
				t.Fatalf("%s = %q, want %q", tt.wantHintKey, hints[tt.wantHintKey], tt.wantHintValue)
			}
			if got := systemPrompt != ""; got != tt.wantSystemPrompt {
				t.Fatalf("systemPrompt set = %v, want %v", got, tt.wantSystemPrompt)
			}
		})
	}
}
