package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
)

// FindEntityArgs represents the arguments for the ha_find_entity tool.
type FindEntityArgs struct {
	Description string                               `json:"description"`       // e.g., "access point LED", "ceiling fan"
	Area        string                               `json:"area,omitempty"`    // e.g., "office", "Nugget's Office"
	Domain      string                               `json:"domain,omitempty"`  // e.g., "light", "switch", "fan"
	Include     homeassistant.EntityMetadataIncludes `json:"include,omitempty"` // optional HA registry metadata
}

// FindEntityResult represents the result of entity discovery.
type FindEntityResult struct {
	Found        bool                          `json:"found"`
	EntityID     string                        `json:"entity_id,omitempty"`
	FriendlyName string                        `json:"friendly_name,omitempty"`
	AreaName     string                        `json:"area_name,omitempty"`
	Confidence   float64                       `json:"confidence,omitempty"`
	Error        string                        `json:"error,omitempty"`
	Candidates   []string                      `json:"candidates,omitempty"` // When ambiguous or not found
	Metadata     *homeassistant.EntityMetadata `json:"metadata,omitempty"`
}

// registerFindEntity registers the ha_find_entity tool.
func (r *Registry) registerFindEntity() {
	if r.ha == nil {
		return // Skip if no HA client
	}

	r.Register(&Tool{
		Name:        "ha_find_entity",
		Description: "Find a Home Assistant entity by description and area. Use this when the user refers to a device by description rather than entity_id. Returns the best matching entity or explains what was found.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"description": map[string]any{
					"type":        "string",
					"description": "Device description from user, e.g., 'access point LED', 'ceiling light', 'bedroom fan'",
				},
				"area": map[string]any{
					"type":        "string",
					"description": "Area name or alias, e.g., 'office', 'Nugget's Office', 'master bedroom'",
				},
				"domain": map[string]any{
					"type":        "string",
					"description": "Entity domain if known, e.g., 'light', 'switch', 'fan', 'cover'",
				},
				"include": EntityMetadataIncludeParameter(),
			},
			"required": []string{"description"},
		},
		Handler: r.executeFindEntityHandler,
	})
}

func (r *Registry) executeFindEntityHandler(ctx context.Context, argsMap map[string]any) (string, error) {
	// Convert map to struct
	var args FindEntityArgs
	if desc, ok := argsMap["description"].(string); ok {
		args.Description = desc
	}
	if area, ok := argsMap["area"].(string); ok {
		args.Area = area
	}
	if domain, ok := argsMap["domain"].(string); ok {
		args.Domain = domain
	}
	include, err := ParseEntityMetadataIncludesArg(argsMap["include"], "include")
	if err != nil {
		return "", err
	}
	args.Include = include

	if args.Description == "" {
		return "", fmt.Errorf("description is required")
	}

	// Auto-infer domain from description keywords if not provided
	if args.Domain == "" {
		args.Domain = inferDomainFromDescription(args.Description)
	}

	// Get entities, optionally filtered by domain
	entities, err := r.ha.GetEntities(ctx, args.Domain)
	if err != nil {
		return "", fmt.Errorf("get entities: %w", err)
	}

	lookupInclude := args.Include
	if args.Area != "" {
		lookupInclude.Area = true
		lookupInclude.Device = true
	}
	var metadata *haEntityMetadataBundle
	if lookupInclude.Any() {
		metadata, err = fetchHAEntityMetadataBundle(ctx, r.ha, lookupInclude)
		if err != nil {
			return "", err
		}
	}

	if args.Area != "" {
		entities = filterEntityInfosByArea(args.Area, entities, metadata)
	}

	if len(entities) == 0 {
		domainStr := args.Domain
		if domainStr == "" {
			domainStr = "any"
		}
		result := FindEntityResult{
			Found: false,
			Error: fmt.Sprintf("No %s entities found", domainStr),
		}
		return toJSON(result), nil
	}

	// Build search string combining description and area
	searchStr := args.Description
	if args.Area != "" {
		searchStr = args.Area + " " + args.Description
	}

	// Fuzzy match against entities
	matches := fuzzyMatchEntityInfosWithMetadata(searchStr, entities, metadata)

	if len(matches) == 0 {
		// No matches - return some candidates
		candidates := make([]string, 0, min(10, len(entities)))
		for i, e := range entities {
			if i >= 10 {
				break
			}
			name := e.FriendlyName
			if name == "" {
				name = e.EntityID
			}
			candidates = append(candidates, name)
		}
		result := FindEntityResult{
			Found:      false,
			Error:      fmt.Sprintf("No entity matching '%s' found", args.Description),
			Candidates: candidates,
		}
		return toJSON(result), nil
	}

	// Return best match
	best := matches[0]
	result := FindEntityResult{
		Found:        true,
		EntityID:     best.EntityID,
		FriendlyName: best.FriendlyName,
		Confidence:   best.Score,
	}
	if metadata != nil {
		if meta := metadata.metadataFromInfo(homeassistant.EntityInfo{
			EntityID:     best.EntityID,
			FriendlyName: best.FriendlyName,
			State:        best.State,
		}); meta != nil {
			if meta.Area != nil {
				result.AreaName = meta.Area.Name
			}
			if args.Include.Any() {
				result.Metadata = metadata.resolver.MetadataForEntity(metadata.entries[best.EntityID], &homeassistant.State{
					EntityID: best.EntityID,
					State:    best.State,
					Attributes: map[string]any{
						"friendly_name": best.FriendlyName,
					},
				}, args.Include)
			}
		}
	}

	// If multiple high-confidence matches, note ambiguity
	if len(matches) > 1 && matches[1].Score > 0.5 {
		candidates := make([]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, m.EntityID)
		}
		result.Candidates = candidates
	}

	return toJSON(result), nil
}

