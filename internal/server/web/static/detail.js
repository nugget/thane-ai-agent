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

// ---------------------------------------------------------------------------
// Live Telemetry Timers
// ---------------------------------------------------------------------------

function clearLiveTelemetry() {
  if (loopData) {
    loopData._iterStartTs = null;
    loopData._liveTools = [];
    loopData._liveModel = '';
    loopData._llmContext = null;
  }
}

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
      loopData._llmContext = null;
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
      loopData._liveTools.push({
        tool: d.tool,
        status: 'running',
        args: d.args || null,
      });
      break;
    case 'loop_tool_done':
      if (loopData._liveTools) {
        for (let i = loopData._liveTools.length - 1; i >= 0; i--) {
          if (loopData._liveTools[i].tool === d.tool && loopData._liveTools[i].status === 'running') {
            loopData._liveTools[i].status = d.error ? 'error' : 'done';
            loopData._liveTools[i].result = d.result || null;
            loopData._liveTools[i].error = d.error || null;
            break;
          }
        }
      }
      break;
    case 'loop_llm_start':
      loopData._liveModel = d.model || '';
      loopData._llmContext = {
        est_tokens: d.est_tokens || 0,
        messages: d.messages || 0,
        tools: d.tools || 0,
        iteration: d.iteration,
        complexity: d.complexity || '',
        intent: d.intent || '',
        reasoning: d.reasoning || '',
      };
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
    container.appendChild(buildLiveCard(loopData));
  }

  // Live connector.
  if ((isSleeping || isWaiting) && iterationHistory.length > 0) {
    container.appendChild(buildConnector(loopData, iterationHistory[0], true, sleepTimers.get(nodeId)));
  }

  // Past iteration cards.
  for (let i = 0; i < iterationHistory.length; i++) {
    container.appendChild(buildPastCard(iterationHistory[i], loopData.handler_only, i, expanded.has(String(i))));
    if (i < iterationHistory.length - 1) {
      container.appendChild(buildConnector(loopData, iterationHistory[i], false, null));
    }
  }
}

// buildLiveCard, buildPastCard, buildConnector, shortModelName,
// buildToolCounts, escapeHTML, truncate, makeIDRow, makeIDChip
// are all in shared.js.

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
