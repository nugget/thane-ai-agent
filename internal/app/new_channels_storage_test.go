package app

import (
	"testing"

	sigcli "github.com/nugget/thane-ai-agent/internal/channels/messaging/signal"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
)

// TestSignalVisionAnalyzer checks that a disabled vision analyzer reaches
// the Signal bridge as a nil interface, which the bridge's nil check can
// see, and an enabled one reaches it unchanged.
func TestSignalVisionAnalyzer(t *testing.T) {
	enabled := &attachments.Analyzer{}
	tests := []struct {
		name string
		in   *attachments.Analyzer
		want sigcli.VisionAnalyzer
	}{
		{name: "vision disabled", in: nil, want: nil},
		{name: "vision enabled", in: enabled, want: enabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := signalVisionAnalyzer(tt.in)
			if got != tt.want {
				t.Errorf("signalVisionAnalyzer(%v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