// EntityMatch represents a fuzzy match result.
type EntityMatch struct {
	EntityID     string
	FriendlyName string
	State        string
	Score        float64
}

// fuzzyMatchEntityInfos scores entities against a description.
func fuzzyMatchEntityInfos(description string, entities []homeassistant.EntityInfo) []EntityMatch {
	return fuzzyMatchEntityInfosWithMetadata(description, entities, nil)
}

// fuzzyMatchEntityInfosWithMetadata scores entities against a
// description, including registry-derived physical context when a
// metadata bundle is available.
func fuzzyMatchEntityInfosWithMetadata(description string, entities []homeassistant.EntityInfo, metadata *haEntityMetadataBundle) []EntityMatch {
	descLower := strings.ToLower(description)
	descTokens := tokenize(descLower)
	metadataWeight := metadataMatchWeight
	if len(descTokens) == 1 {
		metadataWeight = singleTokenMetadataMatchWeight
	}

	var matches []EntityMatch

	for _, e := range entities {
		score := bestTokenMatch(descTokens, []string{e.EntityID, e.FriendlyName})
		if metadata != nil {
			score = max(score, metadataWeight*bestTokenMatch(descTokens, metadataSearchTargets(metadata.metadataFromInfo(e))))
		}
		if score > 0.3 { // Minimum threshold
			matches = append(matches, EntityMatch{
				EntityID:     e.EntityID,
				FriendlyName: e.FriendlyName,
				State:        e.State,
				Score:        score,
			})
		}
	}

	// Sort by score descending
	for i := 0; i < len(matches)-1; i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].Score > matches[i].Score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	return matches
}

const (
	metadataMatchWeight            = 0.75
	singleTokenMetadataMatchWeight = 0.30
)

func bestTokenMatch(query []string, targets []string) float64 {
	score := 0.0
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		score = max(score, tokenMatchScore(query, tokenize(strings.ToLower(target))))
	}
	return score
}

func metadataSearchTargets(meta *homeassistant.EntityMetadata) []string {
	if meta == nil {
		return nil
	}
	targets := []string{
		meta.FriendlyName,
		meta.Name,
		meta.OriginalName,
		meta.Description,
		meta.EntityCategory,
		meta.Platform,
		meta.DeviceClass,
	}
	targets = append(targets, meta.Aliases...)
	if meta.Area != nil {
		targets = append(targets, meta.Area.ID, meta.Area.Name, meta.Area.FloorID)
		targets = append(targets, meta.Area.Aliases...)
	}
	if meta.Device != nil {
		targets = append(targets,
			meta.Device.ID,
			meta.Device.Name,
			meta.Device.NameByUser,
			meta.Device.Manufacturer,
			meta.Device.Model,
			meta.Device.ModelID,
			meta.Device.SerialNumber,
			meta.Device.AreaID,
			meta.Device.AreaName,
			meta.Device.EntryType,
		)
	}
	for _, label := range meta.Labels {
		targets = append(targets, label.ID, label.Name, label.Description)
	}
	return targets
}

