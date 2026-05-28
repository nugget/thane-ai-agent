package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

const (
	defaultHASearchStatesLimit   = 20
	maxHASearchStatesLimit       = 100
	haSearchStatesTruncationNote = "Result exceeded the tool byte cap; tighten the filter (narrower state set, a domain, or an area) or lower limit."
)

// haSearchStateComparisons is the set of numeric-attribute comparison
// operators the tool accepts.
var haSearchStateComparisons = map[string]func(a, b float64) bool{
	">":  func(a, b float64) bool { return a > b },
	"<":  func(a, b float64) bool { return a < b },
	">=": func(a, b float64) bool { return a >= b },
	"<=": func(a, b float64) bool { return a <= b },
	"==": func(a, b float64) bool { return a == b },
	"!=": func(a, b float64) bool { return a != b },
}

type haSearchStatesResult struct {
	Count     int                  `json:"count"`
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated,omitempty"`
	Filters   haSearchStateFilters `json:"filters"`
	Items     []haListEntityItem   `json:"items"`
}

// haSearchStateFilters echoes the resolved filter set back to the model
// so it can confirm what was actually searched.
type haSearchStateFilters struct {
	Domain     string   `json:"domain,omitempty"`
	States     []string `json:"states,omitempty"`
	Area       string   `json:"area,omitempty"`
	Attribute  string   `json:"attribute,omitempty"`
	Comparison string   `json:"comparison,omitempty"`
	Value      *float64 `json:"value,omitempty"`
}

type haSearchStateQuery struct {
	domain     string
	states     []string
	area       string
	attribute  string
	comparison string
	value      float64
	hasValue   bool
	limit      int
	include    homeassistant.EntityMetadataIncludes
}

// registerHASearchStates wires the ha_search_states tool: predicate
// search across the live state set. It is the native answer to the
// high-traffic "what's on / open / unavailable / low-battery" questions
// that previously forced a model to fan out N ha_get_state calls or
// reach for an MCP search tool.
func (r *Registry) registerHASearchStates() {
	if r.ha == nil {
		return
	}
	r.Register(&Tool{
		Name: "ha_search_states",
		Description: "Search Home Assistant entities by live state across all domains. " +
			"Answers 'what's on right now', 'what doors are open', 'which sensors are unavailable', 'what batteries are low'. " +
			"Filter by state value(s), by a numeric attribute predicate (e.g. battery < 20, temperature > 80), by domain, and/or by area — filters compose (AND). " +
			"At least one filter is required. Add include for area/device/label/description/visibility metadata on each match.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Match entities whose current state is one of these values (e.g. [\"on\"], [\"open\"], [\"unavailable\",\"unknown\"]). A single string is also accepted.",
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Restrict to one domain (e.g. light, binary_sensor, climate, lock, cover).",
				},
				"area": map[string]any{
					"type":        "string",
					"description": "Restrict to entities resolved (via the HA registry, including device-inherited area) to this area name, alias, or area_id.",
				},
				"attribute": map[string]any{
					"type":        "string",
					"description": "Numeric attribute to compare on each entity (e.g. battery, temperature, current_temperature). Requires comparison and value.",
				},
				"comparison": map[string]any{
					"type":        "string",
					"enum":        []string{">", "<", ">=", "<=", "==", "!="},
					"description": "Comparison operator for the attribute predicate.",
				},
				"value": map[string]any{
					"type":        "number",
					"description": "Numeric threshold for the attribute predicate.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum matches to return (default 20, max 100).",
				},
				"include": EntityMetadataIncludeParameter(),
			},
		},
		Handler: r.handleSearchStates,
	})
}

func (r *Registry) handleSearchStates(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	q, err := parseHASearchStateQuery(args)
	if err != nil {
		return "", err
	}

	states, err := r.ha.GetStates(ctx)
	if err != nil {
		return "", err
	}

	// Pass 1: cheap predicates (domain, state membership, numeric
	// attribute) over the raw state set — no registry round-trip.
	candidates := make([]homeassistant.State, 0, len(states))
	for _, s := range states {
		if q.domain != "" && entityDomainOf(s.EntityID) != q.domain {
			continue
		}
		if len(q.states) > 0 && !stateMatches(s.State, q.states) {
			continue
		}
		if q.attribute != "" && !attributeMatches(s, q.attribute, q.comparison, q.value) {
			continue
		}
		candidates = append(candidates, s)
	}

	// Pass 2: area filter and/or metadata enrichment need the registry.
	// Fetch one full-registry bundle (single GetEntityRegistry pass plus
	// the area/device/label/floor reads the include implies) rather than
	// one registry call per candidate.
	working := q.include
	if q.area != "" {
		working.Area = true
	}
	var bundle *haEntityMetadataBundle
	if working.Any() {
		bundle, err = fetchHAEntityMetadataBundle(ctx, r.ha, working)
		if err != nil {
			return "", err
		}
	}

	if q.area != "" {
		filtered := candidates[:0]
		areaOnly := homeassistant.EntityMetadataIncludes{Area: true}
		for _, s := range candidates {
			meta := bundle.resolver.MetadataForEntity(bundle.entries[s.EntityID], &s, areaOnly)
			if meta == nil || meta.Area == nil || !areaMetadataMatches(meta.Area, q.area) {
				continue
			}
			filtered = append(filtered, s)
		}
		candidates = filtered
	}

	total := len(candidates)
	if len(candidates) > q.limit {
		candidates = candidates[:q.limit]
	}

	items := make([]haListEntityItem, 0, len(candidates))
	for i := range candidates {
		s := candidates[i]
		item := haListEntityItem{EntityID: s.EntityID, State: s.State}
		if friendly, ok := s.Attributes["friendly_name"].(string); ok {
			item.FriendlyName = friendly
		}
		// Render output metadata using the CALLER's include (not the
		// working set the area filter may have widened), so an area
		// filter doesn't leak area metadata the caller didn't request.
		if q.include.Any() && bundle != nil {
			item.Metadata = bundle.resolver.MetadataForEntity(bundle.entries[s.EntityID], &s, q.include)
		}
		items = append(items, item)
	}

	result := haSearchStatesResult{
		Count:     len(items),
		Total:     total,
		Truncated: total > len(items),
		Filters:   q.filters(),
		Items:     items,
	}
	return toIndentedJSONWithTruncationNote(result, haSearchStatesTruncationNote), nil
}

