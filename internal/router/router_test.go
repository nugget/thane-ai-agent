package router

import (
	"context"
	"log/slog"
	"testing"
)

func newTestRouter() *Router {
	return NewRouter(slog.Default(), Config{
		DefaultModel: "test-model",
		MaxAuditLog:  10,
	})
}

func TestAnalyzeComplexity(t *testing.T) {
	r := newTestRouter()

	tests := []struct {
		name  string
		query string
		want  Complexity
	}{
		// Simple: direct device commands
		{name: "turn on", query: "turn on the office light", want: ComplexitySimple},
		{name: "turn off", query: "turn off all the lights", want: ComplexitySimple},
		{name: "lock", query: "lock the front door", want: ComplexitySimple},
		{name: "unlock", query: "unlock the garage", want: ComplexitySimple},
		{name: "set", query: "set the thermostat to 72", want: ComplexitySimple},

		// Simple: retrieval/search tasks (even with complex-looking words)
		{name: "search with history", query: "search IRC archives for distributed.net history", want: ComplexitySimple},
		{name: "search web", query: "search the web for FlightAware origins", want: ComplexitySimple},
		{name: "read file", query: "read the config file", want: ComplexitySimple},
		{name: "list entities", query: "list all light entities", want: ComplexitySimple},
		{name: "fetch page", query: "fetch the weather page", want: ComplexitySimple},
		{name: "find entity", query: "find the kitchen temperature sensor", want: ComplexitySimple},
		{name: "check state", query: "check if the front door is locked", want: ComplexitySimple},

		// Moderate: questions about state
		{name: "question mark", query: "what is the temperature outside?", want: ComplexityModerate},
		{name: "is prefix", query: "is the garage door open", want: ComplexityModerate},
		{name: "what prefix", query: "what time is it", want: ComplexityModerate},

		// Complex: reasoning and analysis (without simple action verbs)
		{name: "explain", query: "explain why the temperature dropped overnight", want: ComplexityComplex},
		{name: "analyze", query: "analyze the energy usage trends", want: ComplexityComplex},
		{name: "compare", query: "compare the upstairs and downstairs temperatures", want: ComplexityComplex},
		{name: "recommend", query: "recommend a good thermostat schedule", want: ComplexityComplex},
		{name: "standalone history", query: "show me the history of this device", want: ComplexityComplex},
		{name: "why", query: "why did the lights turn on at 3am", want: ComplexityComplex},

		// Default: moderate for ambiguous queries
		{name: "general chat", query: "hello, how are you today", want: ComplexityModerate},
		{name: "short command", query: "do it", want: ComplexityModerate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.analyzeComplexity(tt.query)
			if got != tt.want {
				t.Errorf("analyzeComplexity(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestDetectIntent(t *testing.T) {
	r := newTestRouter()

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "turn on", query: "turn on the kitchen light", want: "device_control"},
		{name: "turn off", query: "turn off the fan", want: "device_control"},
		{name: "lock", query: "lock the front door", want: "security"},
		{name: "temperature", query: "what is the temperature", want: "climate"},
		{name: "who home", query: "who is home right now", want: "presence"},
		{name: "when", query: "when did the last power outage happen", want: "temporal"},
		{name: "general", query: "hello", want: "general"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.detectIntent(tt.query)
			if got != tt.want {
				t.Errorf("detectIntent(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestRoute_LocalOnlyHint(t *testing.T) {
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "local-model",
		Models: []Model{
			{Name: "local-model", Provider: "ollama", SupportsTools: true, Speed: 8, Quality: 5, CostTier: 0, ContextWindow: 8192},
			{Name: "cloud-model", Provider: "anthropic", SupportsTools: true, Speed: 6, Quality: 10, CostTier: 3, ContextWindow: 8192},
		},
		MaxAuditLog: 10,
	})

	model, decision := r.Route(context.Background(), Request{
		Query:      "search archives for something",
		NeedsTools: true,
		ToolCount:  3,
		Priority:   PriorityBackground,
		Hints: map[string]string{
			HintLocalOnly: "true",
		},
	})

	if model != "local-model" {
		t.Errorf("Route() with local_only hint selected %q, want %q", model, "local-model")
	}

	// Cloud model should have a heavily negative score from the -200 penalty.
	score, ok := decision.Scores["cloud-model"]
	if !ok {
		t.Fatalf("cloud-model score missing from decision.Scores: %#v", decision.Scores)
	}
	if score >= 0 {
		t.Errorf("cloud-model score = %d, want negative (local_only penalty)", score)
	}
}

func TestMaxQuality(t *testing.T) {
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "local-model",
		Models: []Model{
			{Name: "local-model", Quality: 5},
			{Name: "mid-model", Quality: 7},
			{Name: "cloud-model", Quality: 10},
		},
	})

	if got := r.MaxQuality(); got != 10 {
		t.Errorf("MaxQuality() = %d, want 10", got)
	}
}

func TestMaxQuality_SingleModel(t *testing.T) {
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "only-model",
		Models: []Model{
			{Name: "only-model", Quality: 6},
		},
	})

	if got := r.MaxQuality(); got != 6 {
		t.Errorf("MaxQuality() = %d, want 6", got)
	}
}

