// Detail popup — standalone window for inspecting a single node.
// Reads ?type=loop&id=xxx or ?type=system from URL params.

'use strict';

const $ = (sel) => document.querySelector(sel);
const logEmpty = $('#log-empty');
const logScroll = $('#log-scroll');
const logBody = $('#log-body');
const connDot = $('#conn-dot');
const pollSlider = $('#poll-rate');
const pollLabel = $('#poll-rate-label');

// ---------------------------------------------------------------------------
// Connection Status (dot indicator instead of status bar)
// ---------------------------------------------------------------------------

function setConnStatus(state, detail) {
  connDot.className = 'conn-dot' + (state === 'ok' ? ' conn-dot--ok' : state === 'err' ? ' conn-dot--err' : '');
  connDot.title = detail || state;
}

// ---------------------------------------------------------------------------
// Dynamic Log Poll Rate
// ---------------------------------------------------------------------------

let logPollInterval = null;

function startLogPoll() {
  if (logPollInterval) clearInterval(logPollInterval);
  const secs = parseInt(pollSlider.value, 10);
  pollLabel.textContent = secs + 's';
  const fetchFn = nodeType === 'system' ? fetchSystemLogs : fetchLoopLogs;
  logPollInterval = setInterval(fetchFn, secs * 1000);
}

pollSlider.addEventListener('input', () => {
  pollLabel.textContent = parseInt(pollSlider.value, 10) + 's';
  startLogPoll();
});

// Parse URL params.
const urlParams = new URLSearchParams(window.location.search);
const nodeType = urlParams.get('type') || 'loop';
const nodeId = urlParams.get('id') || '';
const nodeName = urlParams.get('name') || '';

// Set title immediately from URL param, refined later from SSE data.
document.title = 'Thane \u00b7 ' + (nodeName || (nodeType === 'system' ? 'Runtime' : nodeId.slice(0, 8)));

// ---------------------------------------------------------------------------
// Helpers (duplicated from app.js — no module system)
// ---------------------------------------------------------------------------

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

