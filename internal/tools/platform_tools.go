package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform"
)

type platformCallFunc func(ctx context.Context, req platform.CallRequest) (json.RawMessage, error)

type platformCalendarRequest struct {
	Start         string   `json:"start"`
	End           string   `json:"end"`
	CalendarNames []string `json:"calendar_names,omitempty"`
	Query         string   `json:"query,omitempty"`
	Limit         int      `json:"limit,omitempty"`
}

type platformCalendarResponse struct {
	Events []platformCalendarEvent `json:"events"`
}

type platformCalendarEvent struct {
	Title        string `json:"title"`
	Calendar     string `json:"calendar"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Location     string `json:"location,omitempty"`
	NotesExcerpt string `json:"notes_excerpt,omitempty"`
	URL          string `json:"url,omitempty"`
}

const (
	defaultPlatformCalendarLimit = 20
	maxPlatformCalendarLimit     = 100
	maxPlatformCalendarResultLen = 16_000
)

// EnablePlatformTools adds native platform-host tools to the registry.
func (r *Registry) EnablePlatformTools(caller platformCallFunc) {
	r.platformCaller = caller
	r.registerPlatformTools()
}

func (r *Registry) registerPlatformTools() {
	if r.platformCaller == nil {
		return
	}

	r.Register(&Tool{
		Name:            "macos_calendar_events",
		AlwaysAvailable: true,
		Description: "List calendar events from a connected macOS platform host. " +
			"Use this for upcoming availability, scheduled meetings, and calendar context when a macOS provider is connected to Thane.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"description": "Optional account identity to target when multiple platform accounts are connected.",
				},
				"client_id": map[string]any{
					"type":        "string",
					"description": "Optional specific device/client_id to target when an account has multiple macOS hosts connected.",
				},
				"start": map[string]any{
					"type":        "string",
					"description": "Inclusive start of the calendar window in RFC3339 format. Defaults to now.",
				},
				"end": map[string]any{
					"type":        "string",
					"description": "Exclusive end of the calendar window in RFC3339 format. Defaults to 24 hours after start.",
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
					"description": fmt.Sprintf("Maximum number of events to return. Default: %d. Max: %d.", defaultPlatformCalendarLimit, maxPlatformCalendarLimit),
				},
			},
		},
		Handler: r.handleMacOSCalendarEvents,
	})
}

func (r *Registry) handleMacOSCalendarEvents(ctx context.Context, args map[string]any) (string, error) {
	if r.platformCaller == nil {
		return "", fmt.Errorf("no native platform caller configured")
	}

	now := time.Now()
	start, err := parsePlatformTimeArg(args, "start", now)
	if err != nil {
		return "", err
	}
	end, err := parsePlatformTimeArg(args, "end", start.Add(24*time.Hour))
	if err != nil {
		return "", err
	}
	if !end.After(start) {
		return "", fmt.Errorf("end must be after start")
	}

	limit := defaultPlatformCalendarLimit
	if raw, ok := args["limit"].(float64); ok {
		if raw != float64(int(raw)) {
			return "", fmt.Errorf("limit must be a whole number")
		}
		limit = int(raw)
	}
	if limit <= 0 {
		return "", fmt.Errorf("limit must be positive")
	}
	if limit > maxPlatformCalendarLimit {
		return "", fmt.Errorf("limit must be <= %d", maxPlatformCalendarLimit)
	}

	request := platformCalendarRequest{
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

	result, err := r.platformCaller(ctx, platform.CallRequest{
		Account:    strings.TrimSpace(stringArg(args, "account")),
		ClientID:   strings.TrimSpace(stringArg(args, "client_id")),
		Capability: "macos.calendar",
		Method:     "list_events",
		Params:     payload,
	})
	if err != nil {
		return "", err
	}

	var response platformCalendarResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return "", fmt.Errorf("decode platform calendar response: %w", err)
	}

	return formatPlatformCalendarResponse(response), nil
}

func parsePlatformTimeArg(args map[string]any, key string, fallback time.Time) (time.Time, error) {
	value := strings.TrimSpace(stringArg(args, key))
	if value == "" {
		return fallback, nil
	}

	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts, nil
	}
	return time.Time{}, fmt.Errorf("%s must be RFC3339 (got %q)", key, value)
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

func formatPlatformCalendarResponse(response platformCalendarResponse) string {
	if len(response.Events) == 0 {
		return "No macOS calendar events found in the requested window."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d macOS calendar events:", len(response.Events))
	for _, event := range response.Events {
		fmt.Fprintf(&b, "\n- %s | %s", formatPlatformCalendarRange(event), strings.TrimSpace(event.Title))
		if event.Calendar != "" {
			fmt.Fprintf(&b, " (%s)", event.Calendar)
		}
		if event.Location != "" {
			fmt.Fprintf(&b, "\n  Location: %s", event.Location)
		}
		if event.NotesExcerpt != "" {
			fmt.Fprintf(&b, "\n  Notes: %s", event.NotesExcerpt)
		}
		if event.URL != "" {
			fmt.Fprintf(&b, "\n  URL: %s", event.URL)
		}
	}
	formatted := b.String()
	if len(formatted) <= maxPlatformCalendarResultLen {
		return formatted
	}
	const note = "\n\n[... output truncated; narrow the window, filters, or limit for more ...]"
	allowed := maxPlatformCalendarResultLen - len(note)
	if allowed < 0 {
		allowed = 0
	}
	return truncateUTF8(formatted, allowed) + note
}

func formatPlatformCalendarRange(event platformCalendarEvent) string {
	start, startErr := parseCalendarTimestamp(event.Start)
	end, endErr := parseCalendarTimestamp(event.End)
	if startErr != nil {
		if event.End == "" {
			return event.Start
		}
		return strings.TrimSpace(event.Start + " - " + event.End)
	}

	if event.AllDay {
		return formatPlatformCalendarAllDayRange(start, end, endErr)
	}

	if endErr != nil || event.End == "" {
		return start.Format("Mon Jan 2 3:04PM MST")
	}
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return fmt.Sprintf("%s-%s", start.Format("Mon Jan 2 3:04PM MST"), end.Format("3:04PM MST"))
	}
	return fmt.Sprintf("%s -> %s", start.Format("Mon Jan 2 3:04PM MST"), end.Format("Mon Jan 2 3:04PM MST"))
}

func formatPlatformCalendarAllDayRange(start, end time.Time, endErr error) string {
	if endErr != nil || !end.After(start) {
		return fmt.Sprintf("%s (all day)", start.Format("Mon Jan 2"))
	}

	// EventKit-style all-day events use an exclusive end date, so show
	// the previous day when the range spans multiple days.
	lastDay := end.Add(-24 * time.Hour)
	if lastDay.Before(start) {
		lastDay = start
	}
	if start.Year() == lastDay.Year() && start.YearDay() == lastDay.YearDay() {
		return fmt.Sprintf("%s (all day)", start.Format("Mon Jan 2"))
	}
	return fmt.Sprintf("%s -> %s (all day)", start.Format("Mon Jan 2"), lastDay.Format("Mon Jan 2"))
}

func parseCalendarTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