func (q haSearchStateQuery) filters() haSearchStateFilters {
	f := haSearchStateFilters{
		Domain:     q.domain,
		States:     q.states,
		Area:       q.area,
		Attribute:  q.attribute,
		Comparison: q.comparison,
	}
	if q.hasValue {
		v := q.value
		f.Value = &v
	}
	return f
}

func parseHASearchStateQuery(args map[string]any) (haSearchStateQuery, error) {
	var q haSearchStateQuery

	q.domain = strings.TrimSpace(stringArgValue(args, "domain"))
	q.area = strings.TrimSpace(stringArgValue(args, "area"))
	q.states = stringListArg(args["state"])

	include, err := ParseEntityMetadataIncludesArg(args["include"], "include")
	if err != nil {
		return haSearchStateQuery{}, err
	}
	q.include = include

	limit, err := boundedIntArg(args, "limit", defaultHASearchStatesLimit, maxHASearchStatesLimit)
	if err != nil {
		return haSearchStateQuery{}, err
	}
	q.limit = limit

	q.attribute = strings.TrimSpace(stringArgValue(args, "attribute"))
	q.comparison = strings.TrimSpace(stringArgValue(args, "comparison"))
	if rawValue, ok := args["value"]; ok && rawValue != nil {
		v, ok := coerceFloat(rawValue)
		if !ok {
			return haSearchStateQuery{}, fmt.Errorf("value must be a number")
		}
		q.value = v
		q.hasValue = true
	}

	// The attribute predicate is all-or-nothing: attribute + comparison
	// + value travel together or not at all.
	if q.attribute != "" || q.comparison != "" || q.hasValue {
		if q.attribute == "" || q.comparison == "" || !q.hasValue {
			return haSearchStateQuery{}, fmt.Errorf("attribute, comparison, and value must all be set together for a numeric predicate")
		}
		if _, ok := haSearchStateComparisons[q.comparison]; !ok {
			return haSearchStateQuery{}, fmt.Errorf("comparison must be one of >, <, >=, <=, ==, !=")
		}
	}

	if q.domain == "" && q.area == "" && len(q.states) == 0 && q.attribute == "" {
		return haSearchStateQuery{}, fmt.Errorf("at least one filter is required: state, domain, area, or an attribute predicate")
	}
	return q, nil
}

func stateMatches(state string, want []string) bool {
	for _, w := range want {
		if strings.EqualFold(state, w) {
			return true
		}
	}
	return false
}

func attributeMatches(s homeassistant.State, attribute, comparison string, threshold float64) bool {
	raw, ok := s.Attributes[attribute]
	if !ok {
		return false
	}
	value, ok := coerceFloat(raw)
	if !ok {
		return false
	}
	cmp, ok := haSearchStateComparisons[comparison]
	if !ok {
		return false
	}
	return cmp(value, threshold)
}

func areaMetadataMatches(area *homeassistant.EntityAreaMetadata, query string) bool {
	if area == nil {
		return false
	}
	if strings.EqualFold(area.ID, query) || strings.EqualFold(area.Name, query) {
		return true
	}
	for _, alias := range area.Aliases {
		if strings.EqualFold(alias, query) {
			return true
		}
	}
	return false
}

func entityDomainOf(entityID string) string {
	if idx := strings.IndexByte(entityID, '.'); idx > 0 {
		return entityID[:idx]
	}
	return ""
}

// stringArgValue reads a string argument, returning "" when absent or
// not a string.
func stringArgValue(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

// stringListArg accepts a JSON array of strings or a single string,
// returning the non-empty trimmed values.
func stringListArg(raw any) []string {
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, el := range v {
			if s, ok := el.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	}
	return nil
}

func coerceFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
