package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

type companionCallFunc func(ctx context.Context, req companion.CallRequest) (json.RawMessage, error)

// companionCallTimeout bounds a single dispatch to a connected companion.
//
// [companion.Registry.Call] deliberately imposes no deadline of its own —
// it waits on the caller's context, or on the provider disconnecting. That
// is the right division, but on an ordinary conversational turn there is no
// deadline to wait on: delegate profiles and loop definitions set
// ToolTimeout, while a Signal message or a web-console turn leaves it zero,
// and the loop only applies one when it is positive. A companion that is
// connected but not answering therefore hangs the turn outright.
//
// That state is not hypothetical. A Mac blocks inside EventKit until
// someone dismisses the macOS permission prompt, which on a laptop that is
// closed, locked, or simply elsewhere is never. A disconnect is already
// handled; being present and unresponsive was not.
//
// Thirty seconds is far longer than any real query — EventKit answers a
// window query in well under a second — and short enough that a wedged turn
// recovers on its own. It is deliberately not configurable: the value only
// has to separate "working" from "wedged", and a real query approaching it
// is a problem to surface rather than a number to tune.
const companionCallTimeout = 30 * time.Second

// callCompanion dispatches one request to a connected companion under
// [companionCallTimeout], translating a deadline this bound imposed into
// something the model can act on.
//
// A bare "context deadline exceeded" tells the model nothing it can use. It
// needs to know the Mac is reachable but silent, that a permission prompt
// is the usual reason, and that retrying immediately will not help — so it
// reports the situation rather than burning the turn on retries.
func callCompanion(ctx context.Context, call companionCallFunc, req companion.CallRequest) (json.RawMessage, error) {
	return callCompanionWithin(ctx, companionCallTimeout, call, req)
}

// callCompanionWithin is callCompanion with the bound supplied, so a test
// can drive a real expiry in milliseconds instead of standing in for one.
// The production bound stays the constant: this exists for tests, not for
// operators, and no caller outside them passes anything else.
func callCompanionWithin(ctx context.Context, timeout time.Duration, call companionCallFunc, req companion.CallRequest) (json.RawMessage, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := call(callCtx, req)
	if err == nil {
		return result, nil
	}

	// Claim the timeout only when this bound is demonstrably the reason.
	// All three conditions carry their own weight:
	//
	//   - the returned error is deadline-shaped, so a disconnect that raced
	//     the deadline is still reported as the disconnect it was;
	//   - this call's own context expired, so a caller that hands back
	//     DeadlineExceeded from somewhere else entirely — its own inner
	//     bound, a wrapped transport error — is not turned into a claim
	//     that a Mac sat silent for the full window;
	//   - the parent is still healthy, so a turn that ran out of time gets
	//     its own error back rather than the companion taking the blame.
	//
	// The middle one is the difference between reporting a fact and
	// inferring one from a error value that happens to match.
	if errors.Is(err, context.DeadlineExceeded) &&
		errors.Is(callCtx.Err(), context.DeadlineExceeded) &&
		ctx.Err() == nil {
		return nil, fmt.Errorf(
			"companion did not respond to %s/%s within %s; it is connected but not answering, "+
				"which usually means the Mac is asleep, locked, or waiting on a macOS permission prompt. "+
				"Report this rather than retrying — the next call will wait the same %s",
			req.Capability, req.Method, timeout, timeout)
	}
	return nil, err
}

type companionCalendarRequest struct {
	Start         string   `json:"start"`
	End           string   `json:"end"`
	CalendarNames []string `json:"calendar_names,omitempty"`
	Query         string   `json:"query,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

type companionCalendarResponse struct {
	Events []companionCalendarEvent `json:"events"`
}

type companionCalendarEvent struct {
	Title    string `json:"title"`
	Calendar string `json:"calendar"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day"`
	// TimeZone is the IANA zone the event is scheduled in, when the
	// companion knows one. On this operator's calendar it is a statement
	// of intent rather than incidental metadata: an event recorded in
	// Europe/Berlin means they expect to be in Berlin for it. Absent for
	// floating events and for companions predating the field.
	TimeZone     string `json:"time_zone,omitempty"`
	Location     string `json:"location,omitempty"`
	NotesExcerpt string `json:"notes_excerpt,omitempty"`
	URL          string `json:"url,omitempty"`
}

const (
	defaultCompanionCalendarLimit   = 20
	maxCompanionCalendarLimit       = 100
	maxCompanionCalendarResultBytes = 16_000
)

