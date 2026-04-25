package contacts

// Trust zone constants define the access levels for contacts. Zones
// are listed from most-privileged to least-privileged.
const (
	// ZoneAdmin is for system administrators (the Thane owner and
	// designated operators). Full frontier model access, unrestricted
	// tools, proactive outreach allowed, immediate send.
	ZoneAdmin = "admin"

	// ZoneHousehold is for household members and intimate family.
	// Full frontier model access, most tools available, proactive
	// outreach allowed, immediate send.
	ZoneHousehold = "household"

	// ZoneTrusted is for close friends, extended family, and
	// professional contacts with an established relationship.
	// Frontier model access, safe tool subset, limited proactive
	// outreach, sends require confirmation.
	ZoneTrusted = "trusted"

	// ZoneKnown is the default zone for contacts with a record but
	// no elevated trust. Local models only, read-only tool access,
	// no proactive outreach, sends blocked without explicit approval.
	ZoneKnown = "known"

	// ZoneUnknown represents unrecognized senders with no contact
	// record. This zone is NOT stored on contacts — it is the
	// implicit zone for messages from addresses that don't match any
	// contact. Local model access only (for triage), no tools, no
	// outreach, no sends.
	ZoneUnknown = "unknown"
)

// ValidTrustZones is the set of trust zone values that can be stored
// on a contact record. ZoneUnknown is intentionally excluded — it is
// the implicit zone for senders with no contact record.
var ValidTrustZones = map[string]bool{
	ZoneAdmin:     true,
	ZoneHousehold: true,
	ZoneTrusted:   true,
	ZoneKnown:     true,
}

// ZonePolicy encodes the capability matrix for a trust zone. Each
// field captures a dimension of what the agent is permitted to do when
// interacting with a contact at that trust level. Downstream
// consumers (model routing, notification priority, send gating) can
// query Policy(zone) instead of hardcoding zone names.
type ZonePolicy struct {
	// Zone is the trust zone this policy applies to.
	Zone string

	// FrontierModelAccess indicates whether frontier/cloud models
	// (e.g., Anthropic) may be used for this contact's requests.
	FrontierModelAccess bool

	// LocalModelOnly restricts the contact to local models only
	// (e.g., Ollama). Mutually exclusive with FrontierModelAccess
	// in practice — if both are false, no model access is granted.
	LocalModelOnly bool

	// ProactiveOutreach describes whether the agent may initiate
	// contact: "full", "limited", or "none".
	ProactiveOutreach string

	// ToolAccess describes the tool permission level:
	// "unrestricted", "most", "safe", "readonly", or "none".
	ToolAccess string

	// SendGating describes outbound message policy:
	// "allowed", "confirmation", or "blocked".
	SendGating string
}

// policies is the ordered list of zone policies from highest to
// lowest privilege. The order matters for Policies().
var policies = []ZonePolicy{
	{
		Zone:                ZoneAdmin,
		FrontierModelAccess: true,
		ProactiveOutreach:   "full",
		ToolAccess:          "unrestricted",
		SendGating:          "allowed",
	},
	{
		Zone:                ZoneHousehold,
		FrontierModelAccess: true,
		ProactiveOutreach:   "full",
		ToolAccess:          "most",
		SendGating:          "allowed",
	},
	{
		Zone:                ZoneTrusted,
		FrontierModelAccess: true,
		ProactiveOutreach:   "limited",
		ToolAccess:          "safe",
		SendGating:          "confirmation",
	},
	{
		Zone:              ZoneKnown,
		LocalModelOnly:    true,
		ProactiveOutreach: "none",
		ToolAccess:        "readonly",
		SendGating:        "blocked",
	},
	{
		Zone:              ZoneUnknown,
		LocalModelOnly:    true,
		ProactiveOutreach: "none",
		ToolAccess:        "none",
		SendGating:        "blocked",
	},
}

// policyIndex provides O(1) lookup by zone name.
var policyIndex = func() map[string]ZonePolicy {
	m := make(map[string]ZonePolicy, len(policies))
	for _, p := range policies {
		m[p.Zone] = p
	}
	return m
}()

// Policy returns the ZonePolicy for the given zone string. Unrecognized
// zone values fall back to the ZoneUnknown policy.
func Policy(zone string) ZonePolicy {
	if p, ok := policyIndex[zone]; ok {
		return p
	}
	return policyIndex[ZoneUnknown]
}

// Policies returns all zone policies in hierarchy order from highest
// privilege (admin) to lowest (unknown).
func Policies() []ZonePolicy {
	result := make([]ZonePolicy, len(policies))
	copy(result, policies)
	return result
}
