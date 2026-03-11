package main

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/server/web"
)

// systemStatusAdapter bridges [connwatch.Manager] and [buildinfo] to the
// web package's [web.SystemStatusProvider] interface, keeping the web
// package decoupled from connwatch and buildinfo.
type systemStatusAdapter struct {
	connMgr *connwatch.Manager
}

// Health returns the health state of all watched services.
func (a *systemStatusAdapter) Health() map[string]web.ServiceHealth {
	status := a.connMgr.Status()
	result := make(map[string]web.ServiceHealth, len(status))
	for name, s := range status {
		h := web.ServiceHealth{
			Name:      s.Name,
			Ready:     s.Ready,
			LastError: s.LastError,
		}
		if !s.LastCheck.IsZero() {
			h.LastCheck = s.LastCheck.Format(time.RFC3339)
		}
		result[name] = h
	}
	return result
}

// Uptime returns how long the process has been running.
func (a *systemStatusAdapter) Uptime() time.Duration {
	return buildinfo.Uptime()
}

// Version returns build and runtime metadata.
func (a *systemStatusAdapter) Version() map[string]string {
	return buildinfo.RuntimeInfo()
}
