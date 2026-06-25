package messages

import (
	"fmt"
	"strings"
	"time"
)

// MaxLoopEventsPerWake is the largest structured event batch that should be
// delivered in one loop wake. The task-loop renderer exposes this many events
// directly in the model prompt, so source pollers must split larger backlogs
// into multiple wakes and advance their cursors only after each batch is
// accepted.
const MaxLoopEventsPerWake = 50

// LoopEventPayload is structured event-source context delivered through a
// loop notification. Event producers use it for durable facts such as source,
// type, title, URL, and source-specific metadata; task-based loops receive the
// same data in the notification JSON rendered into their next prompt.
type LoopEventPayload struct {
	Source     string            `json:"source"`
	Type       string            `json:"type"`
	ID         string            `json:"id,omitempty"`
	Title      string            `json:"title,omitempty"`
	URL        string            `json:"url,omitempty"`
	Summary    string            `json:"summary,omitempty"`
	ObservedAt time.Time         `json:"observed_at,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// LoopWakeTarget identifies an existing loop that should receive an
// event-source wake. It mirrors thane_wake routing but is embeddable in
// source-specific subscription records.
//
// YAML and JSON tags are kept in lockstep so the same snake_case field
// names work in operator-edited config files (loaded as YAML by
// internal/platform/config) and in tool-call JSON arguments from the
// model. Field names like loop_id and force_supervisor would otherwise
// silently degrade to lowercase concatenation under default YAML
// reflection.
type LoopWakeTarget struct {
	LoopID          string   `json:"loop_id,omitempty" yaml:"loop_id,omitempty"`
	Name            string   `json:"name,omitempty" yaml:"name,omitempty"`
	ForceSupervisor bool     `json:"force_supervisor,omitempty" yaml:"force_supervisor,omitempty"`
	Priority        Priority `json:"priority,omitempty" yaml:"priority,omitempty"`
	Instructions    string   `json:"instructions,omitempty" yaml:"instructions,omitempty"`
	// Tags are iteration-scoped capability tags activated when the
	// target loop processes this wake. They merge into the
	// iteration's Request.InitialTags via the loop runtime's
	// notification drain, then fade unless the model explicitly
	// activates them via tool. Use this to route source-side
	// classification outcomes (contacts → "owner" / "untrusted",
	// MQTT topic patterns → "security" / "device_control") into
	// the target loop's per-iteration tool surface and context
	// providers without spawning a separate handler loop per
	// classification. See the trigger-unification design in
	// issue #902.
	Tags []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

// Empty reports whether the target has no loop selector.
func (t LoopWakeTarget) Empty() bool {
	return strings.TrimSpace(t.LoopID) == "" && strings.TrimSpace(t.Name) == ""
}

// Destination returns the message destination for the target.
func (t LoopWakeTarget) Destination() (Destination, error) {
	loopID := strings.TrimSpace(t.LoopID)
	name := strings.TrimSpace(t.Name)
	switch {
	case loopID != "":
		return Destination{Kind: DestinationLoop, Target: loopID, Selector: SelectorID}, nil
	case name != "":
		return Destination{Kind: DestinationLoop, Target: name, Selector: SelectorName}, nil
	default:
		return Destination{}, fmt.Errorf("loop wake target requires loop_id or name")
	}
}

// ParseLoopWakeTarget decodes the wake_loop tool argument shape used by
// source-specific subscription tools. A string is treated as a loop name; an
// object accepts loop_id, name, force_supervisor, priority, instructions, and
// tags.
func ParseLoopWakeTarget(raw any) (LoopWakeTarget, bool, error) {
	switch v := raw.(type) {
	case nil:
		return LoopWakeTarget{}, false, nil
	case string:
		target := LoopWakeTarget{Name: strings.TrimSpace(v)}
		return target, target.Name != "", nil
	case LoopWakeTarget:
		return validateLoopWakeTarget(v)
	case *LoopWakeTarget:
		if v == nil {
			return LoopWakeTarget{}, false, nil
		}
		return validateLoopWakeTarget(*v)
	case map[string]any:
		target := LoopWakeTarget{
			LoopID:          stringMapValue(v, "loop_id"),
			Name:            stringMapValue(v, "name"),
			ForceSupervisor: boolMapValue(v, "force_supervisor"),
			Instructions:    stringMapValue(v, "instructions"),
			Tags:            stringSliceMapValue(v, "tags"),
		}
		if priority := stringMapValue(v, "priority"); priority != "" {
			target.Priority = Priority(priority)
		}
		return validateLoopWakeTarget(target)
	default:
		return LoopWakeTarget{}, false, fmt.Errorf("wake_loop must be an object or loop name string, got %T", raw)
	}
}

// LoopResolver is the minimal contract a wake-target verifier needs from
// the live loop registry. Defined here rather than in the loop package so
// that ParseLoopWakeTarget and VerifyLoopWakeTarget callers can validate
// targets without taking a direct dependency on the loop runtime.
type LoopResolver interface {
	// LoopExistsByID reports whether a live loop with the given ID is
	// currently registered.
	LoopExistsByID(loopID string) bool
	// LoopExistsByName reports whether a live loop with the given name
	// is currently registered.
	LoopExistsByName(name string) bool
	// KnownLoopNames returns the names of currently registered loops in
	// stable order. Used to populate actionable error messages when a
	// caller's wake target doesn't resolve.
	KnownLoopNames() []string
}

// VerifyLoopWakeTarget checks that the target refers to a currently-running
// loop. Returns nil on success, an actionable error otherwise. Callers
// that accept a wake target from model input should call this after
// ParseLoopWakeTarget and before persisting the subscription, so a
// typo in name or loop_id is surfaced immediately rather than producing
// a permanent silent-drop on every subsequent poll cycle.
//
// A nil resolver is treated as "verification disabled" — useful in
// test harnesses or alternative wirings where the live registry is
// unavailable. Callers that need verification must provide a resolver.
func VerifyLoopWakeTarget(target LoopWakeTarget, resolver LoopResolver) error {
	if resolver == nil {
		return nil
	}
	loopID := strings.TrimSpace(target.LoopID)
	name := strings.TrimSpace(target.Name)
	switch {
	case loopID != "":
		if resolver.LoopExistsByID(loopID) {
			return nil
		}
		return fmt.Errorf("wake_loop.loop_id %q does not match any running loop; known loop names: %v", loopID, resolver.KnownLoopNames())
	case name != "":
		if resolver.LoopExistsByName(name) {
			return nil
		}
		return fmt.Errorf("wake_loop.name %q does not match any running loop; known loop names: %v", name, resolver.KnownLoopNames())
	default:
		return fmt.Errorf("wake_loop requires loop_id or name")
	}
}

// NewEventSourceEnvelope constructs a loop signal envelope from structured
// event-source records.
func NewEventSourceEnvelope(from Identity, target LoopWakeTarget, source string, events []LoopEventPayload) (Envelope, error) {
	if len(events) == 0 {
		return Envelope{}, fmt.Errorf("event-source envelope requires at least one event")
	}
	if len(events) > MaxLoopEventsPerWake {
		return Envelope{}, fmt.Errorf("event-source envelope has %d events; max per wake is %d", len(events), MaxLoopEventsPerWake)
	}
	destination, err := target.Destination()
	if err != nil {
		return Envelope{}, err
	}
	payloadEvents := cloneLoopEventPayloads(events)
	source = strings.TrimSpace(source)
	if source == "" {
		source = "event_source"
	}

	payload := LoopNotifyPayload{
		Kind:            "event_source",
		Message:         RenderLoopEventSummary(source, payloadEvents, target.Instructions),
		Context:         strings.TrimSpace(target.Instructions),
		ForceSupervisor: target.ForceSupervisor,
		Events:          payloadEvents,
		Tags:            append([]string(nil), target.Tags...),
	}
	scope := []string{"event_source"}
	if source != "event_source" {
		scope = append(scope, source)
	}

	return Envelope{
		From:     from,
		To:       destination,
		Type:     TypeSignal,
		Payload:  payload,
		Priority: normalizedPriority(target.Priority),
		Scope:    scope,
	}, nil
}

// RenderLoopEventSummary returns a compact text companion to structured events.
// It exists for models and UIs that render only the legacy message field.
func RenderLoopEventSummary(source string, events []LoopEventPayload, instructions string) string {
	var sb strings.Builder
	source = strings.TrimSpace(source)
	if source == "" {
		source = "event source"
	}
	fmt.Fprintf(&sb, "Event-source wake from %s:", source)
	if trimmed := strings.TrimSpace(instructions); trimmed != "" {
		fmt.Fprintf(&sb, "\nInstructions: %s", trimmed)
	}
	for _, event := range events {
		title := firstNonEmpty(event.Title, event.ID, event.Type, "event")
		fmt.Fprintf(&sb, "\n- [%s/%s] %s", event.Source, event.Type, title)
		if event.URL != "" {
			fmt.Fprintf(&sb, "\n  %s", event.URL)
		}
		if event.Summary != "" {
			fmt.Fprintf(&sb, "\n  %s", event.Summary)
		}
	}
	return sb.String()
}

func validateLoopWakeTarget(target LoopWakeTarget) (LoopWakeTarget, bool, error) {
	target.LoopID = strings.TrimSpace(target.LoopID)
	target.Name = strings.TrimSpace(target.Name)
	target.Instructions = strings.TrimSpace(target.Instructions)
	target.Priority = normalizedPriority(target.Priority)
	if target.Empty() {
		return LoopWakeTarget{}, true, fmt.Errorf("wake_loop requires loop_id or name")
	}
	return target, true, nil
}

func normalizedPriority(priority Priority) Priority {
	switch priority {
	case "", PriorityNormal:
		return PriorityNormal
	case PriorityLow, PriorityUrgent:
		return priority
	default:
		return PriorityNormal
	}
}

func stringMapValue(values map[string]any, key string) string {
	v, _ := values[key].(string)
	return strings.TrimSpace(v)
}

func boolMapValue(values map[string]any, key string) bool {
	v, _ := values[key].(bool)
	return v
}

// stringSliceMapValue extracts a []string from an args map,
// accepting both []string (passed through Go-side) and []any
// (tool-call args after JSON decode). Non-string elements are
// silently filtered. Empty / missing returns nil so the field
// stays omitempty-friendly.
func stringSliceMapValue(values map[string]any, key string) []string {
	v, ok := values[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []string:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if t := strings.TrimSpace(item); t != "" {
				out = append(out, t)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				if t := strings.TrimSpace(str); t != "" {
					out = append(out, t)
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

func cloneLoopEventPayloads(events []LoopEventPayload) []LoopEventPayload {
	if len(events) == 0 {
		return nil
	}
	out := make([]LoopEventPayload, len(events))
	for i, event := range events {
		out[i] = event
		if len(event.Metadata) > 0 {
			out[i].Metadata = make(map[string]string, len(event.Metadata))
			for k, v := range event.Metadata {
				out[i].Metadata[k] = v
			}
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