func TestMaxQuality_NoModels(t *testing.T) {
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "fallback",
	})

	if got := r.MaxQuality(); got != 10 {
		t.Errorf("MaxQuality() with no models = %d, want 10 (safe default)", got)
	}
}

func TestRoute_PreferSpeedHint(t *testing.T) {
	// Two local models with the same cost tier: a fast+lower-quality model
	// and a slow+higher-quality model. Without HintPreferSpeed the slow
	// model wins on quality bonuses; with the hint the fast model wins.
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "fast-local",
		Models: []Model{
			{Name: "fast-local", Provider: "ollama", SupportsTools: true, Speed: 8, Quality: 5, CostTier: 0, ContextWindow: 8192},
			{Name: "slow-local", Provider: "ollama", SupportsTools: true, Speed: 4, Quality: 8, CostTier: 0, ContextWindow: 8192},
		},
		MaxAuditLog: 10,
	})

	model, decision := r.Route(context.Background(), Request{
		Query:      "check the lights",
		NeedsTools: true,
		ToolCount:  3,
		Priority:   PriorityBackground,
		Hints: map[string]string{
			HintLocalOnly:   "true",
			HintPreferSpeed: "true",
		},
	})

	if model != "fast-local" {
		t.Errorf("Route() with prefer_speed hint selected %q, want %q", model, "fast-local")
	}

	// Verify the fast model scored higher due to the speed bonus.
	fastScore := decision.Scores["fast-local"]
	slowScore := decision.Scores["slow-local"]
	if fastScore <= slowScore {
		t.Errorf("fast-local score (%d) should be higher than slow-local (%d) with prefer_speed hint",
			fastScore, slowScore)
	}
}

func TestRoute_PreferSpeedWithQualityFloor(t *testing.T) {
	// Quality floor should still disqualify low-quality models even when
	// prefer_speed is set. fast-local has quality 5 and should be blocked
	// by a quality_floor of 6.
	r := NewRouter(slog.Default(), Config{
		DefaultModel: "fast-local",
		Models: []Model{
			{Name: "fast-local", Provider: "ollama", SupportsTools: true, Speed: 8, Quality: 5, CostTier: 0, ContextWindow: 8192},
			{Name: "slow-local", Provider: "ollama", SupportsTools: true, Speed: 4, Quality: 8, CostTier: 0, ContextWindow: 8192},
		},
		MaxAuditLog: 10,
	})

	model, decision := r.Route(context.Background(), Request{
		Query:      "summarize the conversation",
		NeedsTools: false,
		Priority:   PriorityBackground,
		Hints: map[string]string{
			HintLocalOnly:    "true",
			HintPreferSpeed:  "true",
			HintQualityFloor: "6",
		},
	})

	if model != "slow-local" {
		t.Errorf("Route() with quality_floor=6 + prefer_speed selected %q, want %q", model, "slow-local")
	}

	// fast-local should have the below_quality_floor penalty.
	fastScore, ok := decision.Scores["fast-local"]
	if !ok {
		t.Fatal("fast-local score missing from decision.Scores")
	}
	slowScore, ok := decision.Scores["slow-local"]
	if !ok {
		t.Fatal("slow-local score missing from decision.Scores")
	}
	if fastScore >= slowScore {
		t.Errorf("fast-local score (%d) should be below slow-local (%d) with quality_floor penalty",
			fastScore, slowScore)
	}
}
