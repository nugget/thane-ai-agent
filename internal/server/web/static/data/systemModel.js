// data/systemModel.js — reconstruct the legacy system shape from /v1.
//
// The node graph reads all system data through one object — the former
// /api/system response: { status, health, version, uptime, model_registry,
// router_stats, capability_catalog }. The native API splits that across four
// slim endpoints, so this composer reassembles the shape the graph already
// understands. Each fragment degrades independently: a missing router or
// cold registry just leaves that sub-object empty, and the graph already
// null-guards every field.

import * as api from './client.js';

// secondsToDuration renders a Go-style duration string (e.g. "3h12m4s") from a
// whole-second count, so the graph's parseDuration(state.system.uptime) path
// keeps working without change — the composer absorbs the only schema break
// (slim /v1/system exposes uptime_seconds, not an uptime string).
export function secondsToDuration(total) {
  if (total == null || Number.isNaN(total)) return undefined;
  let s = Math.max(0, Math.floor(total));
  const h = Math.floor(s / 3600); s -= h * 3600;
  const m = Math.floor(s / 60); s -= m * 60;
  let out = '';
  if (h) out += h + 'h';
  if (h || m) out += m + 'm';
  return out + s + 's';
}

// composeSystem fetches the four fragments in parallel and returns the legacy
// system shape, or null when /v1/system itself is unavailable (mirrors the old
// /api/system 404 -> state.system = null). router_stats carries the promoted
// Stats fields plus anthropic_rate_limit; router_audit is kept for the Models
// view.
export async function composeSystem(signal) {
  const opts = { signal };
  const [sys, registry, router, caps] = await Promise.all([
    api.tryGet('/system', opts),
    api.tryGet('/models/registry', opts),
    api.tryGet('/insights/router', opts),
    api.tryGet('/insights/capabilities', opts),
  ]);
  if (!sys) return null;
  return {
    status: sys.status,
    health: sys.health || {},
    version: sys.version || {},
    uptime: secondsToDuration(sys.uptime_seconds),
    uptime_seconds: sys.uptime_seconds,
    model_registry: registry || {},
    router_stats: (router && router.stats) || {},
    router_audit: (router && router.audit) || null,
    capability_catalog: caps || null,
  };
}