function formatNumber(n) {
  if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

function formatTokens(n) {
  if (n >= 1000000) return (n / 1000000).toFixed(2) + 'M';
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

function formatDuration(ms) {
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

function shortID(id) {
  if (!id) return '';
  if (id.length > 12) return id.slice(0, 8);
  return id;
}

// ---------------------------------------------------------------------------
// Live Telemetry Timers
// ---------------------------------------------------------------------------

function clearLiveTelemetry() {
  if (loopData) {
    loopData._iterStartTs = null;
    loopData._liveTools = [];
    loopData._liveModel = '';
  }
}

// ---------------------------------------------------------------------------
// Log Rendering (duplicated from app.js)
// ---------------------------------------------------------------------------

function renderLogs(entries) {
  if (!entries || entries.length === 0) {
    logEmpty.hidden = false;
    logEmpty.querySelector('p').textContent = 'No log entries found';
    logBody.innerHTML = '';
    return;
  }

  logEmpty.hidden = true;

  const atBottom = logScroll.scrollHeight - logScroll.scrollTop - logScroll.clientHeight < 24;
  logBody.innerHTML = '';

  for (const entry of entries) {
    const tr = document.createElement('tr');

    const tdTime = document.createElement('td');
    tdTime.className = 'log-time';
    tdTime.textContent = entry.Timestamp ? formatTime(new Date(entry.Timestamp)) : '';

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
    logBody.appendChild(tr);
  }

  if (atBottom) {
    logScroll.scrollTop = logScroll.scrollHeight;
  }
}

function buildLogDetail(td, entry) {
  const parts = [];

  if (entry.Model) {
    parts.push({ key: 'model', val: entry.Model, cls: 'model' });
  }
  if (entry.Tool) {
    parts.push({ key: 'tool', val: entry.Tool, cls: 'tool' });
  }

  let attrs = null;
  if (entry.Attrs) {
    try { attrs = JSON.parse(entry.Attrs); } catch (_) { /* ignore */ }
  }
  if (attrs) {
    for (const k of ['duration', 'elapsed', 'latency', 'took']) {
      if (attrs[k] != null) {
        parts.push({ key: k, val: String(attrs[k]), cls: 'duration' });
      }
    }
    if (attrs.input_tokens != null) {
      parts.push({ key: 'in', val: formatTokens(attrs.input_tokens), cls: 'tokens' });
    }
    if (attrs.output_tokens != null) {
      parts.push({ key: 'out', val: formatTokens(attrs.output_tokens), cls: 'tokens' });
    }
    if (attrs.total_tokens != null && attrs.input_tokens == null) {
      parts.push({ key: 'tokens', val: formatTokens(attrs.total_tokens), cls: 'tokens' });
    }
    if (attrs.tool_calls != null) {
      parts.push({ key: 'tools', val: String(attrs.tool_calls), cls: 'tool' });
    }
    if (attrs.tool_count != null && attrs.tool_calls == null) {
      parts.push({ key: 'tools', val: String(attrs.tool_count), cls: 'tool' });
    }
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

  const ids = [
    { label: 'req', full: entry.RequestID },
    { label: 'conv', full: entry.ConversationID },
    { label: 'sess', full: entry.SessionID },
  ];
  for (const { label, full } of ids) {
    if (!full) continue;
    const chip = document.createElement('span');
    chip.className = 'log-id-chip';
    chip.textContent = label + ':' + shortID(full);
    chip.title = label + ' \u2014 click to copy\n' + full;
    chip.addEventListener('click', (e) => {
      e.stopPropagation();
      navigator.clipboard.writeText(full).then(() => {
        chip.classList.add('log-id-chip--copied');
        setTimeout(() => chip.classList.remove('log-id-chip--copied'), 1200);
      });
    });
    td.appendChild(chip);
  }

  if (attrs) {
    td.title = JSON.stringify(attrs, null, 2);
  }
}

// ---------------------------------------------------------------------------
// System Mode
// ---------------------------------------------------------------------------

let systemStartTime = null;

function initSystem() {
  $('#system-detail').hidden = false;

  fetchSystemStatus();
  fetchSystemLogs();
  setInterval(fetchSystemStatus, 10000);
  startLogPoll();
  setInterval(updateSystemUptime, 1000);
}

async function fetchSystemStatus() {
  try {
    const resp = await fetch('/api/system');
    if (!resp.ok) return;
    const sys = await resp.json();

    if (sys.uptime) {
      const uptimeMs = parseDuration(sys.uptime);
      systemStartTime = Date.now() - uptimeMs;
    }

    // Status badge.
    const badge = $('#system-status');
    badge.textContent = sys.status || 'unknown';
    badge.className = 'state-badge state-badge--' +
      (sys.status === 'healthy' ? 'sleeping' : 'error');

    // Services.
    const container = $('#system-services');
    container.innerHTML = '';
    const health = sys.health || {};
    for (const [key, svc] of Object.entries(health)) {
      const row = document.createElement('div');
      row.className = 'system-svc-row';
      const dot = document.createElement('span');
      dot.className = 'system-svc-dot system-svc-dot--' + (svc.ready ? 'ok' : 'err');
      row.appendChild(dot);
      const name = document.createElement('span');
      name.className = 'system-svc-name';
      name.textContent = svc.name || key;
      row.appendChild(name);
      if (!svc.ready && svc.last_error) {
        const err = document.createElement('span');
        err.className = 'system-svc-error';
        err.textContent = svc.last_error;
        row.appendChild(err);
      }
      container.appendChild(row);
    }

    updateSystemUptime();
    const ver = sys.version || {};
    $('#system-version').textContent = ver.version || '-';
    $('#system-commit').textContent = ver.git_commit ? ver.git_commit.slice(0, 7) : '-';
    $('#system-go').textContent = ver.go_version || '-';
    $('#system-arch').textContent = (ver.os || '') + '/' + (ver.arch || '') || '-';

    setConnStatus('ok', 'Connected \u2014 last updated ' + formatTime(new Date()));
  } catch (err) {
    setConnStatus('err', 'Error: ' + err.message);
  }
}

function updateSystemUptime() {
  if (systemStartTime === null) return;
  const ms = Date.now() - systemStartTime;
  $('#system-uptime').textContent = formatUptimeLong(ms);
}

async function fetchSystemLogs() {
  const level = $('#log-level').value;
  let url = '/api/system/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);
  try {
    const resp = await fetch(url);
    if (!resp.ok) return;
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch system logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Loop Mode
// ---------------------------------------------------------------------------

let loopData = null;
const events = [];
const MAX_EVENTS = 50;
const MAX_ITERATION_HISTORY = 10;
const sleepTimers = new Map();
let iterationHistory = [];

function initLoop() {
  if (!nodeId) {
    setConnStatus('err', 'Error: no loop ID specified');
    return;
  }

  $('#loop-detail').hidden = false;

  connectSSE();
  fetchLoopLogs();
  startLogPoll();
  setInterval(tickLoop, 1000);
}

function connectSSE() {
  setConnStatus('connecting', 'Connecting...');
  const es = new EventSource('/api/loops/events');

  es.addEventListener('snapshot', (e) => {
    const statuses = JSON.parse(e.data);
    const match = statuses.find(s => s.id === nodeId);
    if (match) {
      // Seed iteration history from server-side ring buffer.
      if (match.recent_iterations && match.recent_iterations.length > 0) {
        iterationHistory = match.recent_iterations.slice();
      } else {
        iterationHistory = [];
      }
      // Seed live telemetry for a loop already in processing state.
      if (match.state === 'processing') {
        match._iterStartTs = match.last_wake_at ? new Date(match.last_wake_at).getTime() : Date.now();
        match._liveTools = [];
        match._liveModel = '';
      }
      loopData = match;
      document.title = 'Thane \u00b7 ' + (match.name || nodeId.slice(0, 8));
      renderLoopDetail();
    }
    setConnStatus('ok', 'Connected \u2014 ' + formatTime(new Date()));
  });

  es.addEventListener('loop', (e) => {
    const evt = JSON.parse(e.data);
    if (evt.data && evt.data.loop_id === nodeId) {
      events.unshift(evt);
      if (events.length > MAX_EVENTS) events.pop();
      applyLoopEvent(evt);
      renderLoopDetail();
    }
  });

  es.addEventListener('error', () => {
    setConnStatus('err', 'Disconnected \u2014 reconnecting...');
  });

  es.addEventListener('open', () => {
    setConnStatus('ok', 'Connected');
  });
}

function applyLoopEvent(evt) {
  if (!loopData) return;
  const d = evt.data || {};

  switch (evt.kind) {
    case 'loop_iteration_start':
      loopData.state = 'processing';
      loopData.attempts = (loopData.attempts || 0) + 1;
      loopData._supervisor = !!d.supervisor;
      loopData._currentConvID = d.conversation_id || loopData._currentConvID;
      loopData._liveTools = [];
      loopData._liveModel = '';
      loopData._iterStartTs = Date.now();
      break;
    case 'loop_iteration_complete': {
      loopData.iterations = (loopData.iterations || 0) + 1;
      loopData._lastModel = d.model || loopData._lastModel;
      loopData._lastSupervisor = loopData._supervisor;
      if (loopData._supervisor) {
        loopData.last_supervisor_iter = loopData.iterations;
      }
      loopData._supervisor = false;
      if (d.input_tokens) {
        loopData.total_input_tokens = (loopData.total_input_tokens || 0) + d.input_tokens;
        loopData.last_input_tokens = d.input_tokens;
      }
      if (d.output_tokens) {
        loopData.total_output_tokens = (loopData.total_output_tokens || 0) + d.output_tokens;
        loopData.last_output_tokens = d.output_tokens;
      }
      if (d.context_window > 0) loopData.context_window = d.context_window;
      // Build iteration snapshot.
      const snap = {
        number: loopData.iterations,
        conv_id: d.conversation_id || loopData._currentConvID || '',
        model: d.model || '',
        input_tokens: d.input_tokens || 0,
        output_tokens: d.output_tokens || 0,
        context_window: d.context_window || 0,
        tools_used: d.tools_used || buildToolCounts(loopData._liveTools),
        elapsed_ms: d.elapsed_ms || 0,
        supervisor: loopData._lastSupervisor || false,
        started_at: loopData._iterStartTs ? new Date(loopData._iterStartTs).toISOString() : evt.ts,
        completed_at: evt.ts,
        summary: d.summary || null,
      };
      iterationHistory.unshift(snap);
      if (iterationHistory.length > MAX_ITERATION_HISTORY) iterationHistory.length = MAX_ITERATION_HISTORY;
      clearLiveTelemetry();
      break;
    }
    case 'loop_sleep_start': {
      loopData.state = 'sleeping';
      clearLiveTelemetry();
      const ms = parseDuration(d.sleep_duration || '');
      if (ms > 0) {
        sleepTimers.set(nodeId, { startedAt: new Date(), durationMs: ms });
      }
      if (iterationHistory.length > 0) {
        iterationHistory[0].sleep_after_ms = ms;
      }
      break;
    }
    case 'loop_wait_start':
      loopData.state = 'waiting';
      clearLiveTelemetry();
      sleepTimers.delete(nodeId);
      if (iterationHistory.length > 0) {
        iterationHistory[0].wait_after = true;
      }
      break;
    case 'loop_error': {
      loopData.last_error = d.error;
      const errSnap = {
        number: 0,
        error: d.error || '',
        started_at: loopData._iterStartTs ? new Date(loopData._iterStartTs).toISOString() : evt.ts,
        completed_at: evt.ts,
        elapsed_ms: loopData._iterStartTs ? Date.now() - loopData._iterStartTs : 0,
        supervisor: loopData._supervisor || false,
      };
      iterationHistory.unshift(errSnap);
      if (iterationHistory.length > MAX_ITERATION_HISTORY) iterationHistory.length = MAX_ITERATION_HISTORY;
      clearLiveTelemetry();
      break;
    }
    case 'loop_state_change':
      loopData.state = d.to;
      if (d.to !== 'processing') clearLiveTelemetry();
      break;
    case 'loop_stopped':
      loopData.state = 'stopped';
      clearLiveTelemetry();
      break;
    case 'loop_tool_start':
      if (!loopData._liveTools) loopData._liveTools = [];
      if (!loopData._iterStartTs) {
        loopData._iterStartTs = Date.now();
      }
      loopData._liveTools.push({ tool: d.tool, status: 'running' });
      break;
    case 'loop_tool_done':
      if (loopData._liveTools) {
        for (let i = loopData._liveTools.length - 1; i >= 0; i--) {
          if (loopData._liveTools[i].tool === d.tool && loopData._liveTools[i].status === 'running') {
            loopData._liveTools[i].status = d.error ? 'error' : 'done';
            break;
          }
        }
      }
      break;
    case 'loop_llm_start':
      loopData._liveModel = d.model || '';
      if (!loopData._iterStartTs) {
        loopData._iterStartTs = Date.now();
      }
      break;
    case 'loop_llm_response':
      loopData._liveModel = d.model || '';
      if (!loopData._iterStartTs) {
        loopData._iterStartTs = Date.now();
      }
      break;
  }
}

function renderLoopDetail() {
  if (!loopData) return;

  $('#detail-name').textContent = loopData.name || loopData.id;

  const badge = $('#detail-state');
  const isSup = loopData._supervisor && loopData.state === 'processing';
  badge.textContent = isSup ? 'supervisor' : (loopData.state || 'unknown');
  badge.className = 'state-badge state-badge--' + (isSup ? 'supervisor' : (loopData.state || 'pending'));

  // IDs.
  const idsContainer = $('#detail-ids');
  idsContainer.innerHTML = '';
  if (loopData.id) idsContainer.appendChild(makeIDRow('loop_id', loopData.id));
  if (loopData.parent_id) idsContainer.appendChild(makeIDRow('parent_id', loopData.parent_id));
  if (loopData._currentConvID) idsContainer.appendChild(makeIDRow('conv_id', loopData._currentConvID));

  // Aggregate stats bar.
  renderAggregates();

  // Iteration timeline.
  renderTimeline();

  // Capabilities (tags from config).
  const tags = (loopData.config && loopData.config.Tags) || [];
  const tagsSection = $('#detail-tags');
  const tagsList = $('#detail-tags-list');
  if (tags.length > 0) {
    tagsSection.hidden = false;
    tagsList.innerHTML = '';
    for (const tag of tags) {
      const chip = document.createElement('span');
      chip.className = 'tag-chip';
      chip.textContent = tag;
      tagsList.appendChild(chip);
    }
  } else {
    tagsSection.hidden = true;
  }
}

function renderAggregates() {
  const el = $('#detail-aggregates');
  const parts = [];
  const iter = loopData.iterations || 0;
  const att = loopData.attempts || 0;
  parts.push(formatNumber(iter) + ' iter');
  if (att !== iter) parts.push(formatNumber(att) + ' att');
  const totalTok = (loopData.total_input_tokens || 0) + (loopData.total_output_tokens || 0);
  if (totalTok > 0) parts.push(formatTokens(totalTok) + ' tok');
  if (loopData.started_at) parts.push(timeAgo(new Date(loopData.started_at)));
  if (loopData.last_error) {
    parts.push('<span class="agg-error">' + escapeHTML(truncate(loopData.last_error, 40)) + '</span>');
  }
  el.innerHTML = parts.join(' <span class="agg-sep">\u00b7</span> ');
}

function renderTimeline() {
  const container = $('#detail-timeline');

  // Preserve expanded card state across re-renders.
  const expanded = new Set();
  container.querySelectorAll('.iter-card--past.iter-card--expanded').forEach(el => {
    const idx = el.dataset.idx;
    if (idx != null) expanded.add(idx);
  });

  container.innerHTML = '';

  const isProcessing = loopData.state === 'processing';
  const isSleeping = loopData.state === 'sleeping';
  const isWaiting = loopData.state === 'waiting';

  // Live card.
  if (isProcessing && loopData._iterStartTs) {
    container.appendChild(buildLiveCard());
  }

  // Live connector.
  if ((isSleeping || isWaiting) && iterationHistory.length > 0) {
    container.appendChild(buildConnector(iterationHistory[0], true));
  }

  // Past iteration cards.
  for (let i = 0; i < iterationHistory.length; i++) {
    container.appendChild(buildPastCard(iterationHistory[i], loopData.handler_only, i, expanded.has(String(i))));
    if (i < iterationHistory.length - 1) {
      container.appendChild(buildConnector(iterationHistory[i], false));
    }
  }
}

function buildLiveCard() {
  const card = document.createElement('div');
  card.className = 'iter-card iter-card--live';

  const header = document.createElement('div');
  header.className = 'iter-card__header';

  const elapsed = document.createElement('span');
  elapsed.className = 'iter-card__elapsed';
  elapsed.id = 'detail-elapsed';
  elapsed.textContent = loopData._iterStartTs ? formatDuration(Date.now() - loopData._iterStartTs) : '0s';

  const model = document.createElement('span');
  model.className = 'iter-card__model';
  model.textContent = shortModelName(loopData._liveModel || loopData._lastModel || '');

  const liveLabel = document.createElement('span');
  liveLabel.className = 'iter-card__live-label';
  liveLabel.textContent = loopData._supervisor ? 'SUPERVISOR' : 'LIVE';

  header.appendChild(liveLabel);
  header.appendChild(elapsed);
  header.appendChild(model);
  card.appendChild(header);

  // Context meter.
  if (loopData.context_window && loopData.last_input_tokens) {
    const pct = Math.min(100, (loopData.last_input_tokens / loopData.context_window) * 100);
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

  // Live tool list.
  const tools = loopData._liveTools || [];
  if (tools.length > 0) {
    const ul = document.createElement('ul');
    ul.className = 'live-tools';
    for (const entry of tools) {
      const li = document.createElement('li');
      li.className = 'live-tool live-tool--' + entry.status;
      li.textContent = entry.tool;
      ul.appendChild(li);
    }
    card.appendChild(ul);
  }

  return card;
}

function buildPastCard(snap, handlerOnly, idx, startExpanded) {
  const card = document.createElement('div');
  const isError = !!snap.error;
  card.className = 'iter-card iter-card--past' + (isError ? ' iter-card--error' : '') + (startExpanded ? ' iter-card--expanded' : '');
  card.dataset.idx = idx;

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

  const body = document.createElement('div');
  body.className = 'iter-card__body';
  body.hidden = !startExpanded;

  if (!handlerOnly && (snap.input_tokens || snap.output_tokens)) {
    const tokens = document.createElement('div');
    tokens.className = 'iter-card__tokens';
    tokens.textContent = formatTokens(snap.input_tokens || 0) + ' in / ' + formatTokens(snap.output_tokens || 0) + ' out';
    body.appendChild(tokens);
  }

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

  if (snap.error) {
    const errEl = document.createElement('div');
    errEl.className = 'iter-card__error';
    errEl.textContent = snap.error;
    body.appendChild(errEl);
  }

  if (snap.supervisor) {
    const supEl = document.createElement('span');
    supEl.className = 'iter-card__sup-badge';
    supEl.textContent = '\u2726 supervisor';
    body.appendChild(supEl);
  }

  if (snap.completed_at) {
    const ts = document.createElement('div');
    ts.className = 'iter-card__timestamp';
    ts.textContent = formatTime(new Date(snap.completed_at));
    body.appendChild(ts);
  }

  card.appendChild(body);

  header.addEventListener('click', () => {
    body.hidden = !body.hidden;
    chevron.textContent = body.hidden ? '\u25b8' : '\u25be';
    card.classList.toggle('iter-card--expanded', !body.hidden);
  });

  return card;
}

function buildConnector(snap, isLive) {
  const conn = document.createElement('div');
  conn.className = 'iter-connector';

  const line = document.createElement('div');
  line.className = 'iter-connector__line';
  conn.appendChild(line);

  const label = document.createElement('span');
  label.className = 'iter-connector__label';

  if (isLive) {
    // Single line: sleep/wait state + optional supervisor odds.
    let sleepText = '';
    if (loopData.state === 'sleeping') {
      const timer = sleepTimers.get(nodeId);
      if (timer && timer.durationMs > 0) {
        const remaining = timer.durationMs - (Date.now() - timer.startedAt.getTime());
        sleepText = remaining > 0 ? 'sleeping ' + formatDuration(remaining) : 'waking up...';
      } else {
        sleepText = 'sleeping';
      }
    } else if (loopData.state === 'waiting') {
      sleepText = 'awaiting event';
    }
    label.appendChild(document.createTextNode(sleepText));

    // Append supervisor odds inline (sleeping LLM loops only).
    const cfg = loopData.config || {};
    if (loopData.state === 'sleeping' && cfg.Supervisor && cfg.SupervisorProb > 0) {
      const pct = Math.round(cfg.SupervisorProb * 100);
      label.appendChild(document.createTextNode(' \u00b7 '));
      const supSpan = document.createElement('span');
      supSpan.className = 'iter-connector__sup';
      supSpan.textContent = pct + '% supervisor odds';
      label.appendChild(supSpan);
    }
    conn.appendChild(label);
  } else {
    if (snap.sleep_after_ms) {
      label.textContent = 'slept ' + formatDuration(snap.sleep_after_ms);
    } else if (snap.wait_after) {
      label.textContent = 'waited';
    }
    conn.appendChild(label);
  }

  return conn;
}

function shortModelName(model) {
  if (!model) return '';
  const m = model.replace(/-\d{8,}$/, '');
  return m.replace(/^claude-/, '').replace(/^gpt-/, '');
}

function buildToolCounts(liveTools) {
  if (!liveTools || liveTools.length === 0) return null;
  const counts = {};
  for (const t of liveTools) {
    counts[t.tool] = (counts[t.tool] || 0) + 1;
  }
  return counts;
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
  chip.title = 'Click to copy: ' + fullID;
  const txt = document.createElement('span');
  txt.className = 'id-chip-text';
  txt.textContent = fullID;
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

function tickLoop() {
  if (loopData) {
    renderLoopDetail();
  }
}

async function fetchLoopLogs() {
  const level = $('#log-level').value;
  let url = '/api/loops/' + encodeURIComponent(nodeId) + '/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);
  try {
    const resp = await fetch(url);
    if (!resp.ok) return;
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch loop logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Inspector Sidebar Resize
// ---------------------------------------------------------------------------

(function initResize() {
  const handle = document.getElementById('popup-resize');
  const inspector = document.getElementById('inspector');
  const mainEl = document.querySelector('.popup-main');
  let dragging = false;

  handle.addEventListener('mousedown', (e) => {
    e.preventDefault();
    dragging = true;
    handle.classList.add('popup-resize--active');
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  });

  document.addEventListener('mousemove', (e) => {
    if (!dragging) return;
    e.preventDefault();
    const mainRect = mainEl.getBoundingClientRect();
    const isLeft = document.body.classList.contains('inspector-left');
    let newWidth;
    if (isLeft) {
      // Inspector is on the left: width = mouse X relative to main left.
      newWidth = e.clientX - mainRect.left;
    } else {
      // Inspector is on the right: width = main right edge minus mouse X.
      newWidth = mainRect.right - e.clientX;
    }
    const clamped = Math.max(160, Math.min(newWidth, mainRect.width - 200));
    inspector.style.width = clamped + 'px';
  });

  document.addEventListener('mouseup', () => {
    if (!dragging) return;
    dragging = false;
    handle.classList.remove('popup-resize--active');
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  });
})();

// ---------------------------------------------------------------------------
// Swap Inspector Side
// ---------------------------------------------------------------------------

$('#toggle-side').addEventListener('click', () => {
  document.body.classList.toggle('inspector-left');
});

// ---------------------------------------------------------------------------
// Event Bindings
// ---------------------------------------------------------------------------

$('#log-level').addEventListener('change', () => {
  if (nodeType === 'system') fetchSystemLogs();
  else fetchLoopLogs();
});

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

if (nodeType === 'system') {
  initSystem();
} else {
  initLoop();
}
