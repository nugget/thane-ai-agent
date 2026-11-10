package prompts

import (
	"strings"
	"testing"
)

func TestUnchangedWrite(t *testing.T) {
	tests := []struct {
		name        string
		lastChanged string
		want        string
		wantAbsent  string
	}{
		{name: "names the age when known", lastChanged: "-40h2m", want: "no new revision was created and it still reads as last changed -40h2m. That is right only"},
		{name: "omits the age when unknown", want: "no new revision was created. That is right only", wantAbsent: "last changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnchangedWrite(tt.lastChanged)
			if !strings.HasPrefix(got, "unchanged: ") || !strings.Contains(got, tt.want) {
				t.Errorf("UnchangedWrite(%q) = %q, want it to start with \"unchanged: \" and contain %q", tt.lastChanged, got, tt.want)
			}
			if tt.wantAbsent != "" && strings.Contains(got, tt.wantAbsent) {
				t.Errorf("UnchangedWrite(%q) = %q, want no %q", tt.lastChanged, got, tt.wantAbsent)
			}
		})
	}
}
