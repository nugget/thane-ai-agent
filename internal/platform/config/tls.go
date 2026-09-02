package config

import (
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
)

// applyTLSDefaults fills the HTTPS front door's defaults. Ports and the
// HSTS lifetime default whether or not the front door is enabled so an
// operator flipping enabled on sees the values they will get; storage
// derives from the workspace like every other runtime directory.
func (c *Config) applyTLSDefaults() {
	if c.TLS.HTTPS.Port == 0 {
		c.TLS.HTTPS.Port = 443
	}
	if c.TLS.HTTP.Port == 0 {
		c.TLS.HTTP.Port = 80
	}
	if c.TLS.HSTSMaxAge == 0 {
		c.TLS.HSTSMaxAge = 4320 * time.Hour
	}
	if c.TLS.CertMagic.Storage == "" {
		if strings.TrimSpace(c.Workspace.Path) != "" {
			c.TLS.CertMagic.Storage = filepath.Join(c.Workspace.Path, "tls")
		} else if c.DataDir != "" {
			c.TLS.CertMagic.Storage = filepath.Join(c.DataDir, "tls")
		}
	}
}

// tlsKeyTypes are the certificate key types certmagic can generate.
var tlsKeyTypes = []string{"", "ed25519", "p256", "p384", "rsa2048", "rsa4096"}

// validateTLS checks the front door's shape. Provider availability and
// the provider's own settings are checked by the edge package, which
// owns the provider registry.
func (c *Config) validateTLS() error {
	t := c.TLS
	if !t.Enabled {
		return nil
	}
	if t.HTTPS.Port < 1 || t.HTTPS.Port > 65535 {
		return fmt.Errorf("tls.https.port %d out of range (1-65535)", t.HTTPS.Port)
	}
	if t.HTTPS.PublicPort < 0 || t.HTTPS.PublicPort > 65535 {
		return fmt.Errorf("tls.https.public_port %d out of range (0-65535)", t.HTTPS.PublicPort)
	}
	if !t.HTTP.Disabled && (t.HTTP.Port < 1 || t.HTTP.Port > 65535) {
		return fmt.Errorf("tls.http.port %d out of range (1-65535)", t.HTTP.Port)
	}
	if !t.HTTP.Disabled && t.HTTP.Port == t.HTTPS.Port && bindsOverlap(t.HTTP.Address, t.HTTPS.Address) {
		return fmt.Errorf("tls.http and tls.https cannot share port %d: binds %q and %q overlap", t.HTTPS.Port, t.HTTP.Address, t.HTTPS.Address)
	}
	if t.HSTSMaxAge < 0 {
		return fmt.Errorf("tls.hsts_max_age must not be negative")
	}
	if len(t.Hostnames) == 0 {
		return fmt.Errorf("tls.hostnames requires at least one hostname when tls.enabled is true")
	}
	for host, surface := range t.Hostnames {
		if err := validateTLSHostname(host); err != nil {
			return err
		}
		if !slices.Contains(TLSSurfaceNames, surface) {
			return fmt.Errorf("tls.hostnames[%q] routes to unknown surface %q (want one of %s)",
				host, surface, strings.Join(TLSSurfaceNames, ", "))
		}
		if surface == "ollama" && !c.OllamaAPI.Enabled {
			return fmt.Errorf("tls.hostnames[%q] routes to ollama but ollama_api.enabled is false", host)
		}
		if surface == "openai" && !c.OpenAIAPI.Enabled {
			return fmt.Errorf("tls.hostnames[%q] routes to openai but openai_api.enabled is false", host)
		}
	}
	for i, ca := range t.ClientAuth.TrustedPeerCAs {
		if strings.TrimSpace(ca) == "" {
			return fmt.Errorf("tls.client_auth.trusted_peer_cas[%d] is empty", i)
		}
	}
	m := t.CertMagic
	if !m.Agreed {
		return fmt.Errorf("tls.certmagic.agreed must be true: the CA requires acceptance of its subscriber agreement")
	}
	if !slices.Contains(tlsKeyTypes, strings.ToLower(m.KeyType)) {
		return fmt.Errorf("tls.certmagic.key_type %q unknown (want one of ed25519, p256, p384, rsa2048, rsa4096)", m.KeyType)
	}
	if m.RenewalWindowRatio < 0 || m.RenewalWindowRatio >= 1 {
		return fmt.Errorf("tls.certmagic.renewal_window_ratio %v must be in [0, 1)", m.RenewalWindowRatio)
	}
	if m.CertObtainTimeout < 0 {
		return fmt.Errorf("tls.certmagic.cert_obtain_timeout must not be negative")
	}
	if strings.TrimSpace(m.Storage) == "" {
		return fmt.Errorf("tls.certmagic.storage is required when no workspace is set")
	}
	if root := c.CoreRoot(); root != "" && paths.ContainsPath(root, m.Storage) {
		return fmt.Errorf("tls.certmagic.storage %q is inside the core root; certificate material is runtime state and must not enter signed history", m.Storage)
	}
	d := m.DNS
	if strings.TrimSpace(d.Provider) == "" {
		return fmt.Errorf("tls.certmagic.dns.provider is required: the front door solves challenges over DNS-01 only")
	}
	if d.PropagationDelay < 0 {
		return fmt.Errorf("tls.certmagic.dns.propagation_delay must not be negative")
	}
	if d.PropagationTimeout < -1 {
		return fmt.Errorf("tls.certmagic.dns.propagation_timeout must be positive, zero for the provider default, or -1 to disable checks")
	}
	if d.TTL < 0 {
		return fmt.Errorf("tls.certmagic.dns.ttl must not be negative")
	}
	for i, r := range d.Resolvers {
		host := r
		if h, _, err := net.SplitHostPort(r); err == nil {
			host = h
		}
		if strings.TrimSpace(host) == "" {
			return fmt.Errorf("tls.certmagic.dns.resolvers[%d] is empty", i)
		}
	}
	return nil
}

