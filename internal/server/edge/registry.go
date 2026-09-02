package edge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
)

// dnsDefaults are the propagation settings a provider is known to need.
// They apply only where the operator's config leaves the value zero, so
// the pass-through stays a pass-through while a fresh config still works
// against a provider whose authoritative servers lag its API.
type dnsDefaults struct {
	PropagationDelay   time.Duration
	PropagationTimeout time.Duration
}

// providerEntry binds a provider name to its constructor and defaults.
type providerEntry struct {
	build    func(settings map[string]any, logger *slog.Logger) (certmagic.DNSProvider, error)
	defaults dnsDefaults
}

// providers is the registry of DNS providers the front door can solve
// DNS-01 challenges through. Providers are compiled in, so this is the
// one seam of the certmagic pass-through that is not generic: a name
// here selects an implementation of certmagic.DNSProvider, whether an
// upstream libdns module or an in-tree one when upstream's error or
// logging behaviour is unfit (see linodeProvider). Add providers as
// operators need them.
var providers = map[string]providerEntry{
	"linode": {
		build: func(settings map[string]any, logger *slog.Logger) (certmagic.DNSProvider, error) {
			p := &linodeProvider{}
			if err := decodeSettings(settings, p); err != nil {
				return nil, err
			}
			if strings.TrimSpace(p.APIToken) == "" {
				return nil, fmt.Errorf("linode: settings.api_token is required")
			}
			p.ready(logger)
			return p, nil
		},
		// Linode's authoritative nameservers pick up API-created records
		// on a schedule rather than immediately; ten minutes is the wait
		// operators report as reliable, and the checks that follow get
		// generous room after it.
		defaults: dnsDefaults{PropagationDelay: 10 * time.Minute, PropagationTimeout: 15 * time.Minute},
	},
}

// Providers lists the registered DNS provider names, sorted.
func Providers() []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// newProvider resolves the configured provider name and builds it from
// the pass-through settings, returning the provider's defaults alongside
// so the caller can fill any propagation values the config left zero.
func newProvider(name string, settings map[string]any, logger *slog.Logger) (certmagic.DNSProvider, dnsDefaults, error) {
	entry, ok := providers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, dnsDefaults{}, fmt.Errorf("tls.certmagic.dns.provider %q is not registered (known: %s)",
			name, strings.Join(Providers(), ", "))
	}
	p, err := entry.build(settings, logger)
	if err != nil {
		return nil, dnsDefaults{}, fmt.Errorf("tls.certmagic.dns.settings: %w", err)
	}
	return p, entry.defaults, nil
}

// decodeSettings copies a YAML settings map into a provider struct
// through its JSON tags, which every libdns provider declares. Unknown
// keys are an error so a misspelled credential key fails at boot instead
// of at the first renewal.
func decodeSettings(settings map[string]any, into any) error {
	if settings == nil {
		settings = map[string]any{}
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	return nil
}

// or returns v unless it is zero, in which case the default.
func or(v, def time.Duration) time.Duration {
	if v != 0 {
		return v
	}
	return def
}

func sortedKeys(m Surfaces) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
