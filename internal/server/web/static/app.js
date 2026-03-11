// Loop Visualizer — vanilla JS, no framework, no build step.
// Connects to the SSE event stream and renders loop nodes as SVG.

'use strict';

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

const state = {
  loops: new Map(),       // id -> loop status object
  selected: null,         // id of currently selected loop
  events: [],             // recent events (newest first, capped)
  sleepTimers: new Map(), // id -> { startedAt: Date, durationMs: number }
};

const MAX_EVENTS = 50;

// ---------------------------------------------------------------------------
// DOM References
// ---------------------------------------------------------------------------

const $ = (sel) => document.querySelector(sel);
const canvas = $('#canvas');
const connBadge = $('#conn-status');
const detailPlaceholder = $('#detail-placeholder');
const detailContent = $('#detail-content');
const emptyState = $('#empty-state');
const logEmpty = $('#log-empty');
const logScroll = $('#log-scroll');
const logBody = $('#log-body');

// ---------------------------------------------------------------------------
// SSE Connection
// ---------------------------------------------------------------------------

let eventSource = null;

function connect() {
  setConnState('connecting');
  eventSource = new EventSource('/api/loops/events');

  eventSource.addEventListener('snapshot', (e) => {
    const statuses = JSON.parse(e.data);
    state.loops.clear();
    for (const s of statuses) {
      state.loops.set(s.id, s);
    }
    renderAll();
    setConnState('connected');
  });

  eventSource.addEventListener('loop', (e) => {
    const evt = JSON.parse(e.data);
    handleLoopEvent(evt);
  });

  eventSource.onerror = () => {
    setConnState('disconnected');
    // EventSource auto-reconnects; the snapshot on reconnect
    // will restore full state.
  };

  eventSource.onopen = () => {
    setConnState('connected');
  };
}

function setConnState(s) {
  connBadge.textContent = s;
  connBadge.className = 'conn-badge conn-badge--' + s;
}

// ---------------------------------------------------------------------------
// Event Handling
// ---------------------------------------------------------------------------

function handleLoopEvent(evt) {
  const loopId = evt.data && evt.data.loop_id;
  const loopName = evt.data && evt.data.loop_name;

  // Push to event log.
  state.events.unshift(evt);
  if (state.events.length > MAX_EVENTS) state.events.length = MAX_EVENTS;

  switch (evt.kind) {
    case 'loop_started':
      // A new loop appeared. We don't have full status yet — fetch it.
      fetchLoops();
      break;

    case 'loop_stopped':
      if (loopId) {
        const existing = state.loops.get(loopId);
        if (existing) {
          existing.state = 'stopped';
          existing.iterations = evt.data.iterations || existing.iterations;
          existing.attempts = evt.data.attempts || existing.attempts;
        }
      }
      break;

    case 'loop_state_change':
      if (loopId && state.loops.has(loopId)) {
        state.loops.get(loopId).state = evt.data.to;
      }
      break;

    case 'loop_iteration_start':
      if (loopId && state.loops.has(loopId)) {
        const loop = state.loops.get(loopId);
        loop.state = 'processing';
        loop.last_wake_at = evt.ts;
        loop._supervisor = !!evt.data.supervisor;
        loop.attempts = evt.data.attempt || loop.attempts;
      }
      break;

    case 'loop_iteration_complete':
      if (loopId && state.loops.has(loopId)) {
        const loop = state.loops.get(loopId);
        loop._lastModel = evt.data.model;
        loop.total_input_tokens = (loop.total_input_tokens || 0) + (evt.data.input_tokens || 0);
        loop.total_output_tokens = (loop.total_output_tokens || 0) + (evt.data.output_tokens || 0);
        loop.iterations = (loop.iterations || 0) + 1;
        loop._supervisor = false;
        // Auto-refresh logs if this loop is selected.
        if (state.selected === loopId) {
          fetchLogs(loopId);
        }
      }
      break;

    case 'loop_sleep_start':
      if (loopId) {
        const durationStr = evt.data.sleep_duration || '';
        const durationMs = parseDuration(durationStr);
        state.sleepTimers.set(loopId, {
          startedAt: new Date(evt.ts),
          durationMs: durationMs,
        });
        if (state.loops.has(loopId)) {
          state.loops.get(loopId).state = 'sleeping';
        }
      }
      break;

    case 'loop_error':
      if (loopId && state.loops.has(loopId)) {
        const loop = state.loops.get(loopId);
        loop.state = 'error';
        loop.last_error = evt.data.error || '';
        loop.consecutive_errors = (loop.consecutive_errors || 0) + 1;
      }
      break;
  }

  renderAll();
}

// ---------------------------------------------------------------------------
// Data Fetching
// ---------------------------------------------------------------------------

