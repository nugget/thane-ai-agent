package promptfmt

import (
	"errors"
	"fmt"
	"testing"
)

func TestModelFacingError(t *testing.T) {
	sentinel := errors.New("operator detail: /var/data/x.jpg")
	carrier := &ModelFacingError{Text: "the image could not be read", Err: sentinel}

	tests := []struct {
		name     string
		err      error
		wantText string
		wantOK   bool
	}{
		{"carrier", carrier, "the image could not be read", true},
		{"wrapped carrier", fmt.Errorf("attachment_describe: %w", carrier), "the image could not be read", true},
		{"carrier without text", &ModelFacingError{Err: sentinel}, "", false},
		{"plain error", sentinel, "", false},
		{"nil", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, ok := ModelFacingText(tt.err)
			if text != tt.wantText || ok != tt.wantOK {
				t.Errorf("ModelFacingText = (%q, %v), want (%q, %v)", text, ok, tt.wantText, tt.wantOK)
			}
		})
	}

	if got := carrier.Error(); got != sentinel.Error() {
		t.Errorf("Error() = %q, want the wrapped error's text %q", got, sentinel.Error())
	}
	if !errors.Is(carrier, sentinel) {
		t.Error("errors.Is cannot see the wrapped error")
	}
}
