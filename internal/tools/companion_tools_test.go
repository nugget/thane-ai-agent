package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

type fakeCompanionCaller struct {
	req    companion.CallRequest
	result json.RawMessage
	err    error
}

func (f *fakeCompanionCaller) Call(_ context.Context, req companion.CallRequest) (json.RawMessage, error) {
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestSetCompanionCallerRegistersCalendarTool(t *testing.T) {
	caller := &fakeCompanionCaller{
		result: json.RawMessage(`{
			"events": [
				{
					"title": "Design review",
					"calendar": "Work",
					"start": "2026-04-02T09:00:00-05:00",
					"end": "2026-04-02T10:00:00-05:00",
					"location": "Conference Room"
				}
			]
		}`),
	}

	reg := NewEmptyRegistry()
	reg.EnableCompanionTools(caller.Call)

	tool := reg.Get("macos_calendar_events")
	if tool == nil {
		t.Fatal("expected macos_calendar_events to be registered")
	}
	if tool.Core {
		t.Fatal("expected macos_calendar_events to rely on capability tags instead of being a core tool")
	}

	output, err := reg.Execute(context.Background(), "macos_calendar_events", `{
		"account": "nugget",
		"start": "2026-04-02T08:00:00-05:00",
		"end": "2026-04-02T18:00:00-05:00",
		"calendar_names": ["Work", "Personal"],
		"query": "design",
		"limit": 5
	}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if caller.req.Account != "nugget" {
		t.Fatalf("account: got %q, want %q", caller.req.Account, "nugget")
	}
	if caller.req.Capability != "macos.calendar" {
		t.Fatalf("capability: got %q, want %q", caller.req.Capability, "macos.calendar")
	}
	if caller.req.Method != "list_events" {
		t.Fatalf("method: got %q, want %q", caller.req.Method, "list_events")
	}

	var forwarded companionCalendarRequest
	if err := json.Unmarshal(caller.req.Params, &forwarded); err != nil {
		t.Fatalf("unmarshal forwarded params: %v", err)
	}
	if len(forwarded.CalendarNames) != 2 {
		t.Fatalf("calendar_names: got %v", forwarded.CalendarNames)
	}
	if forwarded.Query != "design" {
		t.Fatalf("query: got %q, want %q", forwarded.Query, "design")
	}
	if forwarded.Limit != 5 {
		t.Fatalf("limit: got %d, want %d", forwarded.Limit, 5)
	}

	for _, part := range []string{
		"Found 1 macOS calendar events",
		"Design review",
		"Conference Room",
	} {
		if !strings.Contains(output, part) {
			t.Fatalf("expected output to contain %q, got: %s", part, output)
		}
	}
}

func TestMacOSCalendarEventsPropagatesProviderError(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.EnableCompanionTools((&fakeCompanionCaller{
		err: errors.New("no connected companion app supports macos.calendar/list_events"),
	}).Call)

	_, err := reg.Execute(context.Background(), "macos_calendar_events", `{}`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no connected companion app") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMacOSCalendarEventsRejectsLimitOverMax(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.EnableCompanionTools((&fakeCompanionCaller{}).Call)

	_, err := reg.Execute(context.Background(), "macos_calendar_events", `{"limit":101}`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "limit must be <=") {
		t.Fatalf("unexpected error: %v", err)
	}
}