// validateTLSHostname accepts a DNS hostname as a certificate subject:
// lowercase LDH labels (letters, digits, hyphens, no leading or trailing
// hyphen, at most 63 octets each, 253 in total), at least two labels, no
// port, path, wildcard, trailing dot, or IP literal. Anything looser is
// a name the CA would refuse, and refusing it here means thane validate
// catches it instead of the first asynchronous issuance.
func validateTLSHostname(host string) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("tls.hostnames contains an empty hostname")
	}
	if strings.Contains(host, ":") || strings.Contains(host, "/") {
		return fmt.Errorf("tls.hostnames[%q] must be a bare hostname without port or path", host)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("tls.hostnames[%q] is an IP literal; ACME issues for DNS names only", host)
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("tls.hostnames[%q] is a wildcard; list each hostname explicitly", host)
	}
	if host != strings.ToLower(host) {
		return fmt.Errorf("tls.hostnames[%q] must be lowercase", host)
	}
	if len(host) > 253 {
		return fmt.Errorf("tls.hostnames[%q] exceeds 253 characters", host)
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("tls.hostnames[%q] must be a fully qualified name with at least two labels", host)
	}
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("tls.hostnames[%q] has an empty label (leading, trailing, or doubled dot)", host)
		}
		if len(label) > 63 {
			return fmt.Errorf("tls.hostnames[%q] has a label longer than 63 characters", host)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("tls.hostnames[%q] has a label starting or ending with a hyphen", host)
		}
		for _, r := range label {
			if !isLDHRune(r) {
				return fmt.Errorf("tls.hostnames[%q] contains %q; only lowercase letters, digits, and hyphens are valid in a DNS label", host, r)
			}
		}
	}
	return nil
}

// isLDHRune reports whether r is a lowercase letter, digit, or hyphen,
// the LDH alphabet a DNS label may use.
func isLDHRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
}

// bindsOverlap reports whether two bind addresses on the same port would
// collide: identical addresses do, and a wildcard bind collides with
// every address because it claims all interfaces.
func bindsOverlap(a, b string) bool {
	na, nb := normalizeBind(a), normalizeBind(b)
	return na == "" || nb == "" || na == nb
}

// normalizeBind reduces every spelling of "all interfaces" to the empty
// string and strips IPv6 brackets so equal addresses compare equal.
func normalizeBind(addr string) string {
	addr = strings.Trim(strings.TrimSpace(addr), "[]")
	if addr == "" || addr == "*" {
		return ""
	}
	if ip := net.ParseIP(addr); ip != nil {
		if ip.IsUnspecified() {
			return ""
		}
		return ip.String()
	}
	return strings.ToLower(addr)
}