async function fetchLoops() {
  try {
    const resp = await fetch('/api/loops');
    const statuses = await resp.json();
    state.loops.clear();
    for (const s of statuses) {
      state.loops.set(s.id, s);
    }
    renderAll();
  } catch (err) {
    console.warn('Failed to fetch loops:', err);
  }
}

async function fetchLogs(loopId) {
  if (!loopId) return;
  const level = $('#log-level').value;
  let url = '/api/loops/' + encodeURIComponent(loopId) + '/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);

  try {
    const resp = await fetch(url);
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Rendering — SVG Nodes
// ---------------------------------------------------------------------------

function renderAll() {
  renderNodes();
  renderDetail();
  renderEventList();
}

function renderNodes() {
  const loops = Array.from(state.loops.values());
  emptyState.hidden = loops.length > 0;

  // Get canvas dimensions for centering.
  const rect = canvas.getBoundingClientRect();
  const cx = rect.width / 2;
  const cy = rect.height / 2;

  // For now, lay out nodes in a circle (single node = centered).
  const count = loops.length;
  const radius = count <= 1 ? 0 : Math.min(rect.width, rect.height) * 0.3;

  for (let i = 0; i < count; i++) {
    const loop = loops[i];
    const angle = (2 * Math.PI * i) / count - Math.PI / 2;
    const x = cx + radius * Math.cos(angle);
    const y = cy + radius * Math.sin(angle);
    renderNode(loop, x, y);
  }

  // Remove nodes for loops that no longer exist.
  const existingGroups = canvas.querySelectorAll('.loop-node');
  for (const g of existingGroups) {
    if (!state.loops.has(g.dataset.loopId)) {
      g.remove();
    }
  }
}

function renderNode(loop, x, y) {
  const nodeR = 32;
  const ringR = 44;
  let group = canvas.querySelector(`[data-loop-id="${loop.id}"]`);

  if (!group) {
    group = createSVG('g', {
      class: 'loop-node',
      'data-loop-id': loop.id,
    });
    group.addEventListener('click', () => selectLoop(loop.id));

    // Glow ring.
    const ring = createSVG('circle', {
      class: 'node-ring',
      r: ringR,
      fill: 'none',
      stroke: 'var(--accent)',
      'stroke-width': 2,
    });

    // Sleep progress ring.
    const circumference = 2 * Math.PI * (nodeR + 4);
    const sleepRing = createSVG('circle', {
      class: 'sleep-ring',
      r: nodeR + 4,
      'stroke-dasharray': circumference,
      'stroke-dashoffset': circumference,
    });

    // Main circle.
    const circle = createSVG('circle', {
      class: 'node-circle',
      r: nodeR,
    });

    // Supervisor dot.
    const supDot = createSVG('circle', {
      class: 'supervisor-dot',
      r: 5,
      cy: -(nodeR + 10),
    });

    // Label.
    const label = createSVG('text', {
      class: 'node-label',
      y: nodeR + 18,
    });
    label.textContent = loop.name || loop.id.slice(0, 8);

    group.appendChild(ring);
    group.appendChild(sleepRing);
    group.appendChild(circle);
    group.appendChild(supDot);
    group.appendChild(label);
    canvas.appendChild(group);
  }

  // Update position.
  group.setAttribute('transform', `translate(${x},${y})`);

  // Update state class on main circle.
  const circle = group.querySelector('.node-circle');
  const stateClass = 'node-circle--' + (loop.state || 'pending');
  circle.setAttribute('class', 'node-circle ' + stateClass);

  // Supervisor dot.
  const supDot = group.querySelector('.supervisor-dot');
  supDot.setAttribute('class',
    'supervisor-dot' + (loop._supervisor ? ' supervisor-dot--active' : ''));

  // Selection ring.
  if (state.selected === loop.id) {
    group.classList.add('node-selected');
  } else {
    group.classList.remove('node-selected');
  }

  // Sleep progress ring.
  updateSleepRing(group, loop.id);
}

function updateSleepRing(group, loopId) {
  const sleepRing = group.querySelector('.sleep-ring');
  const timer = state.sleepTimers.get(loopId);
  const r = parseFloat(sleepRing.getAttribute('r'));
  const circumference = 2 * Math.PI * r;

  if (!timer || timer.durationMs <= 0) {
    sleepRing.setAttribute('stroke-dashoffset', circumference);
    return;
  }

  const elapsed = Date.now() - timer.startedAt.getTime();
  const progress = Math.min(1, elapsed / timer.durationMs);
  const offset = circumference * (1 - progress);
  sleepRing.setAttribute('stroke-dashoffset', offset);
}

// ---------------------------------------------------------------------------
// Rendering — Detail Panel
// ---------------------------------------------------------------------------

function renderDetail() {
  if (!state.selected || !state.loops.has(state.selected)) {
    detailPlaceholder.hidden = false;
    detailContent.hidden = true;
    return;
  }

  detailPlaceholder.hidden = true;
  detailContent.hidden = false;

  const loop = state.loops.get(state.selected);

  $('#detail-name').textContent = loop.name || loop.id;

  const badge = $('#detail-state');
  badge.textContent = loop.state || 'unknown';
  badge.className = 'state-badge state-badge--' + (loop.state || 'pending');

  $('#detail-iterations').textContent = formatNumber(loop.iterations || 0);
  $('#detail-attempts').textContent = formatNumber(loop.attempts || 0);
  $('#detail-input-tokens').textContent = formatTokens(loop.total_input_tokens || 0);
  $('#detail-output-tokens').textContent = formatTokens(loop.total_output_tokens || 0);
  $('#detail-model').textContent = loop._lastModel || '-';
  $('#detail-error').textContent = loop.last_error || '-';
  $('#detail-started').textContent = loop.started_at ? timeAgo(new Date(loop.started_at)) : '-';

  // Sleep countdown.
  const timer = state.sleepTimers.get(state.selected);
  if (timer && timer.durationMs > 0 && loop.state === 'sleeping') {
    const remaining = timer.durationMs - (Date.now() - timer.startedAt.getTime());
    if (remaining > 0) {
      $('#detail-sleep').textContent = formatDuration(remaining);
    } else {
      $('#detail-sleep').textContent = 'waking...';
    }
  } else {
    $('#detail-sleep').textContent = loop.state === 'processing' ? 'active' : '-';
  }
}

// ---------------------------------------------------------------------------
// Rendering — Event List
// ---------------------------------------------------------------------------

function renderEventList() {
  const list = $('#event-list');
  list.innerHTML = '';

  // Show events for selected loop, or all if none selected.
  const filtered = state.selected
    ? state.events.filter(e => e.data && e.data.loop_id === state.selected)
    : state.events;

  for (const evt of filtered.slice(0, 20)) {
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
      case 'loop_sleep_start':
        kind.textContent = 'sleep';
        detail.textContent = evt.data.sleep_duration || '';
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

// ---------------------------------------------------------------------------
// Rendering — Log Panel
// ---------------------------------------------------------------------------

function renderLogs(entries) {
  if (!entries || entries.length === 0) {
    logEmpty.hidden = false;
    logEmpty.querySelector('p').textContent = 'No log entries found';
    logScroll.hidden = true;
    return;
  }

  logEmpty.hidden = true;
  logScroll.hidden = false;

  // Check if already scrolled to bottom before updating content.
  const atBottom = logScroll.scrollHeight - logScroll.scrollTop - logScroll.clientHeight < 24;

  logBody.innerHTML = '';

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

    tr.appendChild(tdTime);
    tr.appendChild(tdLevel);
    tr.appendChild(tdSub);
    tr.appendChild(tdMsg);
    logBody.appendChild(tr);
  }

  // Auto-scroll to bottom if user was already at the bottom (live tail).
  if (atBottom) {
    logScroll.scrollTop = logScroll.scrollHeight;
  }
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

function selectLoop(loopId) {
  if (state.selected === loopId) {
    // Deselect.
    state.selected = null;
    logEmpty.hidden = false;
    logEmpty.querySelector('p').textContent = 'Click a loop node to load logs';
    logScroll.hidden = true;
  } else {
    state.selected = loopId;
    fetchLogs(loopId);
  }
  renderAll();
}

// ---------------------------------------------------------------------------
// Animation Loop (sleep countdowns + progress rings)
// ---------------------------------------------------------------------------

function tick() {
  // Update sleep countdown in detail panel.
  if (state.selected && state.loops.has(state.selected)) {
    const loop = state.loops.get(state.selected);
    const timer = state.sleepTimers.get(state.selected);
    if (timer && loop.state === 'sleeping') {
      const remaining = timer.durationMs - (Date.now() - timer.startedAt.getTime());
      const el = $('#detail-sleep');
      if (remaining > 0) {
        el.textContent = formatDuration(remaining);
      } else {
        el.textContent = 'waking...';
      }
    }
  }

  // Update sleep progress rings on all nodes.
  for (const [loopId] of state.sleepTimers) {
    const group = canvas.querySelector(`[data-loop-id="${loopId}"]`);
    if (group) updateSleepRing(group, loopId);
  }

  requestAnimationFrame(tick);
}

// ---------------------------------------------------------------------------
// Event Bindings
// ---------------------------------------------------------------------------

$('#log-level').addEventListener('change', () => {
  if (state.selected) fetchLogs(state.selected);
});

$('#log-refresh').addEventListener('click', () => {
  if (state.selected) fetchLogs(state.selected);
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createSVG(tag, attrs) {
  const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
  for (const [k, v] of Object.entries(attrs)) {
    el.setAttribute(k, v);
  }
  return el;
}

function formatNumber(n) {
  return n.toLocaleString();
}

function formatTokens(n) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  return String(n);
}

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

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

connect();
requestAnimationFrame(tick);
