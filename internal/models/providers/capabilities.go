package providers

import "strings"

// Capabilities describes the runner features Thane knows how to use
// against a provider resource.
type Capabilities struct {
	SupportsChat      bool
	SupportsStreaming bool
	SupportsTools     bool
	SupportsImages    bool
	SupportsInventory bool
}

// CapabilitiesForProvider returns the known feature surface for a
// provider family. This is intentionally conservative and reflects only
// features Thane actually implements against that provider today.
func CapabilitiesForProvider(provider string) Capabilities {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return Capabilities{
			SupportsChat:      true,
			SupportsStreaming: true,
			SupportsTools:     true,
			SupportsImages:    true,
			SupportsInventory: true,
		}
	case "lmstudio":
		return Capabilities{
			SupportsChat:      true,
			SupportsStreaming: true,
			SupportsTools:     true,
			SupportsImages:    true,
			SupportsInventory: true,
		}
	case "anthropic":
		return Capabilities{
			SupportsChat:      true,
			SupportsStreaming: true,
			SupportsTools:     true,
			SupportsImages:    true,
			SupportsInventory: false,
		}
	default:
		return Capabilities{}
	}
}

// SupportsImagesForModel reports whether a specific model on an
// image-capable provider should be treated as vision-capable by Thane.
// This is intentionally conservative: local runner resources may
// accept image payloads at the transport layer even when a specific
// model cannot reason over them.
func SupportsImagesForModel(provider, name, family string, families []string, caps Capabilities) bool {
	if !caps.SupportsImages {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return true
	case "ollama", "lmstudio":
		return looksLikeVisionModel(name, family, families)
	default:
		return false
	}
}

func looksLikeVisionModel(name, family string, families []string) bool {
	values := make([]string, 0, 2+len(families))
	values = append(values, name, family)
	values = append(values, families...)

	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "" {
			continue
		}
		switch {
		case strings.Contains(v, "vision"):
			return true
		case strings.Contains(v, "multimodal"):
			return true
		case strings.Contains(v, "llava"):
			return true
		case strings.Contains(v, "bakllava"):
			return true
		case strings.Contains(v, "moondream"):
			return true
		case strings.Contains(v, "pixtral"):
			return true
		case strings.Contains(v, "internvl"):
			return true
		case strings.Contains(v, "minicpm-v"):
			return true
		case strings.Contains(v, "minicpmv"):
			return true
		case strings.Contains(v, "janus"):
			return true
		case strings.Contains(v, "qwen") && strings.Contains(v, "vl"):
			return true
		case strings.Contains(v, "gemma-3"):
			return true
		case strings.Contains(v, "gemma3"):
			return true
		}
	}
	return false
}