// EnableCompanionTools adds native companion app tools to the registry.
func (r *Registry) EnableCompanionTools(caller companionCallFunc) {
	r.companionCaller = caller
	r.registerCompanionTools()
}

func (r *Registry) registerCompanionTools() {
	if r.companionCaller == nil {
		return
	}

	r.Register(&Tool{
		Name: "macos_calendar_events",
		Description: "List calendar events from a connected macOS companion app. " +
			"Use this for upcoming availability, scheduled meetings, and calendar context when a macOS companion app is connected to Thane.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"description": "Optional account identity to target when multiple companion accounts are connected.",
				},
				"client_id": map[string]any{
					"type":        "string",
					"description": "Optional specific device/client_id to target when an account has multiple macOS hosts connected.",
				},
				"start": map[string]any{
					"type":        "string",
					"description": "Inclusive start of the calendar window. RFC3339, or a bare YYYY-MM-DD[ HH:MM] read in the household timezone. Defaults to now.",
				},
				"end": map[string]any{
					"type":        "string",
					"description": "Exclusive end of the calendar window. RFC3339, or a bare YYYY-MM-DD[ HH:MM] read in the household timezone. Defaults to 24 hours after start.",
				},
				"calendar_names": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Optional list of calendar display names to include.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive search term matched against event title, location, and notes.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of events to return. Default: %d. Max: %d.", defaultCompanionCalendarLimit, maxCompanionCalendarLimit),
				},
			},
		},
		Handler: r.handleMacOSCalendarEvents,
	})
}

func (r *Registry) handleMacOSCalendarEvents(ctx context.Context, args map[string]any) (string, error) {
	if r.companionCaller == nil {
		return "", fmt.Errorf("no native companion caller configured")
	}

	home := r.HomeLocation()
	now := time.Now().In(home)
	start, err := parseCompanionTimeArg(args, "start", home, now)
	if err != nil {
		return "", err
	}
	end, err := parseCompanionTimeArg(args, "end", home, start.Add(24*time.Hour))
	if err != nil {
		return "", err
	}
	if !end.After(start) {
		return "", fmt.Errorf("end must be after start")
	}

	limit := defaultCompanionCalendarLimit
	if raw, ok := args["limit"].(float64); ok {
		if raw != float64(int(raw)) {
			return "", fmt.Errorf("limit must be a whole number")
		}
		limit = int(raw)
	}
	if limit <= 0 {
		return "", fmt.Errorf("limit must be positive")
	}
	if limit > maxCompanionCalendarLimit {
		return "", fmt.Errorf("limit must be <= %d", maxCompanionCalendarLimit)
	}

	request := companionCalendarRequest{
		Start:         start.Format(time.RFC3339),
		End:           end.Format(time.RFC3339),
		CalendarNames: stringSliceArg(args, "calendar_names"),
		Query:         strings.TrimSpace(stringArg(args, "query")),
		Limit:         limit,
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("marshal calendar request: %w", err)
	}

	result, err := callCompanion(ctx, r.companionCaller, companion.CallRequest{
		Account:    strings.TrimSpace(stringArg(args, "account")),
		ClientID:   strings.TrimSpace(stringArg(args, "client_id")),
		Capability: "macos.calendar",
		Method:     "list_events",
		Params:     payload,
	})
	if err != nil {
		return "", err
	}

	var response companionCalendarResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return "", fmt.Errorf("decode companion calendar response: %w", err)
	}

	return formatCompanionCalendarResponse(response, home, now), nil
}

// companionTimeLayouts are the zone-less shapes accepted for a window
// bound, tried after RFC3339 and interpreted in the household zone.
//
// A model asked "what is on my calendar this afternoon" writes the hour it
// means, not the hour in UTC. Reading "2026-08-29 14:00:00" as UTC — which
// is what a zone-less parse defaults to — silently shifts the window by the
// household's offset and answers a question nobody asked.
var companionTimeLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseCompanionTimeArg reads one end of the requested window. A value
// carrying its own offset is taken at face value; one without is read in
// home, the zone the model is reasoning in.
func parseCompanionTimeArg(args map[string]any, key string, home *time.Location, fallback time.Time) (time.Time, error) {
	value := strings.TrimSpace(stringArg(args, key))
	if value == "" {
		return fallback, nil
	}
	if home == nil {
		home = time.Local
	}

	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts, nil
		}
	}
	for _, layout := range companionTimeLayouts {
		if ts, err := time.ParseInLocation(layout, value, home); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("%s must be a valid timestamp (RFC3339, or YYYY-MM-DD[ HH:MM[:SS]] read in %s) (got %q)", key, home, value)
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}

	values := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	return values
}
