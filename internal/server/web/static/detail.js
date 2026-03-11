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
const sleepTimers = new Map();

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
      break;
    case 'loop_iteration_complete':
      loopData.iterations = (loopData.iterations || 0) + 1;
      loopData._lastModel = d.model || loopData._lastModel;
      loopData._lastSupervisor = loopData._supervisor;
      loopData._supervisor = false;
      if (d.input_tokens) {
        loopData.total_input_tokens = (loopData.total_input_tokens || 0) + d.input_tokens;
        loopData.last_input_tokens = d.input_tokens;
      }
      if (d.output_tokens) loopData.total_output_tokens = (loopData.total_output_tokens || 0) + d.output_tokens;
      if (d.context_window > 0) loopData.context_window = d.context_window;
      break;
    case 'loop_sleep_start': {
      loopData.state = 'sleeping';
      const ms = parseDuration(d.sleep_duration || '');
      if (ms > 0) {
        sleepTimers.set(nodeId, { startedAt: new Date(), durationMs: ms });
      }
      break;
    }
    case 'loop_wait_start':
      loopData.state = 'waiting';
      sleepTimers.delete(nodeId);
      break;
    case 'loop_error':
      loopData.last_error = d.error;
      break;
    case 'loop_state_change':
      loopData.state = d.to;
      break;
    case 'loop_stopped':
      loopData.state = 'stopped';
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

  // Forward-looking section.
  const isSleeping = loopData.state === 'sleeping';
  const isWaiting = loopData.state === 'waiting';
  const hasSupervisor = loopData.config && loopData.config.Supervisor;
  const showForward = isSleeping || isWaiting || hasSupervisor;
  $('#detail-forward').hidden = !showForward;
  const sleepVisible = isSleeping || isWaiting;
  $('#detail-sleep-label').hidden = !sleepVisible;
  $('#detail-sleep').hidden = !sleepVisible;
  if (isWaiting) {
    $('#detail-sleep-label').textContent = 'Wait';
    $('#detail-sleep').textContent = 'awaiting event';
  } else {
    $('#detail-sleep-label').textContent = 'Sleep';
    updateSleepDisplay();
  }

  // Supervisor bar.
  renderSupervisorBar();

  // Context utilization meter.
  renderContextMeter();
  const hasContext = !$('#detail-context').hidden;
  $('#detail-divider').hidden = !(showForward || hasContext);

  // Historical metrics.
  $('#detail-iterations').textContent = formatNumber(loopData.iterations || 0);
  $('#detail-attempts').textContent = formatNumber(loopData.attempts || 0);
  $('#detail-input-tokens').textContent = loopData.total_input_tokens ? formatTokens(loopData.total_input_tokens) : '—';
  $('#detail-output-tokens').textContent = loopData.total_output_tokens ? formatTokens(loopData.total_output_tokens) : '—';
  $('#detail-model').textContent = loopData._lastModel || '-';
  $('#detail-error').textContent = loopData.last_error || '-';
  $('#detail-started').textContent = loopData.started_at ? timeAgo(new Date(loopData.started_at)) : '-';

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

  // Event list.
  renderEventList();
}

function renderContextMeter() {
  const container = $('#detail-context');
  const fill = $('#detail-context-fill');
  const pctEl = $('#detail-context-pct');

  if (!loopData || !loopData.context_window || !loopData.last_input_tokens) {
    container.hidden = true;
    return;
  }

  const pct = Math.min(100, (loopData.last_input_tokens / loopData.context_window) * 100);
  fill.style.width = pct.toFixed(1) + '%';
  fill.className = 'context-meter__fill'
    + (pct >= 80 ? ' context-meter__fill--crit' : pct >= 50 ? ' context-meter__fill--warn' : '');
  pctEl.textContent = Math.round(pct) + '%';
  container.hidden = false;
}

function updateSleepDisplay() {
  if (!loopData) return;
  const el = $('#detail-sleep');
  const timer = sleepTimers.get(nodeId);
  if (timer && timer.durationMs > 0 && loopData.state === 'sleeping') {
    const remaining = timer.durationMs - (Date.now() - timer.startedAt.getTime());
    if (remaining > 0) {
      const wakeAt = new Date(timer.startedAt.getTime() + timer.durationMs);
      el.textContent = 'until ' + formatTime(wakeAt);
    } else {
      el.textContent = 'waking up now...';
    }
  } else if (loopData.state === 'processing') {
    el.textContent = 'active';
  } else {
    el.textContent = '-';
  }
}

function renderSupervisorBar() {
  if (!loopData) return;
  const bar = $('#detail-supervisor');
  const cfg = loopData.config || {};

  if (!cfg.Supervisor) {
    bar.hidden = true;
    return;
  }

  bar.hidden = false;
  bar.innerHTML = '';
  const prob = cfg.SupervisorProb || 0;
  const pct = Math.round(prob * 100);

  if (loopData._supervisor) {
    bar.className = 'supervisor-bar supervisor-bar--active';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Supervisor iteration in progress';
  } else if (loopData._lastSupervisor) {
    bar.className = 'supervisor-bar supervisor-bar--last';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Last iteration was supervised';
  } else {
    bar.className = 'supervisor-bar';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Supervisor: ' + pct + '% chance next';
  }
}

function renderEventList() {
  const list = $('#event-list');
  list.innerHTML = '';

  for (const evt of events.slice(0, 20)) {
    const li = document.createElement('li');

    const time = document.createElement('span');
    time.className = 'event-time';
    time.textContent = formatTime(new Date(evt.ts));

    const kind = document.createElement('span');
    kind.className = 'event-kind';

    const detail = document.createElement('span');
    detail.className = 'event-detail';

    switch (evt.kind) {
      case 'loop_iteration_start':
        kind.textContent = evt.data.supervisor ? 'supervisor' : 'iteration';
        kind.className += evt.data.supervisor ? ' event-supervisor' : '';
        detail.textContent = '#' + (evt.data.attempt || '?');
        break;
      case 'loop_iteration_complete':
        kind.textContent = 'complete';
        kind.className += ' event-ok';
        detail.textContent = evt.data.model || '';
        break;
      case 'loop_sleep_start': {
        kind.textContent = 'sleep';
        const raw = evt.data.sleep_duration || '';
        const ms = parseDuration(raw);
        detail.textContent = ms > 0 ? formatDuration(ms) : raw;
        break;
      }
      case 'loop_wait_start':
        kind.textContent = 'waiting';
        detail.textContent = 'awaiting event';
        break;
      case 'loop_error':
        kind.textContent = 'error';
        kind.className += ' event-error';
        detail.textContent = evt.data.error || '';
        break;
      case 'loop_state_change':
        kind.textContent = evt.data.from + ' -> ' + evt.data.to;
        break;
      case 'loop_started':
        kind.textContent = 'started';
        kind.className += ' event-ok';
        break;
      case 'loop_stopped':
        kind.textContent = 'stopped';
        break;
      default:
        kind.textContent = evt.kind;
    }

    li.appendChild(time);
    li.appendChild(kind);
    li.appendChild(detail);
    list.appendChild(li);
  }
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
    updateSleepDisplay();
    if (loopData.started_at) {
      $('#detail-started').textContent = timeAgo(new Date(loopData.started_at));
    }
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
