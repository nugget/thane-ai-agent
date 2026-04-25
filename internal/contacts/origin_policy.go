package contacts

import "strings"

const (
	// PropertyOriginTag stores a capability tag that should be pinned
	// when this contact is the session origin.
	PropertyOriginTag = "X-THANE-ORIGIN-TAG"

	// PropertyOriginContextRef stores a managed document ref that should
	// be injected when this contact is the session origin.
	PropertyOriginContextRef = "X-THANE-ORIGIN-CONTEXT-REF"
)

// OriginPolicy is the contact-directory policy applied when a contact
// is the session origin.
type OriginPolicy struct {
	Tags        []string
	ContextRefs []string
}

// Empty reports whether the policy has no effect.
func (p OriginPolicy) Empty() bool {
	return len(p.Tags) == 0 && len(p.ContextRefs) == 0
}

// OriginPolicyFromProperties extracts origin policy from contact
// properties. Empty property types apply to all sources; a property type
// matching the source applies only to that channel.
func OriginPolicyFromProperties(props []Property, source string) OriginPolicy {
	var policy OriginPolicy
	for _, prop := range props {
		if !originPropertyApplies(prop, source) {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(prop.Property)) {
		case PropertyOriginTag:
			policy.Tags = append(policy.Tags, splitOriginValues(prop.Value)...)
		case PropertyOriginContextRef:
			policy.ContextRefs = append(policy.ContextRefs, splitOriginValues(prop.Value)...)
		}
	}
	policy.Tags = cleanOriginValues(policy.Tags)
	policy.ContextRefs = cleanOriginValues(policy.ContextRefs)
	return policy
}

func originPropertyApplies(prop Property, source string) bool {
	source = strings.TrimSpace(source)
	propType := strings.TrimSpace(prop.Type)
	if propType == "" || source == "" {
		return true
	}
	for _, part := range strings.Split(propType, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.EqualFold(part, "all") || strings.EqualFold(part, source) {
			return true
		}
	}
	return false
}

func splitOriginValues(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
}

func cleanOriginValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		cleaned = append(cleaned, value)
	}
	return cleaned
}
