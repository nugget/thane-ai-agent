package loop

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	definitionEligibilityStateEligible   = "eligible"
	definitionEligibilityStateIneligible = "ineligible"
)

// Conditions describes optional runtime eligibility gates for a loop
// definition. The first supported condition family is schedule-based,
// and additional condition families can be added here over time
// without changing the surrounding loops-ng contract shape.
type Conditions struct {
	// Schedule constrains when the definition is currently eligible for
	// runtime use. When unset, the definition is always eligible unless
	// blocked by policy.
	Schedule *ScheduleCondition `yaml:"schedule,omitempty" json:"schedule,omitempty"`
}

// ScheduleCondition constrains definition eligibility to one or more
// recurring local-time windows.
type ScheduleCondition struct {
	// Timezone is the IANA timezone used to interpret window start and
	// end times. Empty means the local system timezone.
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	// Windows are the recurring day/time windows during which the
	// definition is eligible to run or launch.
	Windows []ScheduleWindow `yaml:"windows,omitempty" json:"windows,omitempty"`
}

// ScheduleWindow is one recurring local-time eligibility window.
type ScheduleWindow struct {
	// Days limits the window to specific weekdays using stable short
	// names such as mon, tue, wed, thu, fri, sat, and sun. Empty means
	// every day.
	Days []string `yaml:"days,omitempty" json:"days,omitempty"`
	// Start is the local wall-clock start time in HH:MM 24-hour form.
	Start string `yaml:"start,omitempty" json:"start,omitempty"`
	// End is the local wall-clock end time in HH:MM 24-hour form. When
	// End is earlier than Start, the window crosses midnight into the
	// next day.
	End string `yaml:"end,omitempty" json:"end,omitempty"`
}

// DefinitionEligibilityStatus is the effective runtime eligibility for
// one stored loop definition at a point in time.
type DefinitionEligibilityStatus struct {
	// Eligible reports whether the definition is currently eligible to
	// run or launch, before policy and operation-specific logic are
	// applied.
	Eligible bool `yaml:"eligible" json:"eligible"`
	// Reason describes why the definition is currently ineligible, when
	// known. Empty means either eligible or no explanatory detail.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
	// NextTransitionAt is the next time the eligibility result is
	// expected to change, such as the next schedule boundary.
	NextTransitionAt time.Time `yaml:"next_transition_at,omitempty" json:"next_transition_at,omitempty"`
}

// IneligibleDefinitionError reports that a loop definition exists and
// is active by policy, but its runtime conditions do not currently
// permit launch.
type IneligibleDefinitionError struct {
	Name   string
	Reason string
}

func (e *IneligibleDefinitionError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		return fmt.Sprintf("loop: definition %q is not currently eligible", e.Name)
	}
	return fmt.Sprintf("loop: definition %q is not currently eligible: %s", e.Name, reason)
}

// Validate checks that all configured runtime conditions are
// structurally valid.
func (c Conditions) Validate() error {
	if c.Schedule != nil {
		if err := c.Schedule.Validate(); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
	}
	return nil
}

// Evaluate returns the effective eligibility of the conditions at the
// provided time.
func (c Conditions) Evaluate(now time.Time) DefinitionEligibilityStatus {
	if c.Schedule == nil {
		return DefinitionEligibilityStatus{Eligible: true}
	}
	return c.Schedule.Evaluate(now)
}

// Validate checks that the schedule condition is well-formed.
func (s *ScheduleCondition) Validate() error {
	if s == nil {
		return nil
	}
	if _, err := s.location(); err != nil {
		return err
	}
	if len(s.Windows) == 0 {
		return fmt.Errorf("at least one schedule window is required")
	}
	for i, window := range s.Windows {
		if err := window.Validate(); err != nil {
			return fmt.Errorf("windows[%d]: %w", i, err)
		}
	}
	return nil
}

// Evaluate returns the effective eligibility of the schedule at the
// provided time.
func (s *ScheduleCondition) Evaluate(now time.Time) DefinitionEligibilityStatus {
	if s == nil {
		return DefinitionEligibilityStatus{Eligible: true}
	}

	loc, err := s.location()
	if err != nil {
		return DefinitionEligibilityStatus{
			Eligible: false,
			Reason:   err.Error(),
		}
	}

	parsed, err := s.parsedWindows()
	if err != nil {
		return DefinitionEligibilityStatus{
			Eligible: false,
			Reason:   err.Error(),
		}
	}

	current := scheduleWindowsActive(parsed, now.In(loc))
	next := nextScheduleTransition(parsed, now.In(loc))
	status := DefinitionEligibilityStatus{
		Eligible:         current,
		NextTransitionAt: next.UTC(),
	}
	if !current {
		status.Reason = "outside scheduled windows"
	}
	if next.IsZero() {
		status.NextTransitionAt = time.Time{}
	}
	return status
}

func (s *ScheduleCondition) location() (*time.Location, error) {
	if s == nil || strings.TrimSpace(s.Timezone) == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(strings.TrimSpace(s.Timezone))
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q", s.Timezone)
	}
	return loc, nil
}

func (s *ScheduleCondition) parsedWindows() ([]parsedScheduleWindow, error) {
	if s == nil {
		return nil, nil
	}
	windows := make([]parsedScheduleWindow, 0, len(s.Windows))
	for i, window := range s.Windows {
		parsed, err := parseScheduleWindow(window)
		if err != nil {
			return nil, fmt.Errorf("windows[%d]: %w", i, err)
		}
		windows = append(windows, parsed)
	}
	return windows, nil
}

