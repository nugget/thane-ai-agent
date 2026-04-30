// shared.js — Common utilities, card builders, and log rendering shared
// between app.js (main dashboard) and detail.js (detail popup).
//
// Loaded before both page scripts via <script src="/static/shared.js">.
// All functions defined here are available as globals.

// ---------------------------------------------------------------------------
// Number & Token Formatting
// ---------------------------------------------------------------------------

function formatNumber(n) {
  return n.toLocaleString();
}

function formatTokens(n) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  return String(n);
}

// ---------------------------------------------------------------------------
// Time & Duration Formatting
// ---------------------------------------------------------------------------

function formatDuration(ms) {
  if (ms < 0) return '0s';
  const totalSec = Math.floor(ms / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  if (m > 0) return m + 'm ' + s + 's';
  return s + 's';
}

function formatTime(date) {
  if (!(date instanceof Date) || isNaN(date)) return '';
  return date.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

function formatTimeShort(date) {
  if (!(date instanceof Date) || isNaN(date)) return '';
  return date.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

function timeAgo(date) {
  if (!(date instanceof Date) || isNaN(date)) return '';
  const diff = Date.now() - date.getTime();
  if (diff < 0) return 'soon';
  const sec = Math.floor(diff / 1000);
  if (sec < 60) return sec + 's ago';
  const min = Math.floor(sec / 60);
  if (min < 60) return min + 'm ago';
  const hr = Math.floor(min / 60);
  if (hr < 24) return hr + 'h ago';
  return Math.floor(hr / 24) + 'd ago';
}

// parseDuration converts a Go-style duration string (e.g., "10m30s",
// "2h", "500ms") to milliseconds.
function parseDuration(s) {
  if (!s) return 0;
  let ms = 0;
  const re = /(\d+(?:\.\d+)?)(ns|us|ms|s|m|h)/g;
  let match;
  while ((match = re.exec(s)) !== null) {
    const val = parseFloat(match[1]);
    switch (match[2]) {
      case 'h':  ms += val * 3600000; break;
      case 'm':  ms += val * 60000; break;
      case 's':  ms += val * 1000; break;
      case 'ms': ms += val; break;
      case 'us': ms += val / 1000; break;
      case 'ns': ms += val / 1000000; break;
    }
  }
  return ms;
}

function formatUptimeLong(ms) {
  const sec = Math.floor(ms / 1000);
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  const parts = [];
  if (d > 0) parts.push(d + 'd');
  if (h > 0) parts.push(h + 'h');
  if (m > 0) parts.push(m + 'm');
  parts.push(s + 's');
  return parts.join(' ');
}

// ---------------------------------------------------------------------------
// String Helpers
// ---------------------------------------------------------------------------

function shortID(id) {
  if (!id) return '';
  // UUID-like: show first 8 chars.
  if (id.length > 12) return id.slice(0, 8);
  return id;
}

function shortModelName(model) {
  if (!model) return '';
  // Strip date suffixes (e.g. "claude-sonnet-4-20250514" -> "claude-sonnet-4").
  const m = model.replace(/-\d{8,}$/, '');
  // Shorten common prefixes.
  return m.replace(/^claude-/, '').replace(/^gpt-/, '');
}

function escapeHTML(s) {
  const div = document.createElement('div');
  div.textContent = s;
  return div.innerHTML;
}

function truncate(s, max) {
  if (!s || s.length <= max) return s;
  return s.slice(0, max) + '\u2026';
}

function formatSchemaToken(value) {
  if (!value) return '';
  return String(value).replace(/_/g, ' ');
}

// formatToolTooltip builds a readable tooltip string for a live tool entry.
// Fields: tool (name), status ("running"/"done"/"error"), args, result, error.
function formatToolTooltip(entry) {
  if (!entry) return '';
  const lines = [entry.tool || 'unknown tool'];
  if (entry.args) {
    try {
      const s = typeof entry.args === 'string' ? entry.args : JSON.stringify(entry.args, null, 2);
      lines.push('Args: ' + s);
    } catch (_) { /* ignore */ }
  }
  if (entry.error) {
    lines.push('Error: ' + entry.error);
  }
  if (entry.result) {
    lines.push('Result: ' + entry.result);
  }
  if (entry.status === 'running') {
    lines.push('(running\u2026)');
  }
  return lines.join('\n');
}

const delegateToolNames = new Set(['thane_now', 'thane_assign', 'thane_delegate']);

function isDelegateTool(name) {
  return delegateToolNames.has(name);
}

function delegateToolLabel(name) {
  if (name === 'thane_now') return 'sync';
  if (name === 'thane_assign') return 'async';
  return 'legacy';
}

// parseDelegateArgs extracts task/guidance/tags from a delegate tool
// call's args (which may be a JSON string or object).
function parseDelegateArgs(args) {
  if (!args) return {};
  try {
    return typeof args === 'string' ? JSON.parse(args) : args;
  } catch (_) {
    return {};
  }
}

function extractDelegateCalls(liveTools) {
  if (!liveTools || liveTools.length === 0) return [];
  const calls = [];
  for (const entry of liveTools) {
    if (!isDelegateTool(entry.tool)) continue;
    const parsed = parseDelegateArgs(entry.args);
    calls.push({
      task: parsed.task || '',
      profile: parsed.profile || delegateToolLabel(entry.tool),
      guidance: truncate(parsed.guidance || '', 200),
      tags: parsed.tags || [],
      status: entry.status || 'done',
      error: entry.error || null,
    });
  }
  return calls;
}

function buildToolCounts(liveTools) {
  if (!liveTools || liveTools.length === 0) return null;
  const counts = {};
  for (const t of liveTools) {
    counts[t.tool] = (counts[t.tool] || 0) + 1;
  }
  return counts;
}

function countToolCalls(toolsUsed) {
  if (!toolsUsed) return 0;
  return Object.values(toolsUsed).reduce((sum, count) => sum + (count || 0), 0);
}

function countSummarySignals(summary) {
  if (!summary) return 0;
  return Object.keys(summary).length;
}

// ---------------------------------------------------------------------------
// ID Chip Helpers
// ---------------------------------------------------------------------------

function makeIDRow(label, value, opts = {}) {
  const row = document.createElement('div');
  row.className = 'id-row';

  const lbl = document.createElement('span');
  lbl.className = 'id-label';
  lbl.textContent = label;
  row.appendChild(lbl);

  row.appendChild(makeIDChip(value, opts));
  return row;
}

function makeIDChip(fullID, opts = {}) {
  const chip = document.createElement('span');
  chip.className = 'id-chip' + (opts.responsive === false ? '' : ' id-chip--responsive');
  chip.title = fullID;
  const txt = document.createElement('span');
  txt.className = 'id-chip-text';
  txt.textContent = opts.compact && fullID.length > 12 ? fullID.slice(0, 8) : fullID;
  chip.appendChild(txt);
  chip.addEventListener('click', (e) => {
    e.stopPropagation();
    navigator.clipboard.writeText(fullID).then(() => {
      chip.classList.add('id-chip--copied');
      setTimeout(() => chip.classList.remove('id-chip--copied'), 1200);
    });
  });
  return chip;
}

// ---------------------------------------------------------------------------
// System Inspector Rendering
// ---------------------------------------------------------------------------

function parseTimestamp(raw) {
  if (!raw) return null;
  if (typeof raw === 'string' && raw.startsWith('0001-01-01T00:00:00')) return null;
  const date = new Date(raw);
  if (isNaN(date) || date.getUTCFullYear() <= 1) return null;
  return date;
}

function formatTimestampMeta(raw) {
  const date = parseTimestamp(raw);
  if (!date) return '-';
  return timeAgo(date);
}

function formatFutureMeta(raw) {
  const date = parseTimestamp(raw);
  if (!date) return '-';
  const diff = date.getTime() - Date.now();
  if (diff <= 0) return 'expired';
  return 'for ' + formatDuration(diff);
}

function buildSystemStat(label, value, opts = {}) {
  const item = document.createElement('div');
  item.className = 'system-stat' + (opts.emphasis ? ' system-stat--emphasis' : '');

  const lbl = document.createElement('div');
  lbl.className = 'system-stat__label';
  lbl.textContent = label;
  item.appendChild(lbl);

  const val = document.createElement('div');
  val.className = 'system-stat__value';
  if (opts.id) val.id = opts.id;
  val.textContent = value;
  if (opts.title) val.title = opts.title;
  item.appendChild(val);

  return item;
}

function buildSystemChip(label, kind = '') {
  const chip = document.createElement('span');
  chip.className = 'system-chip' + (kind ? ' system-chip--' + kind : '');
  chip.textContent = label;
  return chip;
}

function buildSystemEmpty(text) {
  const empty = document.createElement('div');
  empty.className = 'system-empty';
  empty.textContent = text;
  return empty;
}

function renderSystemServices(container, health) {
  if (!container) return;
  container.innerHTML = '';

  const services = Object.entries(health || {}).sort((a, b) => {
    const aName = (a[1].name || a[0]).toLowerCase();
    const bName = (b[1].name || b[0]).toLowerCase();
    return aName.localeCompare(bName);
  });
  if (services.length === 0) {
    container.appendChild(buildSystemEmpty('No service health data'));
    return;
  }

  for (const [key, svc] of services) {
    const row = document.createElement('div');
    row.className = 'system-svc-row';

    const dot = document.createElement('span');
    dot.className = 'system-svc-dot system-svc-dot--' + (svc.ready ? 'ok' : 'err');
    row.appendChild(dot);

    const info = document.createElement('div');
    info.className = 'system-svc-info';

    const name = document.createElement('div');
    name.className = 'system-svc-name';
    name.textContent = svc.name || key;
    info.appendChild(name);

    const meta = document.createElement('div');
    meta.className = 'system-svc-meta';
    meta.textContent = svc.ready ? 'ready' : 'degraded';
    info.appendChild(meta);

    if (!svc.ready && svc.last_error) {
      const err = document.createElement('div');
      err.className = 'system-svc-error';
      err.textContent = svc.last_error;
      err.title = svc.last_error;
      info.appendChild(err);
    }

    row.appendChild(info);
    container.appendChild(row);
  }
}

function renderModelRegistry(summaryEl, resourcesEl, deploymentsEl, metaEl, registry, routerStats) {
  if (!summaryEl || !resourcesEl || !deploymentsEl) return;

  summaryEl.innerHTML = '';
  resourcesEl.innerHTML = '';
  deploymentsEl.innerHTML = '';
  if (metaEl) metaEl.textContent = '';

  if (!registry) {
    summaryEl.appendChild(buildSystemEmpty('Model registry not available'));
    return;
  }

  const deployments = Array.isArray(registry.deployments) ? registry.deployments.slice() : [];
  const resources = Array.isArray(registry.resources) ? registry.resources.slice() : [];
  const deploymentStats = (routerStats && routerStats.deployment_stats) || {};
  const resourceHealth = (routerStats && routerStats.resource_health) || {};

  const discoveredCount = deployments.filter((dep) => dep.source === 'discovered').length;
  const routableCount = deployments.filter((dep) => dep.routable).length;
  const flaggedCount = deployments.filter((dep) => dep.policy_state === 'flagged').length;
  const inactiveCount = deployments.filter((dep) => dep.policy_state === 'inactive').length;
  const overrideCount = deployments.filter((dep) => dep.policy_source === 'overlay').length;
  const cooldownCount = Object.keys(resourceHealth).length;

  summaryEl.appendChild(buildSystemStat('Generation', String(registry.generation || 0)));
  summaryEl.appendChild(buildSystemStat('Resources', formatNumber(resources.length)));
  summaryEl.appendChild(buildSystemStat('Deployments', formatNumber(deployments.length)));
  summaryEl.appendChild(buildSystemStat('Discovered', formatNumber(discoveredCount)));
  summaryEl.appendChild(buildSystemStat('Routable', formatNumber(routableCount)));
  summaryEl.appendChild(buildSystemStat('Cooldowns', formatNumber(cooldownCount), { title: cooldownCount > 0 ? 'resources temporarily avoided after recent timeouts' : '' }));
  summaryEl.appendChild(buildSystemStat('Overrides', formatNumber(overrideCount), { title: 'flagged ' + flaggedCount + ' · inactive ' + inactiveCount }));

  if (metaEl) {
    const parts = [];
    if (registry.updated_at) {
      parts.push('updated ' + formatTimestampMeta(registry.updated_at));
    }
    if (registry.default_model) {
      parts.push('default ' + registry.default_model);
    }
    metaEl.textContent = parts.join(' \u00b7 ');
    if (registry.updated_at) {
      const date = parseTimestamp(registry.updated_at);
      if (date) metaEl.title = date.toLocaleString();
    }
  }

  const sortedResources = resources.sort((a, b) => a.id.localeCompare(b.id));
  if (sortedResources.length === 0) {
    resourcesEl.appendChild(buildSystemEmpty('No configured resources'));
  } else {
    for (const resource of sortedResources) {
      const health = resourceHealth[resource.id] || null;
      const item = document.createElement('div');
      item.className = 'system-item';

      const header = document.createElement('div');
      header.className = 'system-item__header';

      const title = document.createElement('div');
      title.className = 'system-item__title';
      title.textContent = resource.id || '-';
      header.appendChild(title);

      const side = document.createElement('div');
      side.className = 'system-item__metric';
      side.textContent = formatNumber(resource.discovered_models || 0) + ' models';
      header.appendChild(side);
      item.appendChild(header);

      const subtitle = document.createElement('div');
      subtitle.className = 'system-item__subtitle';
      subtitle.textContent = resource.provider || '-';
      item.appendChild(subtitle);

      const chips = document.createElement('div');
      chips.className = 'system-item__chips';
      chips.appendChild(buildSystemChip(resource.provider || 'unknown', 'provider'));
      if (resource.supports_inventory) chips.appendChild(buildSystemChip('inventory', 'ok'));
      if (resource.supports_streaming) chips.appendChild(buildSystemChip('stream', 'ok'));
      if (resource.supports_tools) chips.appendChild(buildSystemChip('tools', 'ok'));
      if (resource.supports_images) chips.appendChild(buildSystemChip('images', 'ok'));
      if (resource.policy_state) {
        chips.appendChild(buildSystemChip(
          resource.policy_state,
          resource.policy_state === 'active' ? 'ok' : resource.policy_state === 'flagged' ? 'warn' : 'error',
        ));
      }
      if (health && health.cooldown_until) {
        chips.appendChild(buildSystemChip('cooldown', 'warn'));
      }
      if (resource.last_error) {
        chips.appendChild(buildSystemChip('error', 'error'));
      } else if (resource.last_refresh) {
        chips.appendChild(buildSystemChip('refreshed', 'ok'));
      }
      item.appendChild(chips);

      const facts = document.createElement('div');
      facts.className = 'system-item__facts';
      const factParts = [];
      if (resource.url) factParts.push(resource.url);
      if (resource.last_refresh) factParts.push('refresh ' + formatTimestampMeta(resource.last_refresh));
      if (health && health.cooldown_until) factParts.push('cooldown ' + formatFutureMeta(health.cooldown_until));
      if (factParts.length === 0) factParts.push('No refresh data yet');
      facts.textContent = factParts.join(' \u00b7 ');
      facts.title = factParts.join(' \u00b7 ');
      item.appendChild(facts);

      if (health && health.cooldown_reason) {
        const reason = document.createElement('div');
        reason.className = 'system-item__reason';
        reason.textContent = health.cooldown_reason;
        reason.title = health.cooldown_reason;
        item.appendChild(reason);
      }

      if (resource.policy_reason) {
        const reason = document.createElement('div');
        reason.className = 'system-item__reason';
        reason.textContent = resource.policy_reason;
        reason.title = resource.policy_reason;
        item.appendChild(reason);
      }

      if (resource.last_error) {
        const err = document.createElement('div');
        err.className = 'system-item__error';
        err.textContent = resource.last_error;
        err.title = resource.last_error;
        item.appendChild(err);
      }

      resourcesEl.appendChild(item);
    }
  }

  const sortedDeployments = deployments.sort((a, b) => {
    const aStats = deploymentStats[a.id] || {};
    const bStats = deploymentStats[b.id] || {};
    const reqDiff = Number(bStats.requests || 0) - Number(aStats.requests || 0);
    if (reqDiff !== 0) return reqDiff;
    const aActive = a.policy_state === 'active' ? 1 : 0;
    const bActive = b.policy_state === 'active' ? 1 : 0;
    if (bActive !== aActive) return bActive - aActive;
    return (a.id || '').localeCompare(b.id || '');
  });

  if (sortedDeployments.length === 0) {
    deploymentsEl.appendChild(buildSystemEmpty('No deployments in registry'));
    return;
  }

  for (const dep of sortedDeployments) {
    const stats = deploymentStats[dep.id] || {};
    const item = document.createElement('div');
    item.className = 'system-item system-item--deployment';

    const header = document.createElement('div');
    header.className = 'system-item__header';

    const title = document.createElement('div');
    title.className = 'system-item__title system-item__title--mono';
    title.textContent = dep.id || dep.model || '-';
    header.appendChild(title);

    const side = document.createElement('div');
    side.className = 'system-item__metric';
    side.textContent = formatNumber(stats.requests || 0) + ' req';
    header.appendChild(side);
    item.appendChild(header);

    if (dep.model && dep.model !== dep.id) {
      const subtitle = document.createElement('div');
      subtitle.className = 'system-item__subtitle';
      subtitle.textContent = dep.model;
      item.appendChild(subtitle);
    }

    const chips = document.createElement('div');
    chips.className = 'system-item__chips';
    chips.appendChild(buildSystemChip(dep.provider || 'unknown', 'provider'));
    if (dep.resource) chips.appendChild(buildSystemChip(dep.resource, 'resource'));
    if (dep.source) chips.appendChild(buildSystemChip(dep.source, dep.source === 'config' ? 'config' : 'discovered'));
    if (dep.policy_state) chips.appendChild(buildSystemChip(dep.policy_state, dep.policy_state === 'active' ? 'ok' : dep.policy_state === 'flagged' ? 'warn' : 'error'));
    if (dep.routable_source === 'overlay') {
      chips.appendChild(buildSystemChip(dep.routable ? 'promoted' : 'demoted', dep.routable ? 'ok' : 'muted'));
    }
    if (!dep.routable) chips.appendChild(buildSystemChip('explicit only', 'muted'));
    if (dep.supports_tools) chips.appendChild(buildSystemChip('tools', 'ok'));
    else if (dep.provider_supports_tools) chips.appendChild(buildSystemChip('tools off', 'muted'));
    if (dep.supports_streaming) chips.appendChild(buildSystemChip('stream', 'ok'));
    if (dep.supports_images) chips.appendChild(buildSystemChip('images', 'ok'));
    item.appendChild(chips);

    const facts = document.createElement('div');
    facts.className = 'system-item__facts';
    const factParts = [];
    if (dep.context_window) factParts.push('ctx ' + formatNumber(dep.context_window));
    if (dep.speed) factParts.push('spd ' + dep.speed);
    if (dep.quality) factParts.push('qlt ' + dep.quality);
    factParts.push('cost ' + (dep.cost_tier || 0));
    if (dep.parameter_size) factParts.push(dep.parameter_size);
    if (dep.quantization) factParts.push(dep.quantization);
    if (dep.family) factParts.push(dep.family);
    facts.textContent = factParts.join(' \u00b7 ');
    item.appendChild(facts);

    const telemetry = document.createElement('div');
    telemetry.className = 'system-item__telemetry';
    const telemetryParts = [
      'ok ' + formatNumber(stats.successes || 0),
      'err ' + formatNumber(stats.failures || 0),
      (stats.avg_latency_ms ? formatNumber(stats.avg_latency_ms) + 'ms' : '-'),
      (stats.avg_tokens_used ? formatTokens(stats.avg_tokens_used) + ' tok' : '-'),
    ];
    telemetry.textContent = telemetryParts.join(' \u00b7 ');
    item.appendChild(telemetry);

    if (dep.policy_reason) {
      const reason = document.createElement('div');
      reason.className = 'system-item__reason';
      reason.textContent = dep.policy_reason;
      reason.title = dep.policy_reason;
      item.appendChild(reason);
    }

    deploymentsEl.appendChild(item);
  }
}

function renderSystemRegistries(summaryEl, listEl, metaEl, sys, actions = {}) {
  if (summaryEl) summaryEl.innerHTML = '';
  if (listEl) listEl.innerHTML = '';
  if (metaEl) metaEl.textContent = '';
  if (!summaryEl && !listEl && !metaEl) return;

  const capabilities = getCapabilityCatalogEntries(sys);
  const capabilitySummary = summarizeCapabilityCatalog(capabilities);
  const registry = (sys && sys.model_registry) || {};
  const resources = Array.isArray(registry.resources) ? registry.resources : [];
  const deployments = Array.isArray(registry.deployments) ? registry.deployments : [];
  const readyActions = [
    typeof actions.toolbox === 'function' ? 'toolbox' : '',
    typeof actions.models === 'function' ? 'models' : '',
  ].filter(Boolean);

  if (summaryEl) {
    summaryEl.appendChild(buildSystemStat('Windows', formatNumber(readyActions.length)));
    summaryEl.appendChild(buildSystemStat('Capabilities', formatNumber(capabilitySummary.capabilityCount)));
    summaryEl.appendChild(buildSystemStat('Tools', formatNumber(capabilitySummary.uniqueToolCount)));
    summaryEl.appendChild(buildSystemStat('Resources', formatNumber(resources.length)));
    summaryEl.appendChild(buildSystemStat('Deployments', formatNumber(deployments.length)));
  }

  if (metaEl) {
    metaEl.textContent = readyActions.length > 0
      ? 'focused registry windows'
      : 'registry launchers pending';
  }

  if (!listEl) return;

  const entries = [
    {
      title: 'Toolbox & Capabilities',
      metric: formatNumber(capabilitySummary.capabilityCount) + ' capabilities',
      description: 'Runtime-defined capability catalog, tool membership, and operator-facing toolbox inventory.',
      chips: [
        buildSystemChip(formatNumber(capabilitySummary.uniqueToolCount) + ' tools', 'config'),
        capabilitySummary.alwaysActiveCount > 0 ? buildSystemChip(formatNumber(capabilitySummary.alwaysActiveCount) + ' always-on', 'ok') : null,
        capabilitySummary.discoverableCount > 0 ? buildSystemChip(formatNumber(capabilitySummary.discoverableCount) + ' discoverable', 'warn') : null,
        capabilitySummary.liveContextCount > 0 ? buildSystemChip(formatNumber(capabilitySummary.liveContextCount) + ' live context', 'ok') : null,
      ].filter(Boolean),
      facts: [
        capabilitySummary.capabilityCount > 0
          ? 'Browse the current runtime toolbox without relying on config as the source of truth.'
          : 'Capability catalog is not available yet.',
      ],
      actionLabel: 'Open toolbox window',
      action: typeof actions.toolbox === 'function' ? actions.toolbox : null,
    },
    {
      title: 'Model Registry',
      metric: formatNumber(deployments.length) + ' deployments',
      description: 'Routing inventory, provider resources, deployment policy, and observed model runtime attributes.',
      chips: [
        buildSystemChip(formatNumber(resources.length) + ' resources', 'resource'),
        buildSystemChip(formatNumber(deployments.length) + ' deployments', 'provider'),
        registry.default_model ? buildSystemChip('default ' + registry.default_model, 'config') : null,
      ].filter(Boolean),
      facts: [
        registry.generation
          ? 'Generation ' + formatNumber(registry.generation) + ' with current routing state and policy overlays.'
          : 'Model registry state is not available yet.',
      ],
      actionLabel: 'Open model registry',
      action: typeof actions.models === 'function' ? actions.models : null,
    },
    {
      title: 'Scheduled Loops',
      metric: 'planned',
      description: 'Future registry window for scheduled loop definitions, cadence, and wake-policy inspection.',
      chips: [
        buildSystemChip('coming soon', 'muted'),
      ],
      facts: [
        'This will follow the same focused-window pattern once scheduler registry data is exposed.',
      ],
      actionLabel: '',
      action: null,
    },
  ];

  for (const entry of entries) {
    const item = document.createElement('div');
    item.className = 'system-item';

    const header = document.createElement('div');
    header.className = 'system-item__header';

    const title = document.createElement('div');
    title.className = 'system-item__title';
    title.textContent = entry.title;
    header.appendChild(title);

    const metric = document.createElement('div');
    metric.className = 'system-item__metric';
    metric.textContent = entry.metric;
    header.appendChild(metric);

    item.appendChild(header);

    const subtitle = document.createElement('div');
    subtitle.className = 'system-item__subtitle';
    subtitle.textContent = entry.description;
    item.appendChild(subtitle);

    if (entry.chips.length > 0) {
      const chips = document.createElement('div');
      chips.className = 'system-item__chips';
      for (const chip of entry.chips) chips.appendChild(chip);
      item.appendChild(chips);
    }

    for (const fact of entry.facts) {
      const facts = document.createElement('div');
      facts.className = 'system-item__facts';
      facts.textContent = fact;
      item.appendChild(facts);
    }

    if (entry.action) {
      const actionsEl = document.createElement('div');
      actionsEl.className = 'system-item__actions';
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'toggle-btn system-item__button';
      btn.textContent = entry.actionLabel || 'Open';
      btn.addEventListener('click', (event) => {
        event.preventDefault();
        entry.action();
      });
      actionsEl.appendChild(btn);
      item.appendChild(actionsEl);
    }

    listEl.appendChild(item);
  }
}

function renderSystemInspector(sys, els) {
  if (!sys || !els) return;

  const badge = els.badge;
  if (badge) {
    badge.textContent = sys.status || 'unknown';
    badge.className = 'state-badge state-badge--' + (sys.status === 'healthy' ? 'sleeping' : 'error');
  }

  if (els.overview) {
    els.overview.innerHTML = '';
    const ver = sys.version || {};
    const totalRequests = Number((sys.router_stats && sys.router_stats.total_requests) || 0);
    const generation = Number((sys.model_registry && sys.model_registry.generation) || 0);

    els.overview.appendChild(buildSystemStat('Uptime', sys.uptime || '-', { id: 'system-uptime' }));
    els.overview.appendChild(buildSystemStat('Version', ver.version || '-', { id: 'system-version' }));
    els.overview.appendChild(buildSystemStat('Commit', ver.git_commit ? ver.git_commit.slice(0, 7) : '-', { id: 'system-commit', title: ver.git_commit || '' }));
    els.overview.appendChild(buildSystemStat('Go', ver.go_version || '-', { id: 'system-go' }));
    els.overview.appendChild(buildSystemStat('Arch', ((ver.os || '') && (ver.arch || '')) ? (ver.os + '/' + ver.arch) : '-', { id: 'system-arch' }));
    els.overview.appendChild(buildSystemStat('Requests', formatNumber(totalRequests), { emphasis: totalRequests > 0 }));
    els.overview.appendChild(buildSystemStat('Generation', formatNumber(generation)));
    els.overview.appendChild(buildSystemStat('Routing', sys.model_registry ? (sys.model_registry.local_first ? 'local-first' : 'policy') : '-', { title: sys.model_registry ? 'default ' + (sys.model_registry.default_model || '-') : '' }));
  }

  renderSystemServices(els.services, sys.health || {});
  renderModelRegistry(
    els.registrySummary,
    els.registryResources,
    els.registryDeployments,
    els.registryMeta,
    sys.model_registry,
    sys.router_stats,
  );

  if (els.capabilitySummary || els.capabilityList || els.capabilityMeta) {
    const capabilities = getCapabilityCatalogEntries(sys);
    renderCapabilityCatalog(
      els.capabilitySummary,
      els.capabilityList,
      els.capabilityMeta,
      capabilities,
      (sys.capability_catalog && sys.capability_catalog.activation_tools) || null,
    );
  }

  if (els.registriesSummary || els.registriesList || els.registriesMeta) {
    renderSystemRegistries(
      els.registriesSummary,
      els.registriesList,
      els.registriesMeta,
      sys,
      els.registryActions || {},
    );
  }
}

function cloneTokenList(values) {
  if (!Array.isArray(values)) return [];
  return Array.from(new Set(values.filter(Boolean).map((value) => String(value)))).sort((a, b) => a.localeCompare(b));
}

function cloneCountMap(raw) {
  if (!raw || typeof raw !== 'object') return {};
  const out = {};
  for (const [key, value] of Object.entries(raw)) {
    if (!key) continue;
    const count = Number(value || 0);
    if (Number.isFinite(count) && count > 0) out[key] = count;
  }
  return out;
}

function readToolingArray(raw, snakeKey, camelKey) {
  if (!raw || typeof raw !== 'object') return [];
  if (Array.isArray(raw[snakeKey])) return cloneTokenList(raw[snakeKey]);
  if (Array.isArray(raw[camelKey])) return cloneTokenList(raw[camelKey]);
  return [];
}

function normalizeCapabilityCatalogEntry(entry) {
  if (!entry || typeof entry !== 'object') return null;
  const tag = String(entry.tag || entry.Tag || '').trim();
  if (!tag) return null;
  const status = String(entry.status || entry.Status || 'available').trim() || 'available';
  const description = String(entry.description || entry.Description || '').trim();
  const toolCount = Number(entry.tool_count || entry.toolCount || 0);
  const tools = cloneTokenList(entry.tools || entry.Tools || []);
  const context = entry.context || entry.Context || null;
  return {
    tag,
    status,
    description,
    toolCount: Number.isFinite(toolCount) ? toolCount : 0,
    tools,
    alwaysActive: !!(entry.always_active || entry.alwaysActive),
    adHoc: !!(entry.ad_hoc || entry.adHoc),
    context: context && typeof context === 'object'
      ? {
          kbArticles: Number(context.kb_articles || context.kbArticles || 0),
          live: !!context.live,
        }
      : null,
  };
}

function normalizeLoadedCapabilityEntry(entry) {
  if (!entry || typeof entry !== 'object') return null;
  const tag = String(entry.tag || entry.Tag || '').trim();
  if (!tag) return null;
  const description = String(entry.description || entry.Description || '').trim();
  const toolCount = Number(entry.tool_count || entry.toolCount || 0);
  const context = entry.context || entry.Context || null;
  return {
    tag,
    description,
    toolCount: Number.isFinite(toolCount) ? toolCount : 0,
    alwaysActive: !!(entry.always_active || entry.alwaysActive),
    adHoc: !!(entry.ad_hoc || entry.adHoc),
    context: context && typeof context === 'object'
      ? {
          kbArticles: Number(context.kb_articles || context.kbArticles || 0),
          live: !!context.live,
        }
      : null,
  };
}

function normalizeLoadedCapabilities(entries, loadedTags) {
  const result = [];
  const seen = new Set();
  for (const entry of Array.isArray(entries) ? entries : []) {
    const normalized = normalizeLoadedCapabilityEntry(entry);
    if (!normalized || seen.has(normalized.tag)) continue;
    seen.add(normalized.tag);
    result.push(normalized);
  }
  for (const tag of cloneTokenList(loadedTags)) {
    if (seen.has(tag)) continue;
    seen.add(tag);
    result.push({ tag, description: '', toolCount: 0, alwaysActive: false, adHoc: false, context: null });
  }
  result.sort((a, b) => a.tag.localeCompare(b.tag));
  return result;
}

function normalizeTooling(raw, fallback = {}) {
  const configuredTags = cloneTokenList([
    ...readToolingArray(raw, 'configured_tags', 'configuredTags'),
    ...cloneTokenList(fallback.configuredTags || fallback.configured_tags || []),
  ]);
  const loadedTags = cloneTokenList([
    ...readToolingArray(raw, 'loaded_tags', 'loadedTags'),
    ...cloneTokenList(fallback.loadedTags || fallback.loaded_tags || []),
  ]);
  const effectiveTools = cloneTokenList([
    ...readToolingArray(raw, 'effective_tools', 'effectiveTools'),
    ...cloneTokenList(fallback.effectiveTools || fallback.effective_tools || []),
  ]);
  const excludedTools = cloneTokenList([
    ...readToolingArray(raw, 'excluded_tools', 'excludedTools'),
    ...cloneTokenList(fallback.excludedTools || fallback.excluded_tools || []),
  ]);
  const rawLoaded = raw && typeof raw === 'object'
    ? (raw.loaded_capabilities || raw.loadedCapabilities || [])
    : (fallback.loadedCapabilities || fallback.loaded_capabilities || []);
  return {
    configuredTags,
    loadedTags,
    loadedCapabilities: normalizeLoadedCapabilities(rawLoaded, loadedTags),
    effectiveTools,
    excludedTools,
    toolsUsed: Object.assign(
      {},
      cloneCountMap(fallback.toolsUsed || fallback.tools_used || null),
      cloneCountMap(raw && typeof raw === 'object' ? (raw.tools_used || raw.toolsUsed || null) : null),
    ),
  };
}

function getCapabilityCatalogEntries(system) {
  const catalog = system && system.capability_catalog;
  if (!catalog || !Array.isArray(catalog.capabilities)) return [];
  return catalog.capabilities
    .map((entry) => normalizeCapabilityCatalogEntry(entry))
    .filter(Boolean)
    .sort((a, b) => a.tag.localeCompare(b.tag));
}

function summarizeCapabilityCatalog(entries) {
  const valid = Array.isArray(entries) ? entries : [];
  const uniqueTools = new Set();
  let alwaysActiveCount = 0;
  let discoverableCount = 0;
  let liveContextCount = 0;
  for (const entry of valid) {
    if (entry.alwaysActive) alwaysActiveCount++;
    if (entry.status === 'discoverable' || entry.adHoc) discoverableCount++;
    if (entry.context && entry.context.live) liveContextCount++;
    for (const tool of entry.tools || []) uniqueTools.add(tool);
  }
  return {
    capabilityCount: valid.length,
    uniqueToolCount: uniqueTools.size,
    alwaysActiveCount,
    discoverableCount,
    liveContextCount,
  };
}

function describeCapabilityEntry(entry) {
  if (!entry) return '';
  const parts = [];
  if (entry.description) parts.push(entry.description);
  const meta = [];
  if (entry.toolCount > 0) meta.push(formatNumber(entry.toolCount) + ' tools');
  if (entry.alwaysActive) meta.push('always active');
  else if (entry.status) meta.push(formatSchemaToken(entry.status));
  if (entry.context && entry.context.kbArticles > 0) meta.push(formatNumber(entry.context.kbArticles) + ' KB');
  if (entry.context && entry.context.live) meta.push('live context');
  if (meta.length > 0) parts.push(meta.join(' · '));
  return parts.join(' — ');
}

function makeIterationCapabilityGroup(label, entries, className = 'tag-chip tag-chip--active') {
  const valid = Array.isArray(entries) ? entries.filter((entry) => entry && entry.tag) : [];
  if (valid.length === 0) return null;
  const group = document.createElement('div');
  group.className = 'iter-card__scope-group';

  const labelEl = document.createElement('div');
  labelEl.className = 'iter-card__scope-label';
  labelEl.textContent = label;
  group.appendChild(labelEl);

  const chips = document.createElement('div');
  chips.className = 'iter-card__scope-chips';
  for (const entry of valid) {
    const chip = document.createElement('span');
    chip.className = className;
    chip.textContent = entry.tag;
    const desc = describeCapabilityEntry(entry);
    if (desc) chip.title = desc;
    chips.appendChild(chip);
  }
  group.appendChild(chips);
  return group;
}

function renderCapabilityCatalog(summaryEl, listEl, metaEl, entries, activationTools) {
  if (summaryEl) summaryEl.innerHTML = '';
  if (listEl) listEl.innerHTML = '';
  if (metaEl) metaEl.textContent = '';

  if (!summaryEl && !listEl && !metaEl) return;

  const valid = Array.isArray(entries) ? entries : [];
  const summary = summarizeCapabilityCatalog(valid);

  if (summaryEl) {
    if (valid.length === 0) {
      summaryEl.appendChild(buildSystemEmpty('Capability catalog not available'));
    } else {
      summaryEl.appendChild(buildSystemStat('Capabilities', formatNumber(summary.capabilityCount)));
      summaryEl.appendChild(buildSystemStat('Tools', formatNumber(summary.uniqueToolCount)));
      summaryEl.appendChild(buildSystemStat('Always-on', formatNumber(summary.alwaysActiveCount)));
      summaryEl.appendChild(buildSystemStat('Discoverable', formatNumber(summary.discoverableCount)));
      summaryEl.appendChild(buildSystemStat('Live context', formatNumber(summary.liveContextCount)));
    }
  }

  if (metaEl) {
    const actionNames = [];
    if (activationTools && activationTools.activate) actionNames.push(activationTools.activate);
    if (activationTools && activationTools.deactivate) actionNames.push(activationTools.deactivate);
    if (activationTools && activationTools.list) actionNames.push(activationTools.list);
    metaEl.textContent = actionNames.length > 0 ? actionNames.join(' · ') : '';
  }

  if (!listEl) return;
  if (valid.length === 0) {
    listEl.appendChild(buildSystemEmpty('No capabilities registered'));
    return;
  }

  for (const entry of valid) {
    const item = document.createElement('div');
    item.className = 'system-item';

    const header = document.createElement('div');
    header.className = 'system-item__header';

    const title = document.createElement('div');
    title.className = 'system-item__title';
    title.textContent = entry.tag;
    header.appendChild(title);

    const side = document.createElement('div');
    side.className = 'system-item__metric';
    side.textContent = formatNumber(entry.toolCount) + ' tools';
    header.appendChild(side);
    item.appendChild(header);

    const subtitle = document.createElement('div');
    subtitle.className = 'system-item__subtitle';
    subtitle.textContent = entry.description || 'Capability description pending';
    item.appendChild(subtitle);

    const chips = document.createElement('div');
    chips.className = 'system-item__chips';
    chips.appendChild(buildSystemChip(entry.status || 'available', entry.alwaysActive ? 'ok' : entry.adHoc ? 'warn' : 'config'));
    if (entry.alwaysActive) chips.appendChild(buildSystemChip('always-on', 'ok'));
    if (entry.context && entry.context.live) chips.appendChild(buildSystemChip('live context', 'ok'));
    if (entry.context && entry.context.kbArticles > 0) chips.appendChild(buildSystemChip(formatNumber(entry.context.kbArticles) + ' KB', 'config'));
    item.appendChild(chips);

    if (entry.tools && entry.tools.length > 0) {
      const tools = document.createElement('div');
      tools.className = 'iter-card__scope-chips';
      for (const tool of entry.tools) {
        const chip = document.createElement('span');
        chip.className = 'iter-card__tool-item iter-card__tool-item--scope';
        chip.textContent = tool;
        tools.appendChild(chip);
      }
      item.appendChild(tools);
    }

    listEl.appendChild(item);
  }
}

// ---------------------------------------------------------------------------
// Log Rendering
// ---------------------------------------------------------------------------

// renderLogRows populates a log <tbody> from an array of log entries.
// Handles empty-state display and auto-scroll to bottom.
//
//   els: { logEmpty, logScroll, logBody }  — DOM element references
function renderLogRows(entries, els) {
  if (!entries || entries.length === 0) {
    els.logEmpty.hidden = false;
    els.logEmpty.querySelector('p').textContent = 'No log entries found';
    els.logBody.innerHTML = '';
    if (els.logScroll) els.logScroll.hidden = true;
    return;
  }

  els.logEmpty.hidden = true;
  if (els.logScroll) els.logScroll.hidden = false;

  // Check if already scrolled to bottom before updating content.
  const scrollEl = els.logScroll || els.logBody.parentElement;
  const atBottom = scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight < 24;

  els.logBody.innerHTML = '';

  for (const entry of entries) {
    const tr = document.createElement('tr');

    const tdTime = document.createElement('td');
    tdTime.className = 'log-time';
    tdTime.textContent = entry.Timestamp
      ? formatTime(new Date(entry.Timestamp))
      : '';

    const tdLevel = document.createElement('td');
    const levelSpan = document.createElement('span');
    levelSpan.className = 'level-badge level-badge--' + (entry.Level || 'INFO');
    levelSpan.textContent = entry.Level || '?';
    tdLevel.appendChild(levelSpan);

    const tdSub = document.createElement('td');
    tdSub.className = 'log-subsystem';
    tdSub.textContent = entry.Subsystem || '';

    const tdMsg = document.createElement('td');
    tdMsg.className = 'log-msg';
    tdMsg.textContent = entry.Msg || '';
    tdMsg.title = entry.Msg || '';

    const tdDetail = document.createElement('td');
    tdDetail.className = 'log-detail';
    buildLogDetail(tdDetail, entry);

    tr.appendChild(tdTime);
    tr.appendChild(tdLevel);
    tr.appendChild(tdSub);
    tr.appendChild(tdMsg);
    tr.appendChild(tdDetail);
    els.logBody.appendChild(tr);
  }

  // Auto-scroll to bottom if user was already there (live tail).
  if (atBottom) {
    scrollEl.scrollTop = scrollEl.scrollHeight;
  }
}

// Build detail column from promoted fields + parsed Attrs JSON.
function buildLogDetail(td, entry) {
  const parts = [];

  if (entry.Model) {
    parts.push({ key: 'model', val: entry.Model, cls: 'model' });
  }
  if (entry.Tool) {
    parts.push({ key: 'tool', val: entry.Tool, cls: 'tool' });
  }

  // Parse Attrs JSON for extra instrumentation.
  let attrs = null;
  if (entry.Attrs) {
    try { attrs = JSON.parse(entry.Attrs); } catch (_) { /* ignore */ }
  }
  if (attrs) {
    // Duration fields (various naming conventions).
    for (const k of ['duration', 'elapsed', 'latency', 'took']) {
      if (attrs[k] != null) {
        parts.push({ key: k, val: String(attrs[k]), cls: 'duration' });
      }
    }
    // Token fields.
    if (attrs.input_tokens != null) {
      parts.push({ key: 'in', val: formatTokens(attrs.input_tokens), cls: 'tokens' });
    }
    if (attrs.output_tokens != null) {
      parts.push({ key: 'out', val: formatTokens(attrs.output_tokens), cls: 'tokens' });
    }
    if (attrs.total_tokens != null && attrs.input_tokens == null) {
      parts.push({ key: 'tokens', val: formatTokens(attrs.total_tokens), cls: 'tokens' });
    }
    // Tool call count.
    if (attrs.tool_calls != null) {
      parts.push({ key: 'tools', val: String(attrs.tool_calls), cls: 'tool' });
    }
    if (attrs.tool_count != null && attrs.tool_calls == null) {
      parts.push({ key: 'tools', val: String(attrs.tool_count), cls: 'tool' });
    }
    // Catch-all: surface remaining scalar attrs.
    const shown = new Set([
      'duration', 'elapsed', 'latency', 'took',
      'input_tokens', 'output_tokens', 'total_tokens',
      'tool_calls', 'tool_count',
      'thane_version', 'thane_commit', 'loop_id', 'loop_name',
    ]);
    for (const [k, v] of Object.entries(attrs)) {
      if (shown.has(k)) continue;
      if (v == null || typeof v === 'object') continue;
      const s = String(v);
      if (s.length > 40) continue;
      parts.push({ key: k, val: s, cls: '' });
    }
  }

  for (const p of parts) {
    const span = document.createElement('span');
    span.className = 'log-attr';

    const key = document.createElement('span');
    key.className = 'log-attr-key';
    key.textContent = p.key + '=';

    const val = document.createElement('span');
    val.className = 'log-attr-val' + (p.cls ? ' log-attr-val--' + p.cls : '');
    val.textContent = p.val;

    span.appendChild(key);
    span.appendChild(val);
    td.appendChild(span);
  }

  // Copy-clickable ID chips for tracing IDs. Request ID chips are also
  // clickable to open the request detail waterfall view.
  const ids = [
    { label: 'req', full: entry.RequestID, inspectable: typeof window.onRequestChipClick === 'function' },
    { label: 'conv', full: entry.ConversationID },
    { label: 'sess', full: entry.SessionID },
  ];
  for (const { label, full, inspectable } of ids) {
    if (!full) continue;
    const chip = document.createElement('span');
    chip.className = 'log-id-chip' + (inspectable ? ' log-id-chip--clickable' : '');
    chip.textContent = label + ':' + shortID(full);
    chip.title = inspectable
      ? 'Click to inspect request \u00b7 Shift+click to copy\n' + full
      : label + ' \u2014 click to copy\n' + full;
    chip.addEventListener('click', (e) => {
      e.stopPropagation();
      if (inspectable && !e.shiftKey && typeof window.onRequestChipClick === 'function') {
        window.onRequestChipClick(full);
        return;
      }
      navigator.clipboard.writeText(full).then(() => {
        chip.classList.add('log-id-chip--copied');
        setTimeout(() => chip.classList.remove('log-id-chip--copied'), 1200);
      });
    });
    td.appendChild(chip);
  }

  // Tooltip with full attrs JSON for inspection.
  if (attrs) {
    td.title = JSON.stringify(attrs, null, 2);
  }
}

// ---------------------------------------------------------------------------
// Iteration Card Builders
// ---------------------------------------------------------------------------

// buildLiveCard creates the green-bordered card for a currently-running
// iteration. Caller passes the loop data object.
function makeIterationFact(label, value) {
  if (value === null || value === undefined || value === '') return null;
  const cell = document.createElement('div');
  cell.className = 'iter-card__fact';

  const valueEl = document.createElement('div');
  valueEl.className = 'iter-card__fact-value';
  valueEl.textContent = String(value);
  cell.appendChild(valueEl);

  const labelEl = document.createElement('div');
  labelEl.className = 'iter-card__fact-label';
  labelEl.textContent = String(label);
  cell.appendChild(labelEl);

  return cell;
}

function makeIterationFacts(items) {
  const valid = (items || []).filter((item) => item && item.value !== null && item.value !== undefined && item.value !== '');
  if (valid.length === 0) return null;
  const grid = document.createElement('div');
  grid.className = 'iter-card__facts';
  for (const item of valid) {
    const fact = makeIterationFact(item.label, item.value);
    if (fact) grid.appendChild(fact);
  }
  return grid;
}

function makeIterationChipGroup(label, values, className) {
  const valid = Array.isArray(values) ? values.filter(Boolean) : [];
  if (valid.length === 0) return null;
  const group = document.createElement('div');
  group.className = 'iter-card__scope-group';

  const labelEl = document.createElement('div');
  labelEl.className = 'iter-card__scope-label';
  labelEl.textContent = label;
  group.appendChild(labelEl);

  const chips = document.createElement('div');
  chips.className = 'iter-card__scope-chips';
  for (const value of valid) {
    const chip = document.createElement('span');
    chip.className = className;
    chip.textContent = value;
    chips.appendChild(chip);
  }
  group.appendChild(chips);
  return group;
}

function makeIterationScopePanel(items) {
  const valid = (items || []).filter(Boolean);
  if (valid.length === 0) return null;
  const panel = document.createElement('div');
  panel.className = 'iter-card__scope';
  for (const item of valid) {
    const group = item.capabilities
      ? makeIterationCapabilityGroup(item.label, item.capabilities, item.className)
      : makeIterationChipGroup(item.label, item.values, item.className);
    if (group) panel.appendChild(group);
  }
  return panel.childElementCount > 0 ? panel : null;
}

function buildPastIterationSummary(snap, handlerOnly) {
  const toolCalls = countToolCalls(snap.tools_used);
  const summarySignals = countSummarySignals(snap.summary);
  const bits = [];
  if (snap.error) {
    bits.push('Turn ended with an issue' + (snap.elapsed_ms ? ' after ' + formatDuration(snap.elapsed_ms) : ''));
  } else if (handlerOnly) {
    bits.push('Handler pass completed cleanly' + (snap.elapsed_ms ? ' in ' + formatDuration(snap.elapsed_ms) : ''));
  } else {
    bits.push('Model turn completed cleanly' + (snap.elapsed_ms ? ' in ' + formatDuration(snap.elapsed_ms) : ''));
  }
  if (snap.model) bits.push('on ' + shortModelName(snap.model));
  if (!handlerOnly && (snap.input_tokens || snap.output_tokens)) {
    bits.push(formatTokens(snap.input_tokens || 0) + ' in / ' + formatTokens(snap.output_tokens || 0) + ' out');
  }
  if (toolCalls > 0) {
    bits.push(toolCalls + ' tool call' + (toolCalls === 1 ? '' : 's'));
  }
  if (handlerOnly && summarySignals > 0) {
    bits.push(summarySignals + ' reported signal' + (summarySignals === 1 ? '' : 's'));
  }
  return bits.join(' · ');
}

function buildPastIterationHeaderTitle(snap, handlerOnly) {
  const bits = [];
  if (snap.request_id) bits.push('req ' + shortID(snap.request_id));
  if (snap.model) bits.push(shortModelName(snap.model));
  if (handlerOnly && bits.length === 0) bits.push('handler snapshot');
  if (bits.length === 0) bits.push('recent turn');
  return bits.join(' · ');
}

function buildPastIterationStatusLabel(snap, handlerOnly) {
  if (snap.error) return 'Issue';
  if (snap.supervisor) return 'Supervisor';
  if (handlerOnly) return 'Handler';
  return 'Turn';
}

function buildPastIterationHealth(snap) {
  if (snap.error) return 'issue';
  return 'clean';
}

function buildAggregateStat(label, value, opts = {}) {
  if (value === null || value === undefined || value === '') return null;
  const item = document.createElement('div');
  item.className = 'agg-stat' + (opts.emphasis ? ' agg-stat--emphasis' : '');

  const valueEl = document.createElement('div');
  valueEl.className = 'agg-stat__value';
  valueEl.textContent = String(value);
  if (opts.title) valueEl.title = opts.title;
  item.appendChild(valueEl);

  const labelEl = document.createElement('div');
  labelEl.className = 'agg-stat__label';
  labelEl.textContent = String(label);
  item.appendChild(labelEl);

  return item;
}

function buildLiveCard(loop) {
  const card = document.createElement('div');
  card.className = 'iter-card iter-card--live' + (loop._supervisor ? ' iter-card--supervisor' : '');

  // Header.
  const header = document.createElement('div');
  header.className = 'iter-card__header';

  const elapsed = document.createElement('span');
  elapsed.className = 'iter-card__elapsed';
  elapsed.id = 'detail-elapsed';
  elapsed.textContent = loop._iterStartTs ? formatDuration(Date.now() - loop._iterStartTs) : '0s';

  const model = document.createElement('span');
  model.className = 'iter-card__model';
  model.textContent = shortModelName(loop._liveModel || loop._lastModel || '');

  const liveLabel = document.createElement('span');
  liveLabel.className = 'iter-card__live-label';
  liveLabel.textContent = loop._supervisor ? 'SUPERVISOR' : 'LIVE';

  // Show which iteration number is running (next after completed count).
  const iterNum = document.createElement('span');
  iterNum.className = 'iter-card__iter-num';
  iterNum.textContent = '#' + ((loop.iterations || 0) + 1);

  header.appendChild(liveLabel);
  header.appendChild(iterNum);
  header.appendChild(elapsed);
  header.appendChild(model);
  card.appendChild(header);

  const ctx = loop._llmContext;
  const liveRequestID = loop._currentRequestID || (ctx && ctx.request_id) || '';
  const liveTooling = normalizeTooling(ctx && ctx.tooling, {
    configuredTags: loop.tooling && loop.tooling.configured_tags,
    loadedTags: Array.isArray(ctx && ctx.active_tags) && ctx.active_tags.length > 0
      ? ctx.active_tags
      : (Array.isArray(loop.active_tags) ? loop.active_tags : []),
    effectiveTools: Array.isArray(ctx && ctx.effective_tools) ? ctx.effective_tools : [],
    excludedTools: loop.tooling && loop.tooling.excluded_tools,
  });
  const activeTags = liveTooling.loadedTags;
  const loadedCapabilities = liveTooling.loadedCapabilities;
  const effectiveTools = liveTooling.effectiveTools;
  const summary = document.createElement('div');
  summary.className = 'iter-card__summary-line';
  const summaryBits = [];
  const activeToolCalls = loop._liveTools ? loop._liveTools.length : 0;
  if (activeToolCalls > 0) {
    summaryBits.push('Current turn has ' + activeToolCalls + ' active tool call' + (activeToolCalls === 1 ? '' : 's'));
  } else if (loop._liveModel) {
    summaryBits.push('Current turn is sampling on ' + shortModelName(loop._liveModel));
  } else {
    summaryBits.push('Current turn is active');
  }
  if (effectiveTools.length > 0) {
    summaryBits.push(effectiveTools.length + ' tools in scope');
  }
  if (loadedCapabilities.length > 0) {
    summaryBits.push(loadedCapabilities.length + ' capabilities loaded');
  }
  if (ctx && ctx.intent) summaryBits.push(ctx.intent.replace(/_/g, ' '));
  if (ctx && ctx.reasoning) summaryBits.push(ctx.reasoning);
  summary.textContent = summaryBits.join(' · ');
  card.appendChild(summary);

  // Context meter (if we have context info).
  const contextNumerator = (ctx && ctx.est_tokens) || loop.last_input_tokens || 0;
  if (loop.context_window && contextNumerator) {
    const pct = Math.min(100, (contextNumerator / loop.context_window) * 100);
    const meter = document.createElement('div');
    meter.className = 'context-meter';
    meter.innerHTML =
      '<span class="context-meter__label">Context load</span>' +
      '<div class="context-meter__track">' +
        '<div class="context-meter__fill' +
        (pct >= 80 ? ' context-meter__fill--crit' : pct >= 50 ? ' context-meter__fill--warn' : '') +
        '" style="width:' + pct.toFixed(1) + '%"></div>' +
      '</div>' +
      '<span class="context-meter__pct">' + Math.round(pct) + '%</span>';
    card.appendChild(meter);
  }

  // LLM call context line (from loop_llm_start enrichment).
  if (ctx && (ctx.est_tokens || ctx.messages)) {
    const info = document.createElement('div');
    info.className = 'iter-card__llm-context';
    const parts = [];
    if (ctx.est_tokens) parts.push('~' + formatTokens(ctx.est_tokens) + ' tokens');
    if (ctx.messages) parts.push(ctx.messages + ' msgs');
    if (ctx.tools) parts.push(ctx.tools + ' tools');
    if (ctx.complexity) parts.push(ctx.complexity);
    if (ctx.intent) parts.push(ctx.intent.replace(/_/g, ' '));
    info.textContent = parts.join(' \u00b7 ');
    if (ctx.reasoning) info.title = ctx.reasoning;
    card.appendChild(info);
  }

  const liveFacts = makeIterationFacts([
    { label: 'Turn', value: '#' + ((loop.iterations || 0) + 1) },
    { label: 'Request', value: liveRequestID ? shortID(liveRequestID) : '' },
    { label: 'Model', value: loop._liveModel ? shortModelName(loop._liveModel) : '' },
    { label: 'Messages', value: ctx && ctx.messages ? formatNumber(ctx.messages) : '' },
    { label: 'Estimated context', value: ctx && ctx.est_tokens ? formatTokens(ctx.est_tokens) : '' },
    { label: 'Active tools', value: activeToolCalls > 0 ? formatNumber(activeToolCalls) : (ctx && ctx.tools ? formatNumber(ctx.tools) : '') },
    { label: 'Tool surface', value: effectiveTools.length > 0 ? formatNumber(effectiveTools.length) : '' },
    { label: 'Loaded capabilities', value: loadedCapabilities.length > 0 ? loadedCapabilities.map((entry) => entry.tag).join(', ') : '' },
    { label: 'Complexity', value: ctx && ctx.complexity ? ctx.complexity : '' },
  ]);
  if (liveFacts) card.appendChild(liveFacts);

  if (liveRequestID && typeof window.onRequestChipClick === 'function') {
    const reqChip = document.createElement('span');
    reqChip.className = 'log-id-chip log-id-chip--clickable';
    reqChip.textContent = 'req:' + shortID(liveRequestID);
    reqChip.title = 'Click to inspect request\n' + liveRequestID;
    reqChip.addEventListener('click', (e) => {
      e.stopPropagation();
      if (e.shiftKey) {
        navigator.clipboard.writeText(liveRequestID);
        return;
      }
      window.onRequestChipClick(liveRequestID);
    });
    card.appendChild(reqChip);
  }

  const liveScope = makeIterationScopePanel([
    loadedCapabilities.length > 0
      ? { label: 'Loaded capabilities', capabilities: loadedCapabilities, className: 'tag-chip tag-chip--active' }
      : null,
    effectiveTools.length > 0
      ? { label: 'Tool surface', values: effectiveTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
  ]);
  if (liveScope) card.appendChild(liveScope);

  // Live tool list.
  const tools = loop._liveTools || [];
  if (tools.length > 0) {
    const ul = document.createElement('ul');
    ul.className = 'live-tools';
    for (const entry of tools) {
      const li = document.createElement('li');
      li.className = 'live-tool live-tool--' + entry.status;
      if (isDelegateTool(entry.tool)) {
        // Show delegate task inline instead of bare tool name.
        const parsed = parseDelegateArgs(entry.args);
        li.innerHTML = '';
        const icon = document.createElement('span');
        icon.className = 'live-tool__delegate-icon';
        icon.textContent = '\uD83D\uDD00';
        li.appendChild(icon);
        const taskSpan = document.createElement('span');
        taskSpan.className = 'live-tool__delegate-task';
        taskSpan.textContent = truncate(parsed.task || 'delegate', 80);
        li.appendChild(taskSpan);
        const label = parsed.profile || delegateToolLabel(entry.tool);
        const prof = document.createElement('span');
        prof.className = 'live-tool__delegate-profile';
        prof.textContent = label;
        li.appendChild(prof);
      } else {
        li.textContent = entry.tool;
      }
      li.title = formatToolTooltip(entry);
      ul.appendChild(li);
    }
    card.appendChild(ul);
  }

  return card;
}

// buildPastCard creates a collapsible card for a completed iteration.
function buildPastCard(snap, handlerOnly, idx, startExpanded) {
  const card = document.createElement('div');
  const isError = !!snap.error;
  card.className = 'iter-card iter-card--past' + (isError ? ' iter-card--error' : '') + (snap.supervisor ? ' iter-card--supervisor' : '') + (startExpanded ? ' iter-card--expanded' : '');
  card.dataset.idx = idx;

  // Header (always visible, clickable to expand).
  const header = document.createElement('div');
  header.className = 'iter-card__header';

  const status = document.createElement('span');
  status.className = 'iter-card__status-pill'
    + (isError ? ' iter-card__status-pill--error' : '')
    + (snap.supervisor ? ' iter-card__status-pill--supervisor' : '')
    + (!isError && !snap.supervisor ? ' iter-card__status-pill--ok' : '');
  status.textContent = buildPastIterationStatusLabel(snap, handlerOnly);

  const title = document.createElement('span');
  title.className = 'iter-card__title';
  title.textContent = buildPastIterationHeaderTitle(snap, handlerOnly);

  const dur = document.createElement('span');
  dur.className = 'iter-card__duration';
  dur.textContent = snap.elapsed_ms ? formatDuration(snap.elapsed_ms) : '';

  const chevron = document.createElement('span');
  chevron.className = 'iter-card__chevron';
  chevron.textContent = startExpanded ? '\u25be' : '\u25b8';

  header.appendChild(status);
  header.appendChild(title);
  const spacer = document.createElement('span');
  spacer.className = 'iter-card__spacer';
  header.appendChild(spacer);

  // Wall-clock timestamp (HH:MM).
  if (snap.completed_at) {
    const ts = document.createElement('span');
    ts.className = 'iter-card__time';
    ts.textContent = formatTimeShort(new Date(snap.completed_at));
    header.appendChild(ts);
  }

  header.appendChild(dur);
  header.appendChild(chevron);
  card.appendChild(header);

  const preview = buildPastIterationSummary(snap, handlerOnly);
  if (preview) {
    const previewEl = document.createElement('div');
    previewEl.className = 'iter-card__preview';
    previewEl.textContent = preview;
    card.appendChild(previewEl);
  }

  // Body (hidden by default, toggled on click).
  const body = document.createElement('div');
  body.className = 'iter-card__body';
  body.hidden = !startExpanded;

  const toolCalls = countToolCalls(snap.tools_used);
  const summarySignals = countSummarySignals(snap.summary);
  const snapTooling = normalizeTooling(snap.tooling, {
    loadedTags: Array.isArray(snap.active_tags) ? snap.active_tags : [],
    effectiveTools: Array.isArray(snap.effective_tools) ? snap.effective_tools : [],
    toolsUsed: snap.tools_used || null,
  });
  const activeTags = snapTooling.loadedTags;
  const loadedCapabilities = snapTooling.loadedCapabilities;
  const effectiveTools = snapTooling.effectiveTools;
  const facts = makeIterationFacts([
    { label: 'Health', value: buildPastIterationHealth(snap) },
    { label: 'Request', value: snap.request_id ? shortID(snap.request_id) : '' },
    { label: 'Duration', value: snap.elapsed_ms ? formatDuration(snap.elapsed_ms) : '' },
    { label: 'Tool calls', value: toolCalls > 0 ? formatNumber(toolCalls) : '' },
    { label: 'Tool surface', value: effectiveTools.length > 0 ? formatNumber(effectiveTools.length) : '' },
    { label: 'Loaded capabilities', value: loadedCapabilities.length > 0 ? loadedCapabilities.map((entry) => entry.tag).join(', ') : '' },
    !handlerOnly ? { label: 'Input tokens', value: snap.input_tokens ? formatTokens(snap.input_tokens) : '' } : { label: 'Handler signals', value: summarySignals > 0 ? formatNumber(summarySignals) : '' },
    !handlerOnly ? { label: 'Output tokens', value: snap.output_tokens ? formatTokens(snap.output_tokens) : '' } : { label: 'When', value: snap.completed_at ? timeAgo(new Date(snap.completed_at)) : '' },
    !handlerOnly ? { label: 'Context window', value: snap.context_window ? formatNumber(snap.context_window) : '' } : null,
    !handlerOnly ? { label: 'When', value: snap.completed_at ? timeAgo(new Date(snap.completed_at)) : '' } : null,
  ]);
  if (facts) body.appendChild(facts);

  // Token info (skip for handler-only).
  if (!handlerOnly && (snap.input_tokens || snap.output_tokens)) {
    const tokens = document.createElement('div');
    tokens.className = 'iter-card__tokens';
    tokens.textContent = formatTokens(snap.input_tokens || 0) + ' in / ' + formatTokens(snap.output_tokens || 0) + ' out';
    body.appendChild(tokens);
  }

  // Request detail link (when content retention is enabled and the
  // request ID is available). Clicking opens the request detail panel.
  if (snap.request_id && typeof window.onRequestChipClick === 'function') {
    const reqChip = document.createElement('span');
    reqChip.className = 'log-id-chip log-id-chip--clickable';
    reqChip.textContent = 'req:' + shortID(snap.request_id);
    reqChip.title = 'Click to inspect request\n' + snap.request_id;
    reqChip.addEventListener('click', (e) => {
      e.stopPropagation();
      if (e.shiftKey) {
        navigator.clipboard.writeText(snap.request_id);
        return;
      }
      window.onRequestChipClick(snap.request_id);
    });
    body.appendChild(reqChip);
  }

  const pastScope = makeIterationScopePanel([
    loadedCapabilities.length > 0
      ? { label: 'Loaded capabilities', capabilities: loadedCapabilities, className: 'tag-chip tag-chip--active' }
      : null,
    effectiveTools.length > 0
      ? { label: 'Tool surface', values: effectiveTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
  ]);
  if (pastScope) body.appendChild(pastScope);

  // Tool chips.
  const toolsUsed = snap.tools_used;
  if (toolsUsed && Object.keys(toolsUsed).length > 0) {
    const toolsDiv = document.createElement('div');
    toolsDiv.className = 'iter-card__tools';
    for (const [name, count] of Object.entries(toolsUsed)) {
      const chip = document.createElement('span');
      chip.className = 'iter-card__tool-item';
      chip.textContent = name + (count > 1 ? ' \u00d7' + count : '');
      toolsDiv.appendChild(chip);
    }
    body.appendChild(toolsDiv);
  }

  // Delegate calls (rich inline display for thane_now/thane_assign tool uses).
  if (snap.delegate_calls && snap.delegate_calls.length > 0) {
    const delDiv = document.createElement('div');
    delDiv.className = 'iter-card__delegates';
    for (const dc of snap.delegate_calls) {
      const item = document.createElement('div');
      item.className = 'iter-card__delegate-call';
      const icon = document.createElement('span');
      icon.className = 'iter-card__delegate-icon';
      icon.textContent = '\uD83D\uDD00';
      item.appendChild(icon);
      const task = document.createElement('span');
      task.className = 'iter-card__delegate-task';
      task.textContent = truncate(dc.task || 'delegate', 100);
      item.appendChild(task);
      if (dc.profile) {
        const prof = document.createElement('span');
        prof.className = 'iter-card__delegate-profile';
        prof.textContent = dc.profile;
        item.appendChild(prof);
      }
      if (dc.status === 'error' && dc.error) {
        item.classList.add('iter-card__delegate-call--error');
        item.title = dc.error;
      }
      delDiv.appendChild(item);
    }
    body.appendChild(delDiv);
  }

  // Summary stats (handler-reported metrics).
  if (snap.summary && Object.keys(snap.summary).length > 0) {
    const summaryDiv = document.createElement('div');
    summaryDiv.className = 'iter-card__summary';
    for (const [key, value] of Object.entries(snap.summary)) {
      const item = document.createElement('span');
      item.className = 'iter-card__summary-item';
      item.textContent = key.replace(/_/g, ' ') + ': ' + value;
      summaryDiv.appendChild(item);
    }
    body.appendChild(summaryDiv);
  }

  // Error text.
  if (snap.error) {
    const errEl = document.createElement('div');
    errEl.className = 'iter-card__error';
    errEl.textContent = snap.error;
    body.appendChild(errEl);
  }

  // Supervisor badge.
  if (snap.supervisor) {
    const supEl = document.createElement('span');
    supEl.className = 'iter-card__sup-badge';
    supEl.textContent = '\u2726 supervisor';
    body.appendChild(supEl);
  }

  // Timestamp.
  if (snap.completed_at) {
    const ts = document.createElement('div');
    ts.className = 'iter-card__timestamp';
    ts.textContent = formatTime(new Date(snap.completed_at));
    body.appendChild(ts);
  }

  card.appendChild(body);

  // Toggle expand/collapse on header click.
  header.addEventListener('click', () => {
    body.hidden = !body.hidden;
    chevron.textContent = body.hidden ? '\u25b8' : '\u25be';
    card.classList.toggle('iter-card--expanded', !body.hidden);
  });

  return card;
}

// buildConnector creates the vertical line + label between iteration cards.
//
//   loop:        loop data object (needs .state, .config)
//   snap:        iteration snapshot (for historical sleep_after_ms / wait_after)
//   isLive:      true for the connector above the live card
//   sleepTimer:  current sleep timer object { startedAt, durationMs } or null
function buildConnector(loop, snap, isLive, sleepTimer) {
  const conn = document.createElement('div');
  conn.className = 'iter-connector';

  const line = document.createElement('div');
  line.className = 'iter-connector__line';
  conn.appendChild(line);

  const label = document.createElement('span');
  label.className = 'iter-connector__label';

  if (isLive) {
    // Live connector: sleep/wait state + optional supervisor odds.
    let sleepText = '';
    if (loop.state === 'sleeping') {
      if (sleepTimer && sleepTimer.durationMs > 0) {
        const remaining = sleepTimer.durationMs - (Date.now() - sleepTimer.startedAt);
        sleepText = remaining > 0 ? 'sleeping ' + formatDuration(remaining) : 'waking up...';
      } else {
        sleepText = 'sleeping';
      }
    } else if (loop.state === 'waiting') {
      sleepText = 'awaiting event';
    }
    label.appendChild(document.createTextNode(sleepText));

    // Append supervisor odds inline (sleeping LLM loops only).
    const cfg = loop.config || {};
    if (loop.state === 'sleeping' && cfg.Supervisor && cfg.SupervisorProb > 0) {
      const pct = Math.round(cfg.SupervisorProb * 100);
      label.appendChild(document.createTextNode(' \u00b7 '));
      const supSpan = document.createElement('span');
      supSpan.className = 'iter-connector__sup';
      supSpan.textContent = pct + '% supervisor odds';
      label.appendChild(supSpan);
    }
    conn.appendChild(label);
  } else {
    // Historical connector: how long the sleep/wait was.
    if (snap.sleep_after_ms) {
      label.textContent = 'slept ' + formatDuration(snap.sleep_after_ms);
    } else if (snap.wait_after) {
      label.textContent = 'waited';
    }
    conn.appendChild(label);
  }

  return conn;
}

// ---------------------------------------------------------------------------
// Shared Constants
// ---------------------------------------------------------------------------

const MAX_ITERATION_HISTORY = 10;

// ---------------------------------------------------------------------------
// Shared Loop State Helpers
// ---------------------------------------------------------------------------

// clearLiveTelemetry resets transient per-iteration state on a loop object.
function clearLiveTelemetry(loop) {
  loop._iterStartTs = null;
  loop._liveTools = [];
  loop._liveModel = '';
  loop._llmContext = null;
  loop._currentRequestID = '';
}

// ---------------------------------------------------------------------------
// applyLoopEventToLoop — shared SSE event handler
// ---------------------------------------------------------------------------
//
// Mutates ctx.loop in place based on the SSE event. Manages sleep timers
// and annotates iteration history. Returns { snapshot } when an iteration
// or error snapshot was produced, or null otherwise.
//
// ctx: { loop, loopId, sleepTimers (Map), history (array, newest-first) }
function applyLoopEventToLoop(evt, ctx) {
  const loop = ctx.loop;
  const d = evt.data || {};

  switch (evt.kind) {
    case 'loop_stopped':
      loop.state = 'stopped';
      loop.iterations = d.iterations || loop.iterations;
      loop.attempts = d.attempts || loop.attempts;
      return null;

    case 'loop_state_change':
      loop.state = d.to;
      if (d.to !== 'processing') clearLiveTelemetry(loop);
      return null;

    case 'loop_iteration_start':
      loop.state = 'processing';
      loop.last_wake_at = evt.ts;
      loop._supervisor = !!d.supervisor;
      loop.attempts = d.attempt || loop.attempts;
      loop._currentConvID = d.conversation_id || null;
      loop._currentRequestID = d.request_id || '';
      ctx.sleepTimers.delete(ctx.loopId);
      loop._liveTools = [];
      loop._liveModel = '';
      loop._llmContext = null;
      loop._iterStartTs = Date.now();
      return null;

    case 'loop_iteration_complete': {
      loop._lastModel = d.model;
      loop._lastSupervisor = loop._supervisor || false;
      loop.total_input_tokens = (loop.total_input_tokens || 0) + (d.input_tokens || 0);
      loop.total_output_tokens = (loop.total_output_tokens || 0) + (d.output_tokens || 0);
      loop.last_input_tokens = d.input_tokens || 0;
      loop.last_output_tokens = d.output_tokens || 0;
      if (d.context_window > 0) loop.context_window = d.context_window;
      loop.iterations = (loop.iterations || 0) + 1;
      if (loop._supervisor) loop.last_supervisor_iter = loop.iterations;
      loop._supervisor = false;

      const tooling = normalizeTooling(d.tooling, {
        configuredTags: loop.tooling && loop.tooling.configured_tags,
        loadedTags: Array.isArray(d.active_tags) ? d.active_tags : [],
        effectiveTools: Array.isArray(d.effective_tools) ? d.effective_tools : [],
        excludedTools: loop.tooling && loop.tooling.excluded_tools,
        toolsUsed: d.tools_used || null,
      });
      loop.active_tags = tooling.loadedTags.slice();
      loop.tooling = {
        configured_tags: tooling.configuredTags.slice(),
        loaded_tags: tooling.loadedTags.slice(),
        loaded_capabilities: tooling.loadedCapabilities.slice(),
        effective_tools: tooling.effectiveTools.slice(),
        excluded_tools: tooling.excludedTools.slice(),
        tools_used: Object.keys(tooling.toolsUsed).length > 0 ? tooling.toolsUsed : null,
      };

      const delegateCalls = extractDelegateCalls(loop._liveTools);
      const snap = {
        number: loop.iterations,
        conv_id: d.conversation_id || loop._currentConvID || '',
        request_id: d.request_id || '',
        model: d.model || '',
        input_tokens: d.input_tokens || 0,
        output_tokens: d.output_tokens || 0,
        context_window: d.context_window || 0,
        tools_used: Object.keys(tooling.toolsUsed).length > 0 ? tooling.toolsUsed : buildToolCounts(loop._liveTools),
        effective_tools: tooling.effectiveTools.length > 0 ? tooling.effectiveTools.slice() : null,
        active_tags: tooling.loadedTags.length > 0 ? tooling.loadedTags.slice() : null,
        tooling: {
          configured_tags: tooling.configuredTags.slice(),
          loaded_tags: tooling.loadedTags.slice(),
          loaded_capabilities: tooling.loadedCapabilities.slice(),
          effective_tools: tooling.effectiveTools.slice(),
          excluded_tools: tooling.excludedTools.slice(),
          tools_used: Object.keys(tooling.toolsUsed).length > 0 ? tooling.toolsUsed : null,
        },
        elapsed_ms: d.elapsed_ms || 0,
        supervisor: loop._lastSupervisor || false,
        started_at: loop._iterStartTs ? new Date(loop._iterStartTs).toISOString() : evt.ts,
        completed_at: evt.ts,
        summary: d.summary || null,
        delegate_calls: delegateCalls.length > 0 ? delegateCalls : null,
      };
      clearLiveTelemetry(loop);
      return { snapshot: snap };
    }

    case 'loop_tool_start':
      if (!loop._liveTools) loop._liveTools = [];
      if (!loop._iterStartTs) loop._iterStartTs = Date.now();
      loop._liveTools.push({ tool: d.tool, status: 'running', args: d.args || null });
      return null;

    case 'loop_tool_done':
      if (loop._liveTools) {
        for (let i = loop._liveTools.length - 1; i >= 0; i--) {
          if (loop._liveTools[i].tool === d.tool && loop._liveTools[i].status === 'running') {
            loop._liveTools[i].status = d.error ? 'error' : 'done';
            loop._liveTools[i].result = d.result || null;
            loop._liveTools[i].error = d.error || null;
            break;
          }
        }
      }
      if (d.tooling) {
        const tooling = normalizeTooling(d.tooling, {
          configuredTags: loop.tooling && loop.tooling.configured_tags,
          loadedTags: Array.isArray(d.active_tags) ? d.active_tags : (Array.isArray(loop.active_tags) ? loop.active_tags : []),
          effectiveTools: Array.isArray(d.effective_tools) ? d.effective_tools : [],
          excludedTools: loop.tooling && loop.tooling.excluded_tools,
        });
        loop.active_tags = tooling.loadedTags.slice();
        loop.tooling = {
          configured_tags: tooling.configuredTags.slice(),
          loaded_tags: tooling.loadedTags.slice(),
          loaded_capabilities: tooling.loadedCapabilities.slice(),
          effective_tools: tooling.effectiveTools.slice(),
          excluded_tools: tooling.excludedTools.slice(),
          tools_used: Object.keys(tooling.toolsUsed).length > 0 ? tooling.toolsUsed : null,
        };
        if (!loop._llmContext) loop._llmContext = {};
        loop._llmContext.tooling = loop.tooling;
        loop._llmContext.active_tags = tooling.loadedTags.slice();
        loop._llmContext.effective_tools = tooling.effectiveTools.slice();
      }
      return null;

    case 'loop_llm_start':
      loop._liveModel = d.model || '';
      loop._currentRequestID = d.request_id || loop._currentRequestID || '';
      const liveTooling = normalizeTooling(d.tooling, {
        configuredTags: loop.tooling && loop.tooling.configured_tags,
        loadedTags: Array.isArray(d.active_tags) ? d.active_tags : (Array.isArray(loop.active_tags) ? loop.active_tags : []),
        effectiveTools: Array.isArray(d.effective_tools) ? d.effective_tools : [],
        excludedTools: loop.tooling && loop.tooling.excluded_tools,
      });
      loop._llmContext = {
        request_id: d.request_id || '',
        est_tokens: d.est_tokens || 0,
        messages: d.messages || 0,
        tools: d.tools || 0,
        effective_tools: liveTooling.effectiveTools.slice(),
        active_tags: liveTooling.loadedTags.slice(),
        tooling: {
          configured_tags: liveTooling.configuredTags.slice(),
          loaded_tags: liveTooling.loadedTags.slice(),
          loaded_capabilities: liveTooling.loadedCapabilities.slice(),
          effective_tools: liveTooling.effectiveTools.slice(),
          excluded_tools: liveTooling.excludedTools.slice(),
          tools_used: Object.keys(liveTooling.toolsUsed).length > 0 ? liveTooling.toolsUsed : null,
        },
        iteration: d.iteration,
        complexity: d.complexity || '',
        intent: d.intent || '',
        reasoning: d.reasoning || '',
      };
      if (!loop._iterStartTs) loop._iterStartTs = Date.now();
      return null;

    case 'loop_llm_response':
      loop._liveModel = d.model || '';
      loop._currentRequestID = d.request_id || loop._currentRequestID || '';
      if (!loop._iterStartTs) loop._iterStartTs = Date.now();
      return null;

    case 'loop_sleep_start': {
      loop.state = 'sleeping';
      clearLiveTelemetry(loop);
      const ms = parseDuration(d.sleep_duration || '');
      ctx.sleepTimers.set(ctx.loopId, { startedAt: Date.now(), durationMs: ms });
      if (ctx.history.length > 0) ctx.history[0].sleep_after_ms = ms;
      return null;
    }

    case 'loop_wait_start':
      loop.state = 'waiting';
      clearLiveTelemetry(loop);
      ctx.sleepTimers.delete(ctx.loopId);
      if (ctx.history.length > 0) ctx.history[0].wait_after = true;
      return null;

    case 'loop_error': {
      loop.state = 'error';
      loop.last_error = d.error || '';
      loop.consecutive_errors = (loop.consecutive_errors || 0) + 1;
      const errSnap = {
        number: 0,
        error: d.error || '',
        started_at: loop._iterStartTs ? new Date(loop._iterStartTs).toISOString() : evt.ts,
        completed_at: evt.ts,
        elapsed_ms: loop._iterStartTs ? Date.now() - loop._iterStartTs : 0,
        supervisor: loop._supervisor || false,
      };
      clearLiveTelemetry(loop);
      return { snapshot: errSnap };
    }

    default:
      return null;
  }
}

// ---------------------------------------------------------------------------
// Shared Rendering Helpers
// ---------------------------------------------------------------------------

// renderAggregates builds the one-line stats summary (iterations, tokens,
// age, last error) into the given DOM element.
function renderAggregates(loop, el) {
  el.innerHTML = '';
  const iter = loop._delegate
    ? (loop._delegateIterations || 0)
    : (loop.iterations || 0);
  const att = loop.attempts || 0;
  const failedAttempts = Math.max(0, att - iter);
  const totalTok = (loop.total_input_tokens || 0) + (loop.total_output_tokens || 0);
  const startedAt = parseTimestamp(loop.started_at);
  const lastWake = parseTimestamp(loop.last_wake_at);

  const summary = document.createElement('div');
  summary.className = 'agg-summary';
  if (loop.last_error) {
    summary.textContent = 'Recent execution ended with an issue. Review the latest error and recent turn details below.';
  } else if (loop.state === 'processing') {
    summary.textContent = 'Active turn is running now. Live telemetry below follows the current request, context load, and tool activity.';
  } else if (loop.state === 'waiting' && loop.event_driven) {
    summary.textContent = 'Event-driven loop is idle and waiting for the next trigger.';
  } else if (loop.state === 'sleeping') {
    summary.textContent = 'Timed loop is idle between turns and will wake on its next scheduled interval.';
  } else {
    summary.textContent = 'Recent loop health, throughput, and runtime totals for this anchor.';
  }
  el.appendChild(summary);

  const grid = document.createElement('div');
  grid.className = 'agg-grid';
  const stats = [
    buildAggregateStat('State', formatSchemaToken(loop.state || 'pending'), { emphasis: true }),
    buildAggregateStat('Loop mode', loop.event_driven ? 'event driven' : 'timed sleep'),
    buildAggregateStat('Successful turns', formatNumber(iter)),
    buildAggregateStat('Failed attempts', failedAttempts > 0 ? formatNumber(failedAttempts) : '0'),
    totalTok > 0 ? buildAggregateStat('Total tokens', formatTokens(totalTok)) : null,
    startedAt ? buildAggregateStat('Started', timeAgo(startedAt), { title: startedAt.toISOString() }) : null,
    lastWake ? buildAggregateStat('Last wake', timeAgo(lastWake), { title: lastWake.toISOString() }) : null,
    loop.context_window ? buildAggregateStat('Context window', formatNumber(loop.context_window)) : null,
    loop.consecutive_errors > 0 ? buildAggregateStat('Error streak', formatNumber(loop.consecutive_errors)) : buildAggregateStat('Health', 'clean'),
  ].filter(Boolean);

  for (const stat of stats) {
    grid.appendChild(stat);
  }
  el.appendChild(grid);

  if (loop.last_error) {
    const err = document.createElement('div');
    err.className = 'agg-error';
    err.textContent = loop.last_error;
    el.appendChild(err);
  }
}

// renderTimeline builds the vertical iteration timeline (live card +
// connectors + past cards) into the given container element.
//
//   loop:         loop data object
//   container:    DOM element to render into
//   history:      array of iteration snapshots (newest first)
//   sleepTimerId: key into sleepTimers map
//   sleepTimers:  Map of id → { startedAt, durationMs }
function renderTimeline(loop, container, history, sleepTimerId, sleepTimers) {
  // Preserve expanded card state across re-renders.
  const expanded = new Set();
  container.querySelectorAll('.iter-card--past.iter-card--expanded').forEach(el => {
    const idx = el.dataset.idx;
    if (idx != null) expanded.add(idx);
  });

  container.innerHTML = '';

  const isProcessing = loop.state === 'processing';
  const isSleeping = loop.state === 'sleeping';
  const isWaiting = loop.state === 'waiting';

  // Live card (shown during processing).
  if (isProcessing && loop._iterStartTs) {
    container.appendChild(buildLiveCard(loop));
    // Add a connector between the live card and past history.
    if (history.length > 0) {
      container.appendChild(buildConnector(loop, history[0], false, null));
    }
  }

  // Live connector (between sleep/wait position and first past card).
  if ((isSleeping || isWaiting) && history.length > 0) {
    container.appendChild(buildConnector(loop, history[0], true, sleepTimers.get(sleepTimerId)));
  }

  // Past iteration cards with connectors between them.
  for (let i = 0; i < history.length; i++) {
    container.appendChild(buildPastCard(history[i], loop.handler_only, i, expanded.has(String(i))));
    if (i < history.length - 1) {
      container.appendChild(buildConnector(loop, history[i], false, null));
    }
  }
}

// ---------------------------------------------------------------------------
// Request Detail / Waterfall Rendering
// ---------------------------------------------------------------------------

// makeCopyBtn creates a small "Copy" button that copies text to clipboard.
function makeCopyBtn(text, label) {
  const btn = document.createElement('button');
  btn.className = 'copy-btn';
  btn.textContent = label || 'Copy';
  btn.title = 'Copy to clipboard';
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    navigator.clipboard.writeText(text).then(() => {
      btn.textContent = 'Copied';
      btn.classList.add('copy-btn--copied');
      setTimeout(() => {
        btn.textContent = label || 'Copy';
        btn.classList.remove('copy-btn--copied');
      }, 1200);
    });
  });
  return btn;
}

// renderRequestDetail populates the request detail panel from API data.
//
//   detail:    object from GET /api/requests/{id}
//   els:       { ids, meta, content, waterfall } — DOM containers
function renderRequestDetail(detail, els) {
  // IDs section.
  els.ids.innerHTML = '';
  els.ids.appendChild(makeIDRow('request_id', detail.request_id));
  if (detail.prompt_hash) {
    els.ids.appendChild(makeIDRow('prompt_hash', detail.prompt_hash));
  }

  // Metadata bar.
  els.meta.innerHTML = '';
  const bar = document.createElement('div');
  bar.className = 'request-meta-bar';

  const items = [
    { label: 'model', value: detail.model },
    { label: 'iterations', value: String(detail.iteration_count) },
    { label: 'in', value: formatTokens(detail.input_tokens) },
    { label: 'out', value: formatTokens(detail.output_tokens) },
  ];
  if (detail.exhausted) {
    items.push({ label: 'exhausted', value: detail.exhaust_reason || 'yes', warn: true });
  }
  for (const item of items) {
    if (!item.value) continue;
    const el = document.createElement('span');
    el.className = 'request-meta-bar__item';

    const labelEl = document.createElement('span');
    labelEl.className = 'request-meta-bar__label';
    labelEl.textContent = item.label;

    const valueEl = document.createElement('span');
    valueEl.className = 'request-meta-bar__value' + (item.warn ? ' request-meta-bar__value--warn' : '');
    valueEl.textContent = item.value;

    el.appendChild(labelEl);
    el.appendChild(document.createTextNode(' '));
    el.appendChild(valueEl);
    bar.appendChild(el);
  }
  if (detail.tools_used) {
    for (const [name, count] of Object.entries(detail.tools_used)) {
      const el = document.createElement('span');
      el.className = 'request-meta-bar__item';

      const labelEl = document.createElement('span');
      labelEl.className = 'request-meta-bar__label';
      labelEl.textContent = name;

      const valueEl = document.createElement('span');
      valueEl.className = 'request-meta-bar__value';
      valueEl.textContent = '\u00d7' + count;

      el.appendChild(labelEl);
      el.appendChild(document.createTextNode(' '));
      el.appendChild(valueEl);
      bar.appendChild(el);
    }
  }
  els.meta.appendChild(bar);

  // Content sections (system prompt, user, assistant).
  els.content.innerHTML = '';

  if (detail.system_prompt) {
    els.content.appendChild(buildContentSection('System Prompt', detail.system_prompt));
  }
  if (detail.user_content) {
    els.content.appendChild(buildContentSection('User', detail.user_content));
  }
  if (detail.assistant_content) {
    els.content.appendChild(buildContentSection('Assistant', detail.assistant_content));
  }

  // Tool call waterfall.
  els.waterfall.innerHTML = '';
  if (detail.tool_calls && detail.tool_calls.length > 0) {
    const title = document.createElement('div');
    title.className = 'waterfall__title';
    title.textContent = 'Tool Calls (' + detail.tool_calls.length + ')';
    els.waterfall.appendChild(title);

    for (const tc of detail.tool_calls) {
      els.waterfall.appendChild(buildWaterfallItem(tc));
    }
  }
}

// buildContentSection creates a collapsible section showing text content.
function buildContentSection(label, text) {
  const section = document.createElement('div');
  section.className = 'request-content__section';

  const header = document.createElement('div');
  header.className = 'request-content__header';

  const labelSpan = document.createElement('span');
  labelSpan.textContent = label;
  header.appendChild(labelSpan);
  header.appendChild(makeCopyBtn(text));

  const body = document.createElement('div');
  body.className = 'request-content__body';
  body.textContent = text;

  // Start collapsed if long.
  if (text.length > 300) {
    body.classList.add('request-content__body--collapsed');
  }

  header.addEventListener('click', (e) => {
    if (e.target.closest('.copy-btn')) return;
    body.classList.toggle('request-content__body--collapsed');
  });

  section.appendChild(header);
  section.appendChild(body);
  return section;
}

// buildWaterfallItem creates a single tool call entry in the waterfall.
function buildWaterfallItem(tc) {
  const item = document.createElement('div');
  item.className = 'waterfall__item';

  const bar = document.createElement('div');
  bar.className = 'waterfall__bar';

  const idx = document.createElement('span');
  idx.className = 'waterfall__index';
  idx.textContent = '#' + tc.iteration_index;

  const name = document.createElement('span');
  name.className = 'waterfall__name';
  name.textContent = tc.tool_name;

  const callId = document.createElement('span');
  callId.className = 'waterfall__call-id';
  callId.textContent = tc.tool_call_id ? shortID(tc.tool_call_id) : '';

  bar.appendChild(idx);
  bar.appendChild(name);
  bar.appendChild(callId);
  item.appendChild(bar);

  // Expandable detail section — content is lazily built on first expand
  // to avoid eagerly pretty-printing large JSON payloads for every item.
  const expand = document.createElement('div');
  expand.className = 'waterfall__expand';

  let expandInit = false;
  function ensureExpandedContent() {
    if (expandInit) return;
    expandInit = true;
    if (tc.arguments) {
      expand.appendChild(buildWaterfallSub('Arguments', formatJSON(tc.arguments)));
    }
    if (tc.result) {
      expand.appendChild(buildWaterfallSub('Result', tc.result));
    }
  }

  item.appendChild(expand);

  bar.addEventListener('click', () => {
    const isOpen = expand.classList.toggle('waterfall__expand--open');
    if (isOpen) ensureExpandedContent();
  });

  return item;
}

// buildWaterfallSub creates a labeled sub-section within a waterfall item.
function buildWaterfallSub(label, text) {
  const section = document.createElement('div');
  section.className = 'waterfall__sub-section';

  const labelEl = document.createElement('div');
  labelEl.className = 'waterfall__sub-label';

  const labelText = document.createElement('span');
  labelText.textContent = label;
  labelEl.appendChild(labelText);
  labelEl.appendChild(makeCopyBtn(text));

  const content = document.createElement('div');
  content.className = 'waterfall__sub-content';
  content.textContent = text;

  section.appendChild(labelEl);
  section.appendChild(content);
  return section;
}

// formatJSON tries to pretty-print a JSON string. Returns as-is if invalid.
function formatJSON(s) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch (_) {
    return s;
  }
}
