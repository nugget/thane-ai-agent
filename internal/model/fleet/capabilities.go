package fleet

import modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"

func providerCapabilities(provider string, caps modelproviders.Capabilities) modelproviders.Capabilities {
	if caps != (modelproviders.Capabilities{}) {
		return caps
	}
	return modelproviders.CapabilitiesForProvider(provider)
}
