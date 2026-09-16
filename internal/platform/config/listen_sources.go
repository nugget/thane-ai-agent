package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// AllowedPrefixes parses ollama_api.allowed_sources into the prefixes the
// shim's source guard matches a caller against.
func (c OllamaAPIConfig) AllowedPrefixes() ([]netip.Prefix, error) {
	return parseSourcePrefixes("ollama_api", c.AllowedSources)
}

// AllowedPrefixes parses openai_api.allowed_sources into the prefixes the
// shim's source guard matches a caller against.
func (c OpenAIAPIConfig) AllowedPrefixes() ([]netip.Prefix, error) {
	return parseSourcePrefixes("openai_api", c.AllowedSources)
}

// validateListenSources fails a config whose source lists cannot be
// parsed. The work happens at load, not on the first request, so a typo
// stops `thane validate` and the boot gate rather than silently admitting
// or refusing everyone once traffic arrives.
func (c *Config) validateListenSources() error {
	if _, err := c.OllamaAPI.AllowedPrefixes(); err != nil {
		return err
	}
	if _, err := c.OpenAIAPI.AllowedPrefixes(); err != nil {
		return err
	}
	return nil
}

// parseSourcePrefixes converts one allowed_sources list into prefixes,
// naming the block and the offending entry when one does not parse.
func parseSourcePrefixes(block string, entries []string) ([]netip.Prefix, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for i, entry := range entries {
		prefix, err := parseSourcePrefix(strings.TrimSpace(entry))
		if err != nil {
			return nil, fmt.Errorf("%s.allowed_sources[%d] %q: %w", block, i, entry, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// parseSourcePrefix reads one entry as a CIDR prefix, or as a bare
// address standing for the single-host prefix that covers exactly it.
// Prefixes are masked so an entry written with host bits set (192.168.1.5/24)
// means the network the operator plainly intended.
func parseSourcePrefix(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("not a CIDR prefix: %w", err)
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("not an address or CIDR prefix: %w", err)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
