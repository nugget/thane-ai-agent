package outputtargets

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func testTarget() Target {
	return Target{
		ID:    "test.surface",
		Title: "Test surface",
		Slots: []Slot{
			{Name: "value", Kind: SlotKindText, MaxRunes: 6, Required: true, Primary: true, Description: "Headline."},
			{Name: "title", Kind: SlotKindText, MaxRunes: 10, Description: "Label."},
			{Name: "fraction", Kind: SlotKindFraction, Description: "Fill."},
			{Name: "tint", Kind: SlotKindColor, Description: "Color."},
		},
	}
}

func TestNormalizeSuccess(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want Payload
	}{
		{
			name: "primary only",
			args: map[string]any{"value": "72F"},
			want: Payload{State: "72F"},
		},
		{
			name: "all slots",
			args: map[string]any{"value": "72F", "title": "Office", "fraction": 0.5, "tint": "#3fb950"},
			want: Payload{State: "72F", Attributes: map[string]any{"title": "Office", "fraction": 0.5, "tint": "#3FB950"}},
		},
		{
			name: "whitespace trimmed",
			args: map[string]any{"value": "  72F  ", "title": "\tOffice\t"},
			want: Payload{State: "72F", Attributes: map[string]any{"title": "Office"}},
		},
		{
			name: "color without leading hash",
			args: map[string]any{"value": "72F", "tint": "3fb950"},
			want: Payload{State: "72F", Attributes: map[string]any{"tint": "#3FB950"}},
		},
		{
			name: "fraction as json.Number",
			args: map[string]any{"value": "72F", "fraction": json.Number("0.25")},
			want: Payload{State: "72F", Attributes: map[string]any{"fraction": 0.25}},
		},
		{
			name: "fraction as string",
			args: map[string]any{"value": "72F", "fraction": "1"},
			want: Payload{State: "72F", Attributes: map[string]any{"fraction": float64(1)}},
		},
		{
			name: "fraction as int",
			args: map[string]any{"value": "72F", "fraction": 0},
			want: Payload{State: "72F", Attributes: map[string]any{"fraction": float64(0)}},
		},
		{
			name: "explicit null clears an optional slot",
			args: map[string]any{"value": "72F", "title": nil},
			want: Payload{State: "72F"},
		},
		{
			name: "blank optional text clears the slot",
			args: map[string]any{"value": "72F", "title": "   "},
			want: Payload{State: "72F"},
		},
		{
			name: "multibyte value inside the rune budget",
			args: map[string]any{"value": "🌡22°C"},
			want: Payload{State: "🌡22°C"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := testTarget().Normalize(tt.args)
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Normalize = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalizeErrorsTeach(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "unknown slot lists the valid ones",
			args: map[string]any{"value": "72F", "subtitle": "nope"},
			want: []string{`no slot named "subtitle"`, `"value"`, `"fraction"`},
		},
		{
			name: "missing required slot",
			args: map[string]any{"title": "Office"},
			want: []string{`slot "value" is required`},
		},
		{
			name: "required slot blank after trimming",
			args: map[string]any{"value": "  "},
			want: []string{`slot "value" is required`, "empty after trimming"},
		},
		{
			name: "text over budget names the limit and the value",
			args: map[string]any{"value": "1234567"},
			want: []string{"is 7 characters", "at most 6", `"1234567"`},
		},
		{
			name: "text with a newline",
			args: map[string]any{"value": "72F\nx"},
			want: []string{"control character", "single line"},
		},
		{
			name: "text of the wrong type",
			args: map[string]any{"value": 72},
			want: []string{"expected a string"},
		},
		{
			name: "fraction out of range explains normalization",
			args: map[string]any{"value": "72F", "fraction": 42.0},
			want: []string{"must be between 0.0 and 1.0", "(value - minimum)"},
		},
		{
			name: "fraction of the wrong type",
			args: map[string]any{"value": "72F", "fraction": true},
			want: []string{"expected a number between 0.0 and 1.0"},
		},
		{
			name: "malformed color",
			args: map[string]any{"value": "72F", "tint": "greenish"},
			want: []string{"six-digit hex color", `"greenish"`},
		},
		{
			name: "short color",
			args: map[string]any{"value": "72F", "tint": "#fff"},
			want: []string{"six-digit hex color"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testTarget().Normalize(tt.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestNormalizeRejectsRatherThanTruncates(t *testing.T) {
	// The budget is a contract with the display, not a suggestion: a
	// silently shortened value would render as a mystery on the watch.
	_, err := testTarget().Normalize(map[string]any{"value": "1234567"})
	if err == nil {
		t.Fatal("expected an over-budget value to be rejected")
	}
	if strings.Contains(err.Error(), "truncat") && !strings.Contains(err.Error(), "rather than relying on truncation") {
		t.Fatalf("error should not imply truncation happened: %q", err)
	}
}

func TestNormalizeAgainstRegisteredTargets(t *testing.T) {
	rectangular, ok := Lookup("apple_watch.rectangular")
	if !ok {
		t.Fatal("apple_watch.rectangular is not registered")
	}
	payload, err := rectangular.Normalize(map[string]any{
		"value":       "64%",
		"title":       "Battery",
		"subtitle":    "House bank",
		"bottom_text": "charging",
		"fraction":    0.64,
		"gauge_color": "#3FB950",
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if payload.State != "64%" {
		t.Fatalf("state = %q, want %q", payload.State, "64%")
	}
	if payload.Attributes["fraction"] != 0.64 {
		t.Fatalf("fraction attribute = %v", payload.Attributes["fraction"])
	}
	if _, present := payload.Attributes["value"]; present {
		t.Fatal("the primary slot must be the state, not also an attribute")
	}

	circular, ok := Lookup("apple_watch.circular")
	if !ok {
		t.Fatal("apple_watch.circular is not registered")
	}
	if _, err := circular.Normalize(map[string]any{"value": "1013 hPa"}); err == nil {
		t.Fatal("expected the circular target to reject a value well over its budget")
	}
}

// TestPreviewBoundsModelSuppliedValues pins the size of an error, not
// its wording. Slot values arrive from tool arguments, so a runaway
// string would otherwise be echoed into the log and into the model's
// next turn — spending context at the exact moment something is already
// going wrong.
func TestPreviewBoundsModelSuppliedValues(t *testing.T) {
	huge := strings.Repeat("é", 5000)
	slot := Slot{Name: "title", Kind: SlotKindText, MaxRunes: 18}

	_, err := slot.normalizeText(huge)
	if err == nil {
		t.Fatal("an over-budget value should be refused")
	}
	if n := utf8.RuneCountInString(err.Error()); n > 300 {
		t.Errorf("error is %d characters; a bounded preview should keep it short", n)
	}
	if !strings.Contains(err.Error(), "5000 characters") {
		t.Errorf("the error should say how much arrived: %v", err)
	}

	// A short value is still quoted in full — the bound exists for the
	// runaway case, not to make ordinary errors vaguer.
	if _, err := slot.normalizeText("a value that is merely too long for this slot"); err == nil {
		t.Fatal("expected refusal")
	} else if !strings.Contains(err.Error(), "merely too long") {
		t.Errorf("a short value should appear in full: %v", err)
	}
}
