package tools

import (
	"context"
	"strings"
	"testing"
)

// mockSessionManager captures args passed to CloseSession for verification.
type mockSessionManager struct {
	closedReason       string
	closedCarryForward string
	closedConvID       string
}

func (m *mockSessionManager) CloseSession(conversationID, reason, carryForward string) error {
	m.closedConvID = conversationID
	m.closedReason = reason
	m.closedCarryForward = carryForward
	return nil
}

func (m *mockSessionManager) CheckpointSession(string, string) error { return nil }
func (m *mockSessionManager) SplitSession(string, int, string) error { return nil }

func TestSessionClose_CarryForwardAlias(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string // expected carry_forward content
	}{
		{
			name: "canonical carry_forward",
			args: map[string]any{"carry_forward": "my handoff notes"},
			want: "my handoff notes",
		},
		{
			name: "handoff_note alias",
			args: map[string]any{"handoff_note": "explored features with user"},
			want: "explored features with user",
		},
		{
			name: "summary alias",
			args: map[string]any{"summary": "session summary here"},
			want: "session summary here",
		},
		{
			name: "handoff alias",
			args: map[string]any{"handoff": "handoff content"},
			want: "handoff content",
		},
		{
			name: "canonical takes priority over alias",
			args: map[string]any{
				"carry_forward": "canonical value",
				"handoff_note":  "alias value",
			},
			want: "canonical value",
		},
		{
			name: "empty carry_forward falls through to alias",
			args: map[string]any{
				"carry_forward": "",
				"handoff_note":  "alias value",
			},
			want: "alias value",
		},
		{
			name: "no carry_forward at all",
			args: map[string]any{"reason": "topic change"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &mockSessionManager{}
			reg := NewRegistry(nil, nil)
			reg.SetSessionManager(mgr)

			tool := reg.Get("session_close")
			if tool == nil {
				t.Fatal("session_close tool not registered")
			}

			ctx := WithConversationID(context.Background(), "test-conv")
			_, err := tool.Handler(ctx, tt.args)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}

			if mgr.closedCarryForward != tt.want {
				t.Errorf("carry_forward = %q, want %q", mgr.closedCarryForward, tt.want)
			}
		})
	}
}

func TestSessionClose_HonestResponse(t *testing.T) {
	tests := []struct {
		name         string
		carryForward string
		wantContains string
		wantExcludes string
	}{
		{
			name:         "with carry_forward",
			carryForward: "important notes",
			wantContains: "Carry-forward injected",
			wantExcludes: "WARNING",
		},
		{
			name:         "without carry_forward",
			carryForward: "",
			wantContains: "WARNING: No carry-forward content received",
			wantExcludes: "Carry-forward injected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &mockSessionManager{}
			reg := NewRegistry(nil, nil)
			reg.SetSessionManager(mgr)

			tool := reg.Get("session_close")
			ctx := WithConversationID(context.Background(), "test-conv")

			args := map[string]any{"reason": "test"}
			if tt.carryForward != "" {
				args["carry_forward"] = tt.carryForward
			}

			result, err := tool.Handler(ctx, args)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}

			if !strings.Contains(result, tt.wantContains) {
				t.Errorf("result should contain %q\ngot: %s", tt.wantContains, result)
			}
			if strings.Contains(result, tt.wantExcludes) {
				t.Errorf("result should NOT contain %q\ngot: %s", tt.wantExcludes, result)
			}
		})
	}
}
