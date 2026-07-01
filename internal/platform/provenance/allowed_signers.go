package provenance

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// AgentPrincipal is the fixed principal under which thane's own signing key
// is trusted in every rendered allowed_signers file. It is an internal
// identity, not a contactable address.
const AgentPrincipal = "thane@provenance.local"

// TrustedSigner is one operator identity destined for an OpenSSH
// allowed_signers file. Times are RFC3339 as authored in config;
// [RenderAllowedSigners] converts them to OpenSSH's on-disk format.
type TrustedSigner struct {
	// Principal is the OpenSSH signer identity, conventionally an email.
	Principal string

	// PublicKey is the key in authorized_keys form ("ssh-ed25519 AAAA...").
	// Any trailing comment is ignored for identity and rendering; use
	// Comment for the rendered trailing note.
	PublicKey string

	// Comment is an optional trailing note rendered after the key.
	Comment string

	// ValidAfter and ValidBefore are optional RFC3339 validity bounds.
	ValidAfter  string
	ValidBefore string
}

// RenderAllowedSigners produces the content of an OpenSSH allowed_signers
// file that trusts the agent key plus the operator keys, deterministically.
//
// The agent key is the unremovable trust anchor: it is always emitted first,
// under [AgentPrincipal], and an operator entry whose public key equals the
// agent key is rejected — that is a principal-spoof that would let another
// identity ride the agent's own key. Operator keys are canonicalized (comment
// and whitespace stripped for identity), deduplicated by key blob, and sorted
// by (principal, blob) so the rendered file never churns across boots when the
// configured set is unchanged. The returned content ends with a trailing
// newline and is safe to compare byte-for-byte for drift detection.
func RenderAllowedSigners(agentPublicKey string, operators []TrustedSigner) (string, error) {
	agentBlob, err := canonicalKeyBlob(agentPublicKey)
	if err != nil {
		return "", fmt.Errorf("agent signing key: %w", err)
	}

	type entry struct {
		principal string
		blob      string
		line      string
	}
	// seen maps a canonical key blob to the principal that first claimed
	// it, so a key reused under a second principal is caught.
	seen := map[string]string{agentBlob: AgentPrincipal}
	others := make([]entry, 0, len(operators))
	for i, s := range operators {
		blob, err := canonicalKeyBlob(s.PublicKey)
		if err != nil {
			return "", fmt.Errorf("operator signer %d (%s): %w", i, strings.TrimSpace(s.Principal), err)
		}
		if blob == agentBlob {
			return "", fmt.Errorf("operator signer %q uses the agent's own signing key; the agent key is trusted implicitly and must not be listed", strings.TrimSpace(s.Principal))
		}
		if prev, ok := seen[blob]; ok {
			return "", fmt.Errorf("operator signer %q duplicates the key already trusted for %q", strings.TrimSpace(s.Principal), prev)
		}
		seen[blob] = strings.TrimSpace(s.Principal)
		line, err := renderSignerLine(s.Principal, blob, s.Comment, s.ValidAfter, s.ValidBefore)
		if err != nil {
			return "", fmt.Errorf("operator signer %q: %w", strings.TrimSpace(s.Principal), err)
		}
		others = append(others, entry{principal: strings.TrimSpace(s.Principal), blob: blob, line: line})
	}
	sort.Slice(others, func(i, j int) bool {
		if others[i].principal != others[j].principal {
			return others[i].principal < others[j].principal
		}
		return others[i].blob < others[j].blob
	})

	agentLine, err := renderSignerLine(AgentPrincipal, agentBlob, "", "", "")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(agentLine)
	b.WriteByte('\n')
	for _, e := range others {
		b.WriteString(e.line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// renderSignerLine builds one allowed_signers line:
//
//	principal [valid-after="...",valid-before="..."] keytype base64 [comment]
//
// blob is the canonical "keytype base64" form (no comment).
func renderSignerLine(principal, blob, comment, validAfter, validBefore string) (string, error) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return "", fmt.Errorf("principal is required")
	}
	parts := []string{principal}
	opts, err := renderValidityOptions(validAfter, validBefore)
	if err != nil {
		return "", err
	}
	if opts != "" {
		parts = append(parts, opts)
	}
	parts = append(parts, blob)
	if c := strings.TrimSpace(comment); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " "), nil
}

// renderValidityOptions renders the comma-joined OpenSSH options field for a
// validity window, or "" when neither bound is set.
func renderValidityOptions(validAfter, validBefore string) (string, error) {
	var opts []string
	if v := strings.TrimSpace(validAfter); v != "" {
		ts, err := opensshTime(v)
		if err != nil {
			return "", fmt.Errorf("valid_after: %w", err)
		}
		opts = append(opts, `valid-after="`+ts+`"`)
	}
	if v := strings.TrimSpace(validBefore); v != "" {
		ts, err := opensshTime(v)
		if err != nil {
			return "", fmt.Errorf("valid_before: %w", err)
		}
		opts = append(opts, `valid-before="`+ts+`"`)
	}
	return strings.Join(opts, ","), nil
}

// opensshTime converts an RFC3339 timestamp to OpenSSH's allowed_signers time
// format (YYYYMMDDHHMMSSZ, UTC).
func opensshTime(rfc3339 string) (string, error) {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "", fmt.Errorf("%q must be an RFC3339 timestamp: %w", rfc3339, err)
	}
	return t.UTC().Format("20060102150405Z"), nil
}

// canonicalKeyBlob parses an authorized_keys-form public key and returns its
// canonical "keytype base64" form with the comment stripped, so keys that
// differ only by comment or surrounding whitespace compare equal.
//
// It rejects any value carrying more than one key: ssh.ParseAuthorizedKey
// parses only the first line and returns the remainder in rest, so a value
// with an embedded newline and a second key would otherwise be silently
// accepted (and its second key dropped on render) — refuse it instead.
func canonicalKeyBlob(key string) (string, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return "", fmt.Errorf("not a valid SSH public key: %w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", fmt.Errorf("value must contain exactly one SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}