// Validate checks that the schedule window is structurally valid.
func (w ScheduleWindow) Validate() error {
	_, err := parseScheduleWindow(w)
	return err
}

type parsedScheduleWindow struct {
	days        []time.Weekday
	startMinute int
	endMinute   int
}

func parseScheduleWindow(window ScheduleWindow) (parsedScheduleWindow, error) {
	startMinute, err := parseScheduleClock(window.Start)
	if err != nil {
		return parsedScheduleWindow{}, fmt.Errorf("start: %w", err)
	}
	endMinute, err := parseScheduleClock(window.End)
	if err != nil {
		return parsedScheduleWindow{}, fmt.Errorf("end: %w", err)
	}
	if startMinute == endMinute {
		return parsedScheduleWindow{}, fmt.Errorf("start and end must describe a non-zero window")
	}
	days, err := parseScheduleDays(window.Days)
	if err != nil {
		return parsedScheduleWindow{}, err
	}
	return parsedScheduleWindow{
		days:        days,
		startMinute: startMinute,
		endMinute:   endMinute,
	}, nil
}

func parseScheduleClock(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("time is required")
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("must use HH:MM 24-hour format")
	}
	hour, err := parseBoundedInt(parts[0], 0, 23)
	if err != nil {
		return 0, fmt.Errorf("invalid hour %q", parts[0])
	}
	minute, err := parseBoundedInt(parts[1], 0, 59)
	if err != nil {
		return 0, fmt.Errorf("invalid minute %q", parts[1])
	}
	return (hour * 60) + minute, nil
}

func parseBoundedInt(raw string, min, max int) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	value := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("not numeric")
		}
		value = (value * 10) + int(ch-'0')
	}
	if value < min || value > max {
		return 0, fmt.Errorf("out of range")
	}
	return value, nil
}

func parseScheduleDays(raw []string) ([]time.Weekday, error) {
	if len(raw) == 0 {
		return []time.Weekday{
			time.Sunday,
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
			time.Saturday,
		}, nil
	}

	days := make([]time.Weekday, 0, len(raw))
	seen := map[time.Weekday]bool{}
	for _, day := range raw {
		weekday, err := parseWeekday(day)
		if err != nil {
			return nil, err
		}
		if seen[weekday] {
			continue
		}
		seen[weekday] = true
		days = append(days, weekday)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	return days, nil
}

func parseWeekday(raw string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tues", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return time.Sunday, fmt.Errorf("unsupported weekday %q", raw)
	}
}

func scheduleWindowsActive(windows []parsedScheduleWindow, now time.Time) bool {
	base := midnight(now)
	for _, window := range windows {
		for _, dayOffset := range []int{-1, 0} {
			day := base.AddDate(0, 0, dayOffset)
			if !window.appliesOn(day.Weekday()) {
				continue
			}
			start, end := window.interval(day, now.Location())
			if !now.Before(start) && now.Before(end) {
				return true
			}
		}
	}
	return false
}

func nextScheduleTransition(windows []parsedScheduleWindow, now time.Time) time.Time {
	current := scheduleWindowsActive(windows, now)
	candidates := collectScheduleBoundaries(windows, now)
	for _, candidate := range candidates {
		if !candidate.After(now) {
			continue
		}
		if scheduleWindowsActive(windows, candidate.Add(time.Nanosecond)) != current {
			return candidate
		}
	}
	return time.Time{}
}

func collectScheduleBoundaries(windows []parsedScheduleWindow, now time.Time) []time.Time {
	base := midnight(now)
	candidates := make([]time.Time, 0, len(windows)*18)
	seen := map[time.Time]bool{}
	for _, window := range windows {
		for dayOffset := -1; dayOffset <= 8; dayOffset++ {
			day := base.AddDate(0, 0, dayOffset)
			if !window.appliesOn(day.Weekday()) {
				continue
			}
			start, end := window.interval(day, now.Location())
			if !start.IsZero() && !seen[start] {
				seen[start] = true
				candidates = append(candidates, start)
			}
			if !end.IsZero() && !seen[end] {
				seen[end] = true
				candidates = append(candidates, end)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func (w parsedScheduleWindow) appliesOn(day time.Weekday) bool {
	return slices.Contains(w.days, day)
}

func (w parsedScheduleWindow) interval(day time.Time, loc *time.Location) (time.Time, time.Time) {
	start := day.Add(time.Duration(w.startMinute) * time.Minute)
	endDay := day
	if w.endMinute <= w.startMinute {
		endDay = day.AddDate(0, 0, 1)
	}
	end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 0, 0, 0, 0, loc).
		Add(time.Duration(w.endMinute) * time.Minute)
	return start, end
}

func cloneConditions(src Conditions) Conditions {
	clone := src
	if src.Schedule == nil {
		return clone
	}
	schedule := *src.Schedule
	schedule.Windows = make([]ScheduleWindow, 0, len(src.Schedule.Windows))
	for _, window := range src.Schedule.Windows {
		cloneWindow := window
		cloneWindow.Days = append([]string(nil), window.Days...)
		schedule.Windows = append(schedule.Windows, cloneWindow)
	}
	clone.Schedule = &schedule
	return clone
}
