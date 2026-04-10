package llm

import (
	"encoding/json"
	"strings"
)

var topLevelCompositionKeywords = []string{"oneOf", "allOf", "anyOf"}

// StripTopLevelCompositionKeywords returns a deep-copied schema with
// unsupported top-level composition keywords removed and their object
// properties merged into the root when possible.
//
// This is a compatibility helper for downstream consumers that accept
// regular object schemas but reject top-level oneOf/allOf/anyOf. The
// returned schema is intentionally permissive: root-level required fields
// are preserved, but composition-derived required constraints are not
// re-encoded because doing so would often overconstrain the tool contract.
func StripTopLevelCompositionKeywords(schema map[string]any) (map[string]any, []string) {
	if schema == nil {
		return nil, nil
	}
	if !hasTopLevelCompositionKeywords(schema) {
		return schema, nil
	}

	cloned := cloneSchemaMap(schema)
	var removed []string
	var variants []map[string]any
	for _, keyword := range topLevelCompositionKeywords {
		raw, ok := cloned[keyword]
		if !ok {
			continue
		}
		delete(cloned, keyword)
		removed = append(removed, keyword)
		variants = append(variants, schemaVariants(raw)...)
	}
	if len(removed) == 0 {
		return cloned, nil
	}

	properties, _ := cloned["properties"].(map[string]any)
	if properties == nil {
		properties = make(map[string]any)
	}
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		// Hoist the first variant-level title/description when the root
		// schema doesn't already define one. This preserves some helpful
		// human-facing metadata without trying to reconcile conflicting
		// variant descriptions.
		if desc, ok := variant["description"].(string); ok && strings.TrimSpace(desc) != "" && strings.TrimSpace(schemaStringValue(cloned["description"])) == "" {
			cloned["description"] = desc
		}
		if title, ok := variant["title"].(string); ok && strings.TrimSpace(title) != "" && strings.TrimSpace(schemaStringValue(cloned["title"])) == "" {
			cloned["title"] = title
		}
		if variantProps, ok := variant["properties"].(map[string]any); ok {
			// First-wins on duplicate property keys. This keeps the
			// sanitizer deterministic for the current compatibility use
			// case, where variants usually share the same property schema
			// and differ only in required-field groups.
			for key, value := range variantProps {
				if _, exists := properties[key]; !exists {
					properties[key] = value
				}
			}
		}
	}
	if len(properties) > 0 {
		cloned["properties"] = properties
	}
	if _, ok := cloned["type"]; !ok {
		cloned["type"] = "object"
	}
	if tp, _ := cloned["type"].(string); tp == "object" {
		if _, ok := cloned["properties"]; !ok {
			cloned["properties"] = map[string]any{}
		}
	}
	return cloned, removed
}

func cloneSchemaMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		fallback := make(map[string]any, len(in))
		for key, value := range in {
			fallback[key] = value
		}
		return fallback
	}
	return out
}

func schemaVariants(raw any) []map[string]any {
	switch got := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(got))
		for _, item := range got {
			if schema, ok := item.(map[string]any); ok {
				out = append(out, schema)
			}
		}
		return out
	case []map[string]any:
		return got
	default:
		return nil
	}
}

func schemaStringValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func hasTopLevelCompositionKeywords(schema map[string]any) bool {
	for _, keyword := range topLevelCompositionKeywords {
		if _, ok := schema[keyword]; ok {
			return true
		}
	}
	return false
}
