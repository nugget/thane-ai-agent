package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestWithErrorClass(t *testing.T) {
	t.Parallel()

	inner := fmt.Errorf("decode: %w", context.DeadlineExceeded)

	tests := []struct {
		name        string
		class       error
		err         error
		wantNil     bool
		wantText    string
		wantIs      []error
		wantIsNot   []error
		wantAPIErr  bool
		wantBilling bool
	}{
		{
			name:      "empty completion keeps provider text",
			class:     ErrEmptyCompletion,
			err:       errors.New(`test returned an empty assistant completion for model "m"`),
			wantText:  `test returned an empty assistant completion for model "m"`,
			wantIs:    []error{ErrEmptyCompletion},
			wantIsNot: []error{ErrReasoningOnly},
		},
		{
			name:      "reasoning only keeps provider text",
			class:     ErrReasoningOnly,
			err:       errors.New("test produced only reasoning for model \"m\""),
			wantText:  "test produced only reasoning for model \"m\"",
			wantIs:    []error{ErrReasoningOnly},
			wantIsNot: []error{ErrEmptyCompletion},
		},
		{
			name:      "original chain stays reachable",
			class:     ErrEmptyCompletion,
			err:       inner,
			wantText:  inner.Error(),
			wantIs:    []error{ErrEmptyCompletion, inner, context.DeadlineExceeded},
			wantIsNot: []error{ErrReasoningOnly},
		},
		{
			name:        "errors.As still finds typed causes",
			class:       ErrReasoningOnly,
			err:         fmt.Errorf("wrapped: %w", &APIError{StatusCode: 400, Body: "x", Billing: true}),
			wantText:    "wrapped: API error 400: x",
			wantIs:      []error{ErrReasoningOnly},
			wantIsNot:   []error{ErrEmptyCompletion},
			wantAPIErr:  true,
			wantBilling: true,
		},
		{
			name:    "nil error stays nil",
			class:   ErrEmptyCompletion,
			err:     nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := WithErrorClass(tt.class, tt.err)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("WithErrorClass(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("WithErrorClass returned nil for a non-nil error")
			}
			if got.Error() != tt.wantText {
				t.Errorf("Error() = %q, want %q", got.Error(), tt.wantText)
			}
			for _, target := range tt.wantIs {
				if !errors.Is(got, target) {
					t.Errorf("errors.Is(%q, %v) = false, want true", got, target)
				}
			}
			for _, target := range tt.wantIsNot {
				if errors.Is(got, target) {
					t.Errorf("errors.Is(%q, %v) = true, want false", got, target)
				}
			}
			var apiErr *APIError
			if errors.As(got, &apiErr) != tt.wantAPIErr {
				t.Errorf("errors.As(*APIError) = %t, want %t", !tt.wantAPIErr, tt.wantAPIErr)
			}
			if IsBillingBlocked(got) != tt.wantBilling {
				t.Errorf("IsBillingBlocked = %t, want %t", !tt.wantBilling, tt.wantBilling)
			}
		})
	}
}
