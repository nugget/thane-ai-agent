package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Snapshot implements agent.DynamicToolSource: the bridged tools that
// are not served by the shared registry (servers that became ready
// after Start, tools added later, and same-name replacements for static
// tools whose definition changed), plus their tag additions. The values
// are immutable and sorted, so an unchanged tool set renders
// identically from run to run. Returns (nil, nil) when there are none.
// Each call also lets modern servers with an expired tool-list ttl
// refresh in the background.
func (m *Manager) Snapshot() ([]*tools.Tool, map[string][]string) {
	m.mu.Lock()
	snap, tags, started := m.snapshot, m.tagAdds, m.started
	m.mu.Unlock()
	if started {
		for _, s := range m.servers {
			s.client.noteAccess()
		}
	}
	return snap, tags
}

// rebuild is every client's onToolsChanged callback. It runs on the
// client's supervisor goroutine with no client lock held.
func (m *Manager) rebuild() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildLocked()
}

// rebuildLocked recomputes the overlay from every server's current
// list, in config order and then raw tool name order:
//
//   - a name registered at Start by the same server is left to the
//     registry unless its definition drifted, in which case the new
//     definition is included under the same name. Runs use it: the
//     main loop and any delegate whose tags cover it, since delegates
//     run through the same loop. Only delegate model routing (its tool
//     count) and capability views such as tag_inspect still reflect the
//     startup registry;
//   - a name the registry already holds for anything else (a native
//     tool, another server's static tool) always wins over the overlay;
//   - among overlay tools the first claim to a name wins.
//
// Static tools a server stopped offering are simply absent; their
// handlers report the tool as no longer offered. Before Start finishes
// and after Close this does nothing. m.mu must be held.
func (m *Manager) rebuildLocked() {
	if !m.started || m.closed {
		return
	}
	var out []*tools.Tool
	tagAdds := map[string][]string{}
	claimed := map[string]struct{}{}
	for _, s := range m.servers {
		defs := s.client.tools()
		if defs == nil {
			continue
		}
		for _, bt := range s.bridge.bridge(defs) {
			name := bt.tool.Name
			if st, ok := m.static[name]; ok {
				if st.server != s {
					m.logCollision(s, bt)
					continue
				}
				if st.fp == bt.fp {
					continue
				}
				s.bridge.logOnce(slog.LevelInfo, "drift:"+bt.raw+":"+fingerprintKey(bt.fp),
					"mcp: tool definition changed after startup; runs use the new definition",
					"tool", name)
			} else if _, taken := m.reserved[name]; taken {
				m.logCollision(s, bt)
				continue
			}
			if _, dup := claimed[name]; dup {
				m.logCollision(s, bt)
				continue
			}
			claimed[name] = struct{}{}
			out = append(out, bt.tool)
			for _, tag := range bt.tool.Tags {
				tagAdds[tag] = append(tagAdds[tag], name)
			}
		}
	}
	if len(out) == 0 {
		m.snapshot, m.tagAdds = nil, nil
		return
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	for _, names := range tagAdds {
		sort.Strings(names)
	}
	if len(tagAdds) == 0 {
		tagAdds = nil
	}
	m.snapshot, m.tagAdds = out, tagAdds
}

// fingerprintKey shortens a definition fingerprint for log dedupe keys.
func fingerprintKey(fp string) string {
	sum := sha256.Sum256([]byte(fp))
	return hex.EncodeToString(sum[:8])
}
