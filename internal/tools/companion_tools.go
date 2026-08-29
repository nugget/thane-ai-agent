package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

type companionCallFunc func(ctx context.Context, req companion.CallRequest) (json.RawMessage, error)

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

	result, err := r.companionCaller(ctx, companion.CallRequest{
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
