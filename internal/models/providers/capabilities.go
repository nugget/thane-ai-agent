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
