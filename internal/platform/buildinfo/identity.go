package buildinfo

import (
	"fmt"
	"strings"
)

// UserAgentInfoURL is the canonical public description URL for Thane's
// outbound identity.
const UserAgentInfoURL = "https://github.com/nugget/thane-ai-agent"

// RobotsProductToken is Thane's stable crawler/product token. When a
// robots.txt user-agent line is used, this token should appear as a
// substring of the HTTP User-Agent string as recommended by RFC 9309.
const RobotsProductToken = "Thane"

// AgentSurface identifies a standard outbound surface that might merit
// slightly more precise truthful disclosure in a generated User-Agent.
type AgentSurface string

const (
	// AgentSurfaceGeneral is the default truthful identity for general outbound HTTP.
	AgentSurfaceGeneral AgentSurface = ""

	// AgentSurfaceForge identifies forge API traffic.
	AgentSurfaceForge AgentSurface = "forge"
)

const truthfulAgentRole = "automated software agent"

// UserAgent returns Thane's canonical truthful HTTP User-Agent string.
func UserAgent() string {
	return UserAgentFor(AgentSurfaceGeneral)
}

// UserAgentFor returns Thane's canonical truthful HTTP User-Agent string
// with optional standardized surface disclosure.
func UserAgentFor(surface AgentSurface) string {
	comments := []string{
		"+" + UserAgentInfoURL,
		truthfulAgentRole,
	}
	if label := strings.TrimSpace(string(surface)); label != "" {
		comments = append(comments, "surface="+label)
	}
	return fmt.Sprintf("%s/%s (%s)", RobotsProductToken, Version, strings.Join(comments, "; "))
}