func filterEntityInfosByArea(area string, entities []homeassistant.EntityInfo, bundle *haEntityMetadataBundle) []homeassistant.EntityInfo {
	if bundle == nil {
		return entities
	}
	needle := strings.ToLower(strings.TrimSpace(area))
	if needle == "" {
		return entities
	}
	out := make([]homeassistant.EntityInfo, 0, len(entities))
	for _, entity := range entities {
		meta := bundle.metadataFromInfo(entity)
		if meta == nil || meta.Area == nil {
			continue
		}
		if strings.EqualFold(meta.Area.ID, needle) || strings.EqualFold(meta.Area.Name, area) {
			out = append(out, entity)
			continue
		}
		for _, alias := range meta.Area.Aliases {
			if strings.EqualFold(alias, area) {
				out = append(out, entity)
				break
			}
		}
	}
	return out
}

// tokenize splits a string into lowercase tokens.
func tokenize(s string) []string {
	// Split on common separators
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "-", " ")

	tokens := strings.Fields(s)
	result := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if len(t) > 1 { // Skip single chars
			result = append(result, t)
		}
	}
	return result
}

// tokenMatchScore calculates overlap between token sets with abbreviation support.
func tokenMatchScore(query, target []string) float64 {
	if len(query) == 0 || len(target) == 0 {
		return 0
	}

	matches := 0.0
	for _, q := range query {
		bestMatch := 0.0
		for _, t := range target {
			score := 0.0

			// Exact match
			if t == q {
				score = 1.0
				// Substring match
			} else if strings.Contains(t, q) || strings.Contains(q, t) {
				score = 0.8
				// Abbreviation match (e.g., "ap" matches "access point" style naming)
			} else if isAbbreviation(q, t) || isAbbreviation(t, q) {
				score = 0.7
			}

			if score > bestMatch {
				bestMatch = score
			}
		}
		matches += bestMatch
	}

	return matches / float64(len(query))
}

// isAbbreviation checks if 'abbr' could be an abbreviation in 'full'.
// e.g., "ap" could match in "ap_hor_office" since ap is a token.
func isAbbreviation(abbr, full string) bool {
	if len(abbr) < 2 || len(abbr) > 4 {
		return false // Abbreviations are typically 2-4 chars
	}

	// Check if abbr appears as a token in full
	tokens := tokenize(full)
	for _, t := range tokens {
		if t == abbr {
			return true
		}
	}

	return false
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Should never happen with our result types, but be defensive
		return `{"error":"json encoding failed"}`
	}
	return string(b)
}

// inferDomainFromDescription guesses the entity domain from description keywords.
func inferDomainFromDescription(description string) string {
	descLower := strings.ToLower(description)

	// Light indicators
	lightKeywords := []string{"light", "lamp", "led", "bulb", "strip", "chandelier", "sconce", "fixture"}
	for _, kw := range lightKeywords {
		if strings.Contains(descLower, kw) {
			return "light"
		}
	}

	// Switch indicators
	switchKeywords := []string{"switch", "outlet", "plug", "relay"}
	for _, kw := range switchKeywords {
		if strings.Contains(descLower, kw) {
			return "switch"
		}
	}

	// Fan indicators
	fanKeywords := []string{"fan", "ventilat", "exhaust"}
	for _, kw := range fanKeywords {
		if strings.Contains(descLower, kw) {
			return "fan"
		}
	}

	// Lock indicators
	lockKeywords := []string{"lock", "deadbolt"}
	for _, kw := range lockKeywords {
		if strings.Contains(descLower, kw) {
			return "lock"
		}
	}

	// Cover indicators (blinds, shades, garage doors)
	coverKeywords := []string{"blind", "shade", "curtain", "garage", "shutter", "awning"}
	for _, kw := range coverKeywords {
		if strings.Contains(descLower, kw) {
			return "cover"
		}
	}

	// Climate indicators
	climateKeywords := []string{"thermostat", "hvac", "climate", "heat", "cool", "ac ", "a/c"}
	for _, kw := range climateKeywords {
		if strings.Contains(descLower, kw) {
			return "climate"
		}
	}

	// Sensor indicators
	sensorKeywords := []string{"sensor", "temperature", "humidity", "motion", "door sensor", "window sensor"}
	for _, kw := range sensorKeywords {
		if strings.Contains(descLower, kw) {
			return "sensor"
		}
	}

	// No match - return empty to search all domains
	return ""
}
