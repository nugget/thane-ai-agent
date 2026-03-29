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
  const diff = Date.now() - date.getTime();
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

// parseDelegateArgs extracts task/profile/guidance from a thane_delegate
// tool call's args (which may be a JSON string or object).
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
    if (entry.tool !== 'thane_delegate') continue;
    const parsed = parseDelegateArgs(entry.args);
    calls.push({
      task: parsed.task || '',
      profile: parsed.profile || '',
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

// ---------------------------------------------------------------------------
// ID Chip Helpers
// ---------------------------------------------------------------------------

function makeIDRow(label, value) {
  const row = document.createElement('div');
  row.className = 'id-row';

  const lbl = document.createElement('span');
  lbl.className = 'id-label';
  lbl.textContent = label;
  row.appendChild(lbl);

  row.appendChild(makeIDChip(value));
  return row;
}

function makeIDChip(fullID) {
  const chip = document.createElement('span');
  chip.className = 'id-chip';
  chip.title = fullID;
  const txt = document.createElement('span');
  txt.className = 'id-chip-text';
  // Show first 8 chars (UUID first segment) — full value on hover/click.
  txt.textContent = fullID.length > 12 ? fullID.slice(0, 8) : fullID;
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

  // Context meter (if we have context info).
  if (loop.context_window && loop.last_input_tokens) {
    const pct = Math.min(100, (loop.last_input_tokens / loop.context_window) * 100);
    const meter = document.createElement('div');
    meter.className = 'context-meter';
    meter.innerHTML =
      '<span class="context-meter__label">Context</span>' +
      '<div class="context-meter__track">' +
        '<div class="context-meter__fill' +
        (pct >= 80 ? ' context-meter__fill--crit' : pct >= 50 ? ' context-meter__fill--warn' : '') +
        '" style="width:' + pct.toFixed(1) + '%"></div>' +
      '</div>' +
      '<span class="context-meter__pct">' + Math.round(pct) + '%</span>';
    card.appendChild(meter);
  }

  // LLM call context line (from loop_llm_start enrichment).
  const ctx = loop._llmContext;
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

  // Live tool list.
  const tools = loop._liveTools || [];
  if (tools.length > 0) {
    const ul = document.createElement('ul');
    ul.className = 'live-tools';
    for (const entry of tools) {
      const li = document.createElement('li');
      li.className = 'live-tool live-tool--' + entry.status;
      if (entry.tool === 'thane_delegate') {
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
        if (parsed.profile) {
          const prof = document.createElement('span');
          prof.className = 'live-tool__delegate-profile';
          prof.textContent = parsed.profile;
          li.appendChild(prof);
        }
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

  const num = document.createElement('span');
  num.className = 'iter-card__number';
  num.textContent = isError ? '\u2717' : '#' + (snap.number || '?');

  const model = document.createElement('span');
  model.className = 'iter-card__model';
  model.textContent = snap.model ? shortModelName(snap.model) : (handlerOnly ? 'handler' : '');

  const dur = document.createElement('span');
  dur.className = 'iter-card__duration';
  dur.textContent = snap.elapsed_ms ? formatDuration(snap.elapsed_ms) : '';

  const chevron = document.createElement('span');
  chevron.className = 'iter-card__chevron';
  chevron.textContent = startExpanded ? '\u25be' : '\u25b8';

  header.appendChild(num);
  header.appendChild(model);
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

  // Body (hidden by default, toggled on click).
  const body = document.createElement('div');
  body.className = 'iter-card__body';
  body.hidden = !startExpanded;

  // Token info (skip for handler-only).
  if (!handlerOnly && (snap.input_tokens || snap.output_tokens)) {
    const tokens = document.createElement('div');
    tokens.className = 'iter-card__tokens';
    tokens.textContent = formatTokens(snap.input_tokens || 0) + ' in / ' + formatTokens(snap.output_tokens || 0) + ' out';
    body.appendChild(tokens);
  }

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

  // Delegate calls (rich inline display for thane_delegate tool uses).
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

      const delegateCalls = extractDelegateCalls(loop._liveTools);
      const snap = {
        number: loop.iterations,
        conv_id: d.conversation_id || loop._currentConvID || '',
        model: d.model || '',
        input_tokens: d.input_tokens || 0,
        output_tokens: d.output_tokens || 0,
        context_window: d.context_window || 0,
        tools_used: d.tools_used || buildToolCounts(loop._liveTools),
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
      // Signal that active capabilities may have changed so the
      // caller can refetch loop status for updated active_tags.
      if (d.tool === 'request_capability' || d.tool === 'drop_capability') {
        return { capabilityChanged: true };
      }
      return null;

    case 'loop_llm_start':
      loop._liveModel = d.model || '';
      loop._llmContext = {
        est_tokens: d.est_tokens || 0,
        messages: d.messages || 0,
        tools: d.tools || 0,
        iteration: d.iteration,
        complexity: d.complexity || '',
        intent: d.intent || '',
        reasoning: d.reasoning || '',
      };
      if (!loop._iterStartTs) loop._iterStartTs = Date.now();
      return null;

    case 'loop_llm_response':
      loop._liveModel = d.model || '';
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
  const parts = [];
  // Delegate nodes track iterations via their completion event, not
  // the loop counter (which is never incremented for synthetic nodes).
  const iter = loop._delegate
    ? (loop._delegateIterations || 0)
    : (loop.iterations || 0);
  const att = loop.attempts || 0;
  parts.push(formatNumber(iter) + ' iter');
  if (!loop._delegate && att !== iter) parts.push(formatNumber(att) + ' att');
  const totalTok = (loop.total_input_tokens || 0) + (loop.total_output_tokens || 0);
  if (totalTok > 0) parts.push(formatTokens(totalTok) + ' tok');
  if (loop.started_at) parts.push(timeAgo(new Date(loop.started_at)));
  if (loop.last_error) {
    parts.push('<span class="agg-error">' + escapeHTML(truncate(loop.last_error, 40)) + '</span>');
  }
  el.innerHTML = parts.join(' <span class="agg-sep">\u00b7</span> ');
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
    el.innerHTML =
      '<span class="request-meta-bar__label">' + escapeHTML(item.label) + '</span> ' +
      '<span class="request-meta-bar__value' + (item.warn ? ' request-meta-bar__value--warn' : '') +
      '">' + escapeHTML(item.value) + '</span>';
    bar.appendChild(el);
  }
  if (detail.tools_used) {
    for (const [name, count] of Object.entries(detail.tools_used)) {
      const el = document.createElement('span');
      el.className = 'request-meta-bar__item';
      el.innerHTML =
        '<span class="request-meta-bar__label">' + escapeHTML(name) + '</span> ' +
        '<span class="request-meta-bar__value">&times;' + count + '</span>';
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
