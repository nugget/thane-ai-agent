package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
)

// mockFactSetter records SetFact calls for test assertions.
type mockFactSetter struct {
	calls []setFactCall
}

type setFactCall struct {
	category, key, value, source string
	confidence                   float64
}

func (m *mockFactSetter) SetFact(category, key, value, source string, confidence float64) error {
	m.calls = append(m.calls, setFactCall{category, key, value, source, confidence})
	return nil
}

func TestShouldExtract(t *testing.T) {
	e := NewExtractor(&mockFactSetter{}, slog.Default(), 2)

	tests := []struct {
		name         string
		userMsg      string
		assistResp   string
		messageCount int
		skipContext  bool
		want         bool
	}{
		{
			name:         "normal conversation passes",
			userMsg:      "I prefer my office temperature at 72 degrees",
			assistResp:   "I'll remember that you prefer your office at 72°F. Would you like me to adjust the thermostat now?",
			messageCount: 4,
			want:         true,
		},
		{
			name:         "skip context (auxiliary request)",
			userMsg:      "Generate a brief title for this chat",
			assistResp:   "Office Temperature Preferences",
			messageCount: 4,
			skipContext:  true,
			want:         false,
		},
		{
			name:         "too few messages",
			userMsg:      "Hello",
			assistResp:   "Hi there! How can I help you today?",
			messageCount: 1,
			want:         false,
		},
		{
			name:         "short assistant response (confirmation)",
			userMsg:      "Turn on the kitchen light",
			assistResp:   "Done.",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "simple device command - turn on",
			userMsg:      "turn on the office light",
			assistResp:   "I've turned on the office light for you. The brightness is set to 100%.",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "simple device command - turn off",
			userMsg:      "turn off the bedroom fan",
			assistResp:   "Done. The bedroom fan is now off. The current temperature is 71°F.",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "simple device command - set the",
			userMsg:      "set the thermostat to 68",
			assistResp:   "I've set the thermostat to 68°F. It should reach that temperature in about 15 minutes.",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "simple device command - what time",
			userMsg:      "what time is it",
			assistResp:   "It's currently 3:45 PM on Tuesday, February 13th.",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "very short message",
			userMsg:      "hey",
			assistResp:   "Hey there! How can I help you today? Want me to check on anything?",
			messageCount: 4,
			want:         false,
		},
		{
			name:         "informational conversation passes",
			userMsg:      "My wife Sarah and I just moved the office upstairs to the second room",
			assistResp:   "Got it! I'll remember that your office is now upstairs in the second room. Would you like me to update any automations that reference the office?",
			messageCount: 6,
			want:         true,
		},
		{
			name:         "exact min messages threshold passes",
			userMsg:      "We have two cats named Luna and Mochi",
			assistResp:   "That's wonderful! I'll remember Luna and Mochi. Would you like me to set up any pet-related automations?",
			messageCount: 2,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.ShouldExtract(tt.userMsg, tt.assistResp, tt.messageCount, tt.skipContext)
			if got != tt.want {
				t.Errorf("ShouldExtract() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtract_PersistsFacts(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return &ExtractionResult{
			WorthPersisting: true,
			Facts: []ExtractedFact{
				{Category: "preference", Key: "office_temp", Value: "Prefers 72°F", Confidence: 0.9},
				{Category: "user", Key: "partner_name", Value: "Partner is Sarah", Confidence: 0.85},
			},
		}, nil
	})

	err := e.Extract(context.Background(), "test user msg", "test response", nil)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}

	if len(mock.calls) != 2 {
		t.Fatalf("expected 2 SetFact calls, got %d", len(mock.calls))
	}

	// Verify first fact
	if mock.calls[0].category != "preference" || mock.calls[0].key != "office_temp" {
		t.Errorf("call[0] = %v, want preference/office_temp", mock.calls[0])
	}
	if mock.calls[0].source != "auto-extraction" {
		t.Errorf("source = %q, want %q", mock.calls[0].source, "auto-extraction")
	}
	if mock.calls[0].confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", mock.calls[0].confidence)
	}

	// Verify second fact
	if mock.calls[1].category != "user" || mock.calls[1].key != "partner_name" {
		t.Errorf("call[1] = %v, want user/partner_name", mock.calls[1])
	}
}

func TestExtract_SkipsWhenNotWorthPersisting(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return &ExtractionResult{
			WorthPersisting: false,
			Facts:           nil,
		}, nil
	})

	err := e.Extract(context.Background(), "turn on the light", "Done.", nil)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 SetFact calls, got %d", len(mock.calls))
	}
}

func TestExtract_SkipsIncompleteFacts(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return &ExtractionResult{
			WorthPersisting: true,
			Facts: []ExtractedFact{
				{Category: "", Key: "something", Value: "value", Confidence: 0.8},
				{Category: "user", Key: "", Value: "value", Confidence: 0.8},
				{Category: "user", Key: "name", Value: "", Confidence: 0.8},
				{Category: "preference", Key: "valid", Value: "valid fact", Confidence: 0.9},
			},
		}, nil
	})

	err := e.Extract(context.Background(), "test", "test response", nil)
	if err != nil {
		t.Fatalf("Extract() error: %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 SetFact call (only valid fact), got %d", len(mock.calls))
	}
	if mock.calls[0].key != "valid" {
		t.Errorf("expected valid fact, got key=%q", mock.calls[0].key)
	}
}

func TestExtract_HandlesLLMError(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return nil, fmt.Errorf("model timeout")
	})

	err := e.Extract(context.Background(), "test", "test response", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 SetFact calls on error, got %d", len(mock.calls))
	}
}

func TestExtract_NilExtractFunc(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	// Don't set extract func

	err := e.Extract(context.Background(), "test", "test response", nil)
	if err != nil {
		t.Fatalf("Extract() with nil func should be no-op, got error: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 SetFact calls, got %d", len(mock.calls))
	}
}

func TestExtract_NilResult(t *testing.T) {
	mock := &mockFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return nil, nil
	})

	err := e.Extract(context.Background(), "test", "test response", nil)
	if err != nil {
		t.Fatalf("Extract() with nil result should be no-op, got error: %v", err)
	}
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 SetFact calls, got %d", len(mock.calls))
	}
}

// errorFactSetter returns an error on SetFact to test error handling.
type errorFactSetter struct {
	calls int
}

func (e *errorFactSetter) SetFact(_, _, _, _ string, _ float64) error {
	e.calls++
	return errors.New("database locked")
}

func TestExtract_ContinuesOnSetFactError(t *testing.T) {
	mock := &errorFactSetter{}
	e := NewExtractor(mock, slog.Default(), 2)
	e.SetExtractFunc(func(_ context.Context, _, _ string, _ []Message) (*ExtractionResult, error) {
		return &ExtractionResult{
			WorthPersisting: true,
			Facts: []ExtractedFact{
				{Category: "user", Key: "fact1", Value: "val1", Confidence: 0.9},
				{Category: "user", Key: "fact2", Value: "val2", Confidence: 0.8},
			},
		}, nil
	})

	// Should not return error — SetFact errors are logged, not propagated.
	err := e.Extract(context.Background(), "test", "test response", nil)
	if err != nil {
		t.Fatalf("Extract() should not propagate SetFact errors, got: %v", err)
	}
	// Both facts should have been attempted.
	if mock.calls != 2 {
		t.Errorf("expected 2 SetFact attempts, got %d", mock.calls)
	}
}
