package email

import "fmt"

// ContactResolver resolves email addresses to trust zone levels.
// Implementations wrap a contact store without requiring the email
// package to import the contacts package directly.
type ContactResolver interface {
	// ResolveTrustZone returns the trust zone ("owner", "trusted",
	// "known") for the given email address. Returns ("", false, nil)
	// if no matching contact is found.
	ResolveTrustZone(email string) (zone string, found bool, err error)
}

// TrustResult categorizes recipient addresses by their trust zone
// disposition for outbound email.
type TrustResult struct {
	// Allowed contains addresses that can be sent to freely
	// (trust zone "owner" or "trusted").
	Allowed []string

	// Warnings contains human-readable messages for "known" trust
	// zone contacts that require user confirmation.
	Warnings []string

	// Blocked contains human-readable messages for addresses with
	// no contact record.
	Blocked []string
}

// CheckRecipientTrust evaluates each address against the contact store
// and categorizes them by trust zone. If cr is nil, all addresses are
// allowed (trust gating is disabled).
func CheckRecipientTrust(cr ContactResolver, addresses []string) TrustResult {
	var result TrustResult

	if cr == nil {
		result.Allowed = addresses
		return result
	}

	for _, addr := range addresses {
		bare := extractAddress(addr)
		zone, found, err := cr.ResolveTrustZone(bare)
		if err != nil {
			result.Blocked = append(result.Blocked,
				fmt.Sprintf("Cannot send to %s: contact lookup failed: %v", bare, err))
			continue
		}

		if !found {
			result.Blocked = append(result.Blocked,
				fmt.Sprintf("Cannot send to %s: no contact record. Add with save_contact first.", bare))
			continue
		}

		switch zone {
		case "owner", "trusted":
			result.Allowed = append(result.Allowed, addr)
		case "known":
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Contact for %s is 'known' trust level — confirm with user before sending.", bare))
		default:
			result.Blocked = append(result.Blocked,
				fmt.Sprintf("Cannot send to %s: unrecognized trust zone %q.", bare, zone))
		}
	}

	return result
}

// HasIssues reports whether the trust check found any warnings or
// blocked addresses that prevent immediate sending.
func (tr TrustResult) HasIssues() bool {
	return len(tr.Warnings) > 0 || len(tr.Blocked) > 0
}

// FormatIssues returns a human-readable summary of all trust issues.
func (tr TrustResult) FormatIssues() string {
	var parts []string
	for _, w := range tr.Warnings {
		parts = append(parts, "⚠ "+w)
	}
	for _, b := range tr.Blocked {
		parts = append(parts, "✗ "+b)
	}
	return fmt.Sprintf("Email not sent — trust zone issues:\n\n%s", joinLines(parts))
}

// joinLines joins strings with newlines.
func joinLines(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}
