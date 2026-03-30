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

// Utility functions (formatTokens, formatDuration, formatTime, etc.),
// card builders, and log rendering are in shared.js.

// clearLiveTelemetry, renderAggregates, renderTimeline,
// applyLoopEventToLoop, MAX_ITERATION_HISTORY are in shared.js.

// renderLogRows and buildLogDetail are in shared.js.
function renderLogs(entries) {
  renderLogRows(entries, { logEmpty, logScroll, logBody });
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
        // Restore LLM context from snapshot so late-connecting clients
        // see enrichment data immediately.
        match._llmContext = match.llm_context || null;
        if (match._llmContext && match._llmContext.model) {
          match._liveModel = match._llmContext.model;
        }
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

  const result = applyLoopEventToLoop(evt, {
    loop: loopData,
    loopId: nodeId,
    sleepTimers: sleepTimers,
    history: iterationHistory,
  });

  if (result && result.snapshot) {
    iterationHistory.unshift(result.snapshot);
    if (iterationHistory.length > MAX_ITERATION_HISTORY) {
      iterationHistory.length = MAX_ITERATION_HISTORY;
    }
  }

  // Capability tools change active_tags — refetch to update chips.
  if (result && result.capabilityChanged) {
    refreshActiveTags();
  }
}

// refreshActiveTags fetches current loop status and updates
// the active_tags field so capability chips reflect changes
// from activate_capability / deactivate_capability tool calls.
async function refreshActiveTags() {
  try {
    const resp = await fetch('/api/loops');
    if (!resp.ok) return;
    const statuses = await resp.json();
    const match = statuses.find(s => s.id === nodeId);
    if (match && loopData) {
      loopData.active_tags = match.active_tags || null;
      renderLoopDetail();
    }
  } catch (_) { /* best-effort */ }
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
  renderAggregates(loopData, $('#detail-aggregates'));

  // Iteration timeline.
  renderTimeline(loopData, $('#detail-timeline'), iterationHistory, nodeId, sleepTimers);

  // Capabilities: show configured tags (muted if inactive) and
  // dynamically activated tags (dashed border if not in config).
  const configTags = (loopData.config && loopData.config.Tags) || [];
  const activeTags = new Set(loopData.active_tags || []);
  const allTags = new Set([...configTags, ...activeTags]);
  const tagsSection = $('#detail-tags');
  const tagsList = $('#detail-tags-list');
  if (allTags.size > 0) {
    tagsSection.hidden = false;
    tagsList.innerHTML = '';
    for (const tag of [...allTags].sort()) {
      const chip = document.createElement('span');
      const inConfig = configTags.includes(tag);
      const isActive = activeTags.has(tag);
      chip.className = 'tag-chip'
        + (isActive && inConfig ? ' tag-chip--active' : '')
        + (!isActive && inConfig ? ' tag-chip--muted' : '')
        + (isActive && !inConfig ? ' tag-chip--dynamic' : '');
      chip.textContent = tag;
      tagsList.appendChild(chip);
    }
  } else {
    tagsSection.hidden = true;
  }
}

// buildLiveCard, buildPastCard, buildConnector, renderAggregates,
// renderTimeline, clearLiveTelemetry, applyLoopEventToLoop,
// shortModelName, buildToolCounts, escapeHTML, truncate, makeIDRow,
// makeIDChip, MAX_ITERATION_HISTORY are all in shared.js.

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
