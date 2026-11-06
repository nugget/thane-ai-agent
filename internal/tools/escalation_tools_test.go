package tools

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// newTestHumanEscalationDeps returns the four notification dependencies
// request_human_escalation needs, over an in-memory record store.
func newTestHumanEscalationDeps(t *testing.T) EscalationDeps {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	records, err := notifications.NewRecordStore(db, slog.Default())
	if err != nil {
		t.Fatalf("NewRecordStore: %v", err)
	}
	resolver := &mockNotifyContacts{contact: &contacts.Contact{ID: uuid.New(), FormattedName: "Alice"}}
	return EscalationDeps{
		Router:     notifications.NewNotificationRouter(resolver, records, slog.Default()),
		Records:    records,
		Dispatcher: notifications.NewCallbackDispatcher(records, nil, nil, "test-thane", slog.Default()),
		Waiter:     notifications.NewResponseWaiter(),
	}
}

func stubAdvise(context.Context, AIEscalationRequest) (string, error) {
	return "advice", nil
}

// TestSetEscalationToolsRegistersOnlyUsableTools pins the split guard:
// each escalation tool is registered exactly when its own dependencies
// are present, so a loop is never offered request_ai_escalation without
// an advisor behind it, and the advisor never depends on notifications.
func TestSetEscalationToolsRegistersOnlyUsableTools(t *testing.T) {
	tests := []struct {
		name          string
		notifications bool
		advise        func(context.Context, AIEscalationRequest) (string, error)
		wantHuman     bool
		wantAI        bool
	}{
		{name: "notification deps without advisor", notifications: true, advise: nil, wantHuman: true, wantAI: false},
		{name: "notification deps with advisor", notifications: true, advise: stubAdvise, wantHuman: true, wantAI: true},
		{name: "advisor without notification deps", notifications: false, advise: stubAdvise, wantHuman: false, wantAI: true},
		{name: "no deps", notifications: false, advise: nil, wantHuman: false, wantAI: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deps EscalationDeps
			if tt.notifications {
				deps = newTestHumanEscalationDeps(t)
			}
			deps.Advise = tt.advise

			reg := NewEmptyRegistry()
			reg.SetEscalationTools(deps)

			if got := reg.Get("request_human_escalation") != nil; got != tt.wantHuman {
				t.Errorf("request_human_escalation registered = %v, want %v", got, tt.wantHuman)
			}
			if got := reg.Get("request_ai_escalation") != nil; got != tt.wantAI {
				t.Errorf("request_ai_escalation registered = %v, want %v", got, tt.wantAI)
			}
		})
	}
}

// TestSetEscalationToolsPartialNotificationDeps pins that the human
// escalation still needs all four notification dependencies: any one
// missing withholds it.
func TestSetEscalationToolsPartialNotificationDeps(t *testing.T) {
	tests := []struct {
		name string
		drop func(*EscalationDeps)
	}{
		{name: "no router", drop: func(d *EscalationDeps) { d.Router = nil }},
		{name: "no records", drop: func(d *EscalationDeps) { d.Records = nil }},
		{name: "no dispatcher", drop: func(d *EscalationDeps) { d.Dispatcher = nil }},
		{name: "no waiter", drop: func(d *EscalationDeps) { d.Waiter = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := newTestHumanEscalationDeps(t)
			tt.drop(&deps)

			reg := NewEmptyRegistry()
			reg.SetEscalationTools(deps)

			if reg.Get("request_human_escalation") != nil {
				t.Error("request_human_escalation registered with an incomplete notification set")
			}
		})
	}
}

// TestAIEscalationHandlerCallsAdvise pins that request_ai_escalation
// hands the model's arguments to Advise and returns its answer, rather
// than a not-implemented stub.
func TestAIEscalationHandlerCallsAdvise(t *testing.T) {
	adviseErr := errors.New("advisor unreachable")
	tests := []struct {
		name      string
		args      map[string]any
		answer    string
		err       error
		wantReq   *AIEscalationRequest
		want      string
		wantErrIn string
	}{
		{
			name: "full arguments",
			args: map[string]any{
				"question":    "  Should the heater stay on overnight?  ",
				"context":     " Forecast low is -5C. ",
				"constraints": []any{"keep the cost down", "", "keep the cost down", "no window opening"},
			},
			answer: "Leave it on at a lower setpoint.",
			wantReq: &AIEscalationRequest{
				Question:    "Should the heater stay on overnight?",
				Context:     "Forecast low is -5C.",
				Constraints: []string{"keep the cost down", "no window opening"},
			},
			want: "Leave it on at a lower setpoint.",
		},
		{
			name:    "question only",
			args:    map[string]any{"question": "Which plan is safer?"},
			answer:  "Plan B.",
			wantReq: &AIEscalationRequest{Question: "Which plan is safer?"},
			want:    "Plan B.",
		},
		{
			name:      "missing question never reaches the advisor",
			args:      map[string]any{"context": "something"},
			wantErrIn: "question is required",
		},
		{
			name:      "blank question never reaches the advisor",
			args:      map[string]any{"question": "   "},
			wantErrIn: "question is required",
		},
		{
			name:      "advisor error is surfaced",
			args:      map[string]any{"question": "Which plan is safer?"},
			err:       adviseErr,
			wantReq:   &AIEscalationRequest{Question: "Which plan is safer?"},
			wantErrIn: "advisor unreachable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *AIEscalationRequest
			reg := NewEmptyRegistry()
			reg.SetEscalationTools(EscalationDeps{
				Advise: func(_ context.Context, req AIEscalationRequest) (string, error) {
					got = &req
					return tt.answer, tt.err
				},
			})
			tool := reg.Get("request_ai_escalation")
			if tool == nil {
				t.Fatal("request_ai_escalation not registered with Advise set")
			}

			out, err := tool.Handler(context.Background(), tt.args)

			if tt.wantErrIn != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrIn) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErrIn)
				}
				if tt.err != nil && !errors.Is(err, tt.err) {
					t.Errorf("err = %v, want it to wrap %v", err, tt.err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out != tt.want {
				t.Errorf("result = %q, want %q", out, tt.want)
			}
			if !reflect.DeepEqual(got, tt.wantReq) {
				t.Errorf("Advise request = %+v, want %+v", got, tt.wantReq)
			}
		})
	}
}
