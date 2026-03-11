// Cognition Engine — vanilla JS, no framework, no build step.
// Connects to the SSE event stream and renders loop nodes as SVG.

'use strict';

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

const state = {
  loops: new Map(),       // id -> loop status object
  selected: null,         // id of currently selected loop ('__system__' for system node)
  events: [],             // recent events (newest first, capped)
  sleepTimers: new Map(), // id -> { startedAt: Date, durationMs: number }
  system: null,           // system status object from /api/system
  prevIterations: new Map(), // id -> last known iteration count (for flash detection)
  prevErrors: new Map(),     // id -> last known error string (for shake detection)
  knownLoopIds: new Set(),   // ids we've rendered before (for enter animation)
};

const MAX_EVENTS = 50;

// ---------------------------------------------------------------------------
// DOM References
// ---------------------------------------------------------------------------

const $ = (sel) => document.querySelector(sel);
const canvas = $('#canvas');
const canvasWorld = $('#canvas-world');
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
    fetchVersionInfo(); // re-sync uptime on reconnect
  };
}

let connState = 'connecting';

function setConnState(s) {
  connState = s;
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
        loop._currentConvID = evt.data.conversation_id || null;
      }
      break;

    case 'loop_iteration_complete':
      if (loopId && state.loops.has(loopId)) {
        const loop = state.loops.get(loopId);
        loop._lastModel = evt.data.model;
        loop._lastSupervisor = loop._supervisor || false;
        loop.total_input_tokens = (loop.total_input_tokens || 0) + (evt.data.input_tokens || 0);
        loop.total_output_tokens = (loop.total_output_tokens || 0) + (evt.data.output_tokens || 0);
        loop.last_input_tokens = evt.data.input_tokens || 0;
        if (evt.data.context_window > 0) {
          loop.context_window = evt.data.context_window;
        }
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

let systemStartTime = null; // derived from system uptime for local ticking

async function fetchSystemStatus() {
  try {
    const resp = await fetch('/api/system');
    if (resp.status === 404) {
      state.system = null;
      return;
    }
    state.system = await resp.json();
    // Derive start time so we can tick uptime locally.
    if (state.system.uptime) {
      const uptimeMs = parseDuration(state.system.uptime);
      systemStartTime = Date.now() - uptimeMs;
    }
    renderAll();
  } catch (err) {
    console.warn('Failed to fetch system status:', err);
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
  const hasSystem = state.system !== null;
  emptyState.hidden = loops.length > 0 || hasSystem;

  // Get canvas dimensions for centering.
  const rect = canvas.getBoundingClientRect();
  const cx = rect.width / 2;
  const cy = rect.height / 2;

  // For now, lay out nodes in a circle (single node = centered).
  const count = loops.length;
  const radius = count <= 1 ? 0 : Math.min(rect.width, rect.height) * 0.3;

  // Track current loop ids for exit detection.
  const currentIds = new Set();

  // Positions map for linking line.
  const nodePositions = new Map();

  for (let i = 0; i < count; i++) {
    const loop = loops[i];
    currentIds.add(loop.id);
    const angle = (2 * Math.PI * i) / count - Math.PI / 2;
    const x = cx + radius * Math.cos(angle);
    const y = cy + radius * Math.sin(angle);
    nodePositions.set(loop.id, { x, y });
    renderNode(loop, x, y);
  }

  // Remove nodes for loops that no longer exist (with exit animation).
  const existingGroups = canvasWorld.querySelectorAll('.loop-node');
  for (const g of existingGroups) {
    const id = g.dataset.loopId;
    if (!state.loops.has(id)) {
      if (!g.classList.contains('loop-node--exiting')) {
        g.classList.add('loop-node--exiting');
        g.addEventListener('animationend', () => g.remove(), { once: true });
        state.knownLoopIds.delete(id);
        state.prevIterations.delete(id);
        state.prevErrors.delete(id);
      }
    }
  }

  // System node — positioned offset from center.
  const sysX = cx - radius - 100;
  const sysY = cy;
  if (hasSystem) {
    renderSystemNode(sysX, sysY);
  } else {
    const existing = canvasWorld.querySelector('.system-node');
    if (existing) existing.remove();
  }

  // Linking line: runtime → metacognitive.
  renderLinkingLine(hasSystem, sysX, sysY, loops, nodePositions);
}

function renderLinkingLine(hasSystem, sysX, sysY, loops, nodePositions) {
  let line = canvasWorld.querySelector('.link-line');

  // Find the metacognitive loop.
  const metaLoop = loops.find(l => l.name === 'metacognitive');

  if (!hasSystem || !metaLoop || !nodePositions.has(metaLoop.id)) {
    // No connection to draw — remove existing line.
    if (line) line.remove();
    return;
  }

  const pos = nodePositions.get(metaLoop.id);

  if (!line) {
    line = createSVG('line', { class: 'link-line' });
    // Insert before nodes so the line draws behind them.
    canvasWorld.insertBefore(line, canvasWorld.firstChild);
  }

  line.setAttribute('x1', sysX);
  line.setAttribute('y1', sysY);
  line.setAttribute('x2', pos.x);
  line.setAttribute('y2', pos.y);

  // State-driven styling: error state turns the line red.
  if (metaLoop.state === 'error') {
    line.setAttribute('class', 'link-line link-line--error');
  } else {
    line.setAttribute('class', 'link-line');
  }
}

// Flash the linking line briefly (called on supervisor events).
function flashLinkingLine() {
  const line = canvasWorld.querySelector('.link-line');
  if (!line) return;
  const baseClass = line.getAttribute('class').replace(' link-line--flash', '');
  line.setAttribute('class', baseClass + ' link-line--flash');
  setTimeout(() => {
    line.setAttribute('class', baseClass);
  }, 300);
}

function renderNode(loop, x, y) {
  const nodeR = 32;
  const ringR = 44;
  let group = canvasWorld.querySelector(`[data-loop-id="${loop.id}"]`);

  if (!group) {
    group = createSVG('g', {
      class: 'loop-node',
      'data-loop-id': loop.id,
    });
    group.addEventListener('click', () => selectLoop(loop.id));
    group.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      showContextMenu(e.clientX, e.clientY, [
        { label: 'Open in window', action: () => openDetailWindow('loop', loop.id) },
        { separator: true },
        { label: 'Copy loop ID', action: () => navigator.clipboard.writeText(loop.id) },
      ]);
    });

    // Inner group for enter/exit scale animation (children drawn at origin).
    const inner = createSVG('g', { class: 'node-inner' });

    // Native SVG tooltip — instant, no delay.
    const title = createSVG('title', {});
    title.textContent = loop.name || loop.id;
    inner.appendChild(title);

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

    // Supervisor ring (larger ring outside the node).
    const supDot = createSVG('circle', {
      class: 'supervisor-dot',
      r: nodeR + 10,
    });

    // Label.
    const label = createSVG('text', {
      class: 'node-label',
      y: nodeR + 18,
    });
    label.textContent = loop.name || loop.id.slice(0, 8);

    inner.appendChild(ring);
    inner.appendChild(sleepRing);
    inner.appendChild(circle);
    inner.appendChild(supDot);
    inner.appendChild(label);
    group.appendChild(inner);
    canvasWorld.appendChild(group);

    // Enter animation for genuinely new loops.
    const isNew = !state.knownLoopIds.has(loop.id);
    if (isNew) {
      inner.classList.add('node-inner--entering');
      inner.addEventListener('animationend', () => {
        inner.classList.remove('node-inner--entering');
      }, { once: true });
    }
    state.knownLoopIds.add(loop.id);

    // Enable smooth reflow after first paint.
    requestAnimationFrame(() => group.classList.add('loop-node--settled'));
  }

  // Update position (smooth reflow via CSS transition on --settled class).
  group.setAttribute('transform', `translate(${x},${y})`);

  // Update state class on main circle — supervisor processing gets its own style.
  const circle = group.querySelector('.node-circle');
  const isSup = loop._supervisor && loop.state === 'processing';
  const stateClass = isSup
    ? 'node-circle--supervisor'
    : 'node-circle--' + (loop.state || 'pending');
  circle.setAttribute('class', 'node-circle ' + stateClass);

  // Stroke width represents context utilization percentage.
  const ctxPct = (loop.context_window > 0 && loop.last_input_tokens > 0)
    ? Math.min(1, loop.last_input_tokens / loop.context_window)
    : 0;
  const minStroke = 2;
  const maxStroke = 10;
  const strokeW = ctxPct > 0
    ? minStroke + ctxPct * (maxStroke - minStroke)
    : minStroke;
  circle.setAttribute('stroke-width', strokeW.toFixed(1));

  // Supervisor ring (outer pulsing ring around node).
  const supDot = group.querySelector('.supervisor-dot');
  supDot.setAttribute('class',
    'supervisor-dot' + (isSup ? ' supervisor-dot--active' : ''));
  // Also show dimmed ring when last iteration was supervisor (memory).
  if (!isSup && loop._lastSupervisor) {
    supDot.setAttribute('class', 'supervisor-dot supervisor-dot--faded');
  }

  // Selection ring.
  if (state.selected === loop.id) {
    group.classList.add('node-selected');
  } else {
    group.classList.remove('node-selected');
  }

  // Sleep progress ring.
  updateSleepRing(group, loop.id);

  // Iteration flash — ring brightens when iteration count changes.
  const prevIter = state.prevIterations.get(loop.id) || 0;
  const curIter = loop.iterations || 0;
  if (curIter > prevIter && prevIter > 0) {
    const ring = group.querySelector('.node-ring');
    ring.classList.remove('node-ring--flash');
    // Force reflow to restart animation.
    void ring.offsetWidth;
    ring.classList.add('node-ring--flash');

    // Flash the linking line if this is the metacognitive loop and a supervisor fired.
    if (loop.name === 'metacognitive' && loop._lastSupervisor) {
      flashLinkingLine();
    }
  }
  state.prevIterations.set(loop.id, curIter);

  // Error shake — node jitters when a new error appears.
  const prevError = state.prevErrors.get(loop.id) || '';
  const curError = loop.last_error || '';
  if (curError && curError !== prevError) {
    group.classList.remove('loop-node--shake');
    void group.offsetWidth;
    group.classList.add('loop-node--shake');
    group.addEventListener('animationend', () => {
      group.classList.remove('loop-node--shake');
    }, { once: true });
  }
  state.prevErrors.set(loop.id, curError);
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

function renderSystemNode(x, y) {
  const sys = state.system;
  const w = 72, h = 48, r = 8;
  let group = canvasWorld.querySelector('.system-node');

  if (!group) {
    group = createSVG('g', { class: 'system-node' });
    group.addEventListener('click', () => selectSystem());
    group.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      showContextMenu(e.clientX, e.clientY, [
        { label: 'Open in window', action: () => openDetailWindow('system') },
      ]);
    });

    const title = createSVG('title', {});
    title.textContent = 'Runtime';
    group.appendChild(title);

    // Rounded rect (as path for rounded corners).
    const rect = createSVG('rect', {
      class: 'system-rect',
      x: -w / 2, y: -h / 2,
      width: w, height: h,
      rx: r, ry: r,
    });
    group.appendChild(rect);

    const label = createSVG('text', {
      class: 'node-label',
      y: h / 2 + 16,
    });
    label.textContent = 'runtime';
    group.appendChild(label);

    canvasWorld.appendChild(group);
    // Defer adding settled class so initial render isn't animated.
    requestAnimationFrame(() => group.classList.add('system-node--settled'));
  }

  group.setAttribute('transform', `translate(${x},${y})`);

  // Update health-based fill.
  const rect = group.querySelector('.system-rect');
  const cls = sys.status === 'healthy'
    ? 'system-rect system-rect--healthy'
    : 'system-rect system-rect--degraded';
  rect.setAttribute('class', cls);

  // Selection highlight.
  if (state.selected === '__system__') {
    group.classList.add('node-selected');
  } else {
    group.classList.remove('node-selected');
  }
}

function renderSystemDetail() {
  const sys = state.system;
  if (!sys) return;

  // Status badge.
  const badge = $('#system-status');
  badge.textContent = sys.status || 'unknown';
  badge.className = 'state-badge state-badge--' +
    (sys.status === 'healthy' ? 'sleeping' : 'error');

  // Services list.
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

  // Metrics — uptime is ticked live in tick(), seed it here.
  updateSystemUptime();
  const ver = sys.version || {};
  $('#system-version').textContent = ver.version || '-';
  $('#system-commit').textContent = ver.git_commit ? ver.git_commit.slice(0, 7) : '-';
  $('#system-go').textContent = ver.go_version || '-';
  $('#system-arch').textContent = (ver.os || '') + '/' + (ver.arch || '') || '-';
}

function updateSystemUptime() {
  if (systemStartTime === null) {
    $('#system-uptime').textContent = state.system ? (state.system.uptime || '-') : '-';
    return;
  }
  const ms = Date.now() - systemStartTime;
  $('#system-uptime').textContent = formatUptimeLong(ms);
}

// ---------------------------------------------------------------------------
// Rendering — Detail Panel
// ---------------------------------------------------------------------------

const systemDetail = $('#system-detail');

function renderDetail() {
  const isSystem = state.selected === '__system__';
  const isLoop = state.selected && state.loops.has(state.selected);

  if (isSystem && state.system) {
    detailPlaceholder.hidden = true;
    detailContent.hidden = true;
    systemDetail.hidden = false;
    renderSystemDetail();
    return;
  }

  systemDetail.hidden = true;

  if (!isLoop) {
    detailPlaceholder.hidden = false;
    detailContent.hidden = true;
    return;
  }

  detailPlaceholder.hidden = true;
  detailContent.hidden = false;

  const loop = state.loops.get(state.selected);

  $('#detail-name').textContent = loop.name || loop.id;

  const badge = $('#detail-state');
  const isSup = loop._supervisor && loop.state === 'processing';
  badge.textContent = isSup ? 'supervisor' : (loop.state || 'unknown');
  badge.className = 'state-badge state-badge--' + (isSup ? 'supervisor' : (loop.state || 'pending'));

  // IDs section.
  renderDetailIDs(loop);

  // Forward-looking section: visible when sleeping or has supervisor config.
  const isSleeping = loop.state === 'sleeping';
  const hasSupervisor = loop.config && loop.config.Supervisor;
  const showForward = isSleeping || hasSupervisor;
  $('#detail-forward').hidden = !showForward;
  $('#detail-divider').hidden = !showForward;

  // Supervisor status bar.
  renderSupervisorBar(loop);

  // Sleep countdown — hide the row entirely when not sleeping.
  const sleepVisible = loop.state === 'sleeping';
  $('#detail-sleep-label').hidden = !sleepVisible;
  $('#detail-sleep').hidden = !sleepVisible;
  updateSleepDisplay(loop);

  // Historical metrics.
  $('#detail-iterations').textContent = formatNumber(loop.iterations || 0);
  $('#detail-attempts').textContent = formatNumber(loop.attempts || 0);
  $('#detail-input-tokens').textContent = formatTokens(loop.total_input_tokens || 0);
  $('#detail-output-tokens').textContent = formatTokens(loop.total_output_tokens || 0);
  $('#detail-model').textContent = loop._lastModel || '-';
  $('#detail-error').textContent = loop.last_error || '-';
  $('#detail-started').textContent = loop.started_at ? timeAgo(new Date(loop.started_at)) : '-';
}

function updateSleepDisplay(loop) {
  const el = $('#detail-sleep');
  const timer = state.sleepTimers.get(loop.id);
  if (timer && timer.durationMs > 0 && loop.state === 'sleeping') {
    const remaining = timer.durationMs - (Date.now() - timer.startedAt.getTime());
    if (remaining > 0) {
      const wakeAt = new Date(timer.startedAt.getTime() + timer.durationMs);
      const wakeTime = formatTime(wakeAt);
      el.innerHTML = 'until ' + wakeTime + '<br>' + formatFuzzy(remaining);
    } else {
      el.textContent = 'waking up now...';
    }
  } else if (loop.state === 'processing') {
    el.textContent = 'active';
  } else {
    el.textContent = '-';
  }
}

function formatFuzzy(ms) {
  const sec = Math.round(ms / 1000);
  if (sec < 5) return 'moments';
  if (sec < 60) return 'about ' + sec + 's';
  const min = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (min < 2) return 'about a minute';
  if (remSec < 15) return min + ' min';
  return min + ' min ' + remSec + 's';
}

function renderDetailIDs(loop) {
  const container = $('#detail-ids');
  container.innerHTML = '';

  // Loop ID.
  if (loop.id) {
    container.appendChild(makeIDRow('loop_id', loop.id));
  }

  // Parent ID.
  if (loop.parent_id) {
    container.appendChild(makeIDRow('parent_id', loop.parent_id));
  }

  // Active conversation ID (from current iteration).
  if (loop._currentConvID) {
    container.appendChild(makeIDRow('conv_id', loop._currentConvID));
  }

  // Recent conversation IDs.
  const convs = loop.recent_conv_ids;
  if (convs && convs.length > 0) {
    const row = document.createElement('div');
    row.className = 'id-row';

    const label = document.createElement('span');
    label.className = 'id-label';
    label.textContent = 'recent';
    row.appendChild(label);

    const chips = document.createElement('span');
    chips.className = 'id-convs';
    for (const cid of convs) {
      chips.appendChild(makeIDChip(cid));
    }
    row.appendChild(chips);
    container.appendChild(row);
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

function renderSupervisorBar(loop) {
  const bar = $('#detail-supervisor');
  const cfg = loop.config || {};

  // Only show if supervisor mode is configured.
  if (!cfg.Supervisor) {
    bar.hidden = true;
    return;
  }

  bar.hidden = false;
  bar.innerHTML = '';

  const prob = cfg.SupervisorProb || 0;
  const pct = Math.round(prob * 100);

  if (loop._supervisor) {
    // Currently in a supervisor iteration.
    bar.className = 'supervisor-bar supervisor-bar--active';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Supervisor iteration in progress';
  } else if (loop._lastSupervisor) {
    // Last iteration was a supervisor.
    bar.className = 'supervisor-bar supervisor-bar--last';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Last iteration was supervised';
  } else {
    // Idle — show probability of next being a supervisor.
    bar.className = 'supervisor-bar';
    bar.innerHTML = '<span class="sup-icon">&#x2726;</span> Supervisor: ' + pct + '% chance next';
  }
}

function shortID(id) {
  if (!id) return '';
  // UUID-like: show first 8 chars.
  if (id.length > 12) return id.slice(0, 8);
  return id;
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
      case 'loop_sleep_start': {
        kind.textContent = 'sleep';
        const raw = evt.data.sleep_duration || '';
        const ms = parseDuration(raw);
        detail.textContent = ms > 0 ? formatDuration(ms) : raw;
        break;
      }
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

  // Auto-scroll to bottom if user was already at the bottom (live tail).
  if (atBottom) {
    logScroll.scrollTop = logScroll.scrollHeight;
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

  // Copy-clickable ID chips for tracing IDs.
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
    chip.title = label + ' — click to copy\n' + full;
    chip.addEventListener('click', (e) => {
      e.stopPropagation();
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

function selectSystem() {
  if (state.selected === '__system__') {
    state.selected = null;
    logEmpty.hidden = false;
    logEmpty.querySelector('p').textContent = 'Click a loop node to load logs';
    logScroll.hidden = true;
  } else {
    state.selected = '__system__';
    fetchSystemLogs();
  }
  renderAll();
}

async function fetchSystemLogs() {
  const level = $('#log-level').value;
  let url = '/api/system/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);

  try {
    const resp = await fetch(url);
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch system logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Animation Loop (sleep countdowns + progress rings)
// ---------------------------------------------------------------------------

function tick() {
  // Update live detail fields for selected loop.
  if (state.selected && state.loops.has(state.selected)) {
    const loop = state.loops.get(state.selected);
    updateSleepDisplay(loop);
    // Keep "Started" field fresh.
    if (loop.started_at) {
      $('#detail-started').textContent = timeAgo(new Date(loop.started_at));
    }
  }

  // Tick system uptime if system detail is visible.
  if (state.selected === '__system__' && state.system) {
    updateSystemUptime();
  }

  // Update sleep progress rings on all nodes.
  for (const [loopId] of state.sleepTimers) {
    const group = canvasWorld.querySelector(`[data-loop-id="${loopId}"]`);
    if (group) updateSleepRing(group, loopId);
  }

  requestAnimationFrame(tick);
}

// ---------------------------------------------------------------------------
// Event Bindings
// ---------------------------------------------------------------------------

function refreshLogs() {
  if (state.selected === '__system__') {
    fetchSystemLogs();
  } else if (state.selected) {
    fetchLogs(state.selected);
  }
}

$('#log-level').addEventListener('change', refreshLogs);
$('#log-refresh').addEventListener('click', refreshLogs);

// ---------------------------------------------------------------------------
// Panel Toggle
// ---------------------------------------------------------------------------

function toggleInspector() {
  const panel = document.getElementById('detail-panel');
  const handle = document.getElementById('resize-v');
  const btn = document.getElementById('toggle-inspector');
  const visible = !panel.hidden;
  panel.hidden = visible;
  handle.hidden = visible;
  btn.classList.toggle('toggle-btn--active', !visible);
}

function toggleLogs() {
  const panel = document.getElementById('log-panel');
  const handle = document.getElementById('resize-h');
  const btn = document.getElementById('toggle-logs');
  const visible = !panel.hidden;
  panel.hidden = visible;
  handle.hidden = visible;
  btn.classList.toggle('toggle-btn--active', !visible);
}

$('#toggle-inspector').addEventListener('click', toggleInspector);
$('#toggle-logs').addEventListener('click', toggleLogs);

// ---------------------------------------------------------------------------
// Context Menu
// ---------------------------------------------------------------------------

const contextMenu = document.getElementById('context-menu');
const contextMenuItems = document.getElementById('context-menu-items');

function showContextMenu(clientX, clientY, items) {
  contextMenuItems.innerHTML = '';
  for (const item of items) {
    if (item.separator) {
      const sep = document.createElement('li');
      sep.className = 'context-menu-sep';
      contextMenuItems.appendChild(sep);
      continue;
    }
    const li = document.createElement('li');
    li.textContent = item.label;
    li.addEventListener('click', () => {
      hideContextMenu();
      item.action();
    });
    contextMenuItems.appendChild(li);
  }

  contextMenu.hidden = false;

  // Position, clamping to viewport.
  const menuRect = contextMenu.getBoundingClientRect();
  const x = Math.min(clientX, window.innerWidth - menuRect.width - 4);
  const y = Math.min(clientY, window.innerHeight - menuRect.height - 4);
  contextMenu.style.left = Math.max(0, x) + 'px';
  contextMenu.style.top = Math.max(0, y) + 'px';
}

function hideContextMenu() {
  contextMenu.hidden = true;
}

document.addEventListener('click', (e) => {
  if (!contextMenu.hidden && !contextMenu.contains(e.target)) {
    hideContextMenu();
  }
});

document.addEventListener('scroll', hideContextMenu, true);

// ---------------------------------------------------------------------------
// Popup Detail Window
// ---------------------------------------------------------------------------

function openDetailWindow(type, id) {
  const params = type === 'system'
    ? '?type=system'
    : '?type=loop&id=' + encodeURIComponent(id);
  const name = type === 'system'
    ? 'Runtime'
    : (state.loops.get(id)?.name || id?.slice(0, 8) || 'Loop');
  const w = window.open(
    '/static/detail.html' + params + '&name=' + encodeURIComponent(name),
    'detail-' + (id || 'system'),
    'popup=yes,width=500,height=700'
  );
  // Set title once loaded (cross-origin safe since same origin).
  if (w) {
    w.addEventListener('load', () => {
      w.document.title = 'Thane \u00b7 ' + name;
    });
  }
}

// ---------------------------------------------------------------------------
// Keyboard Shortcuts
// ---------------------------------------------------------------------------

document.addEventListener('keydown', (e) => {
  // Skip when typing in form elements.
  const tag = e.target.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

  switch (e.key.toLowerCase()) {
    case 'i':
      toggleInspector();
      break;
    case 'l':
      toggleLogs();
      break;
    case 'escape':
      hideContextMenu();
      break;
  }
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
// Footer — version & uptime
// ---------------------------------------------------------------------------

let serverStartTime = null; // derived from uptime snapshot

async function fetchVersionInfo() {
  try {
    const resp = await fetch('/v1/version');
    const info = await resp.json();

    const ver = info.version || 'dev';
    const commit = (info.git_commit || 'unknown').slice(0, 7);
    $('#footer-version').textContent = ver + ' (' + commit + ')';
    $('#footer-arch').textContent = (info.os || '') + '/' + (info.arch || '');
    $('#footer-go').textContent = info.go_version || '';

    // Derive server start time from uptime string so we can tick locally.
    if (info.uptime) {
      const uptimeMs = parseDuration(info.uptime);
      serverStartTime = Date.now() - uptimeMs;
    }

    updateUptime();
  } catch (err) {
    console.warn('Failed to fetch version info:', err);
  }
}

function updateUptime() {
  if (serverStartTime === null) return;
  if (connState !== 'connected') return;
  const ms = Date.now() - serverStartTime;
  $('#footer-uptime').textContent = 'up ' + formatUptimeLong(ms);
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
// Resizable Panes
// ---------------------------------------------------------------------------

(function initResize() {
  const resizeV = document.getElementById('resize-v');
  const resizeH = document.getElementById('resize-h');
  const detailPanel = document.getElementById('detail-panel');
  const logPanel = document.getElementById('log-panel');
  const mainEl = document.querySelector('.main');

  // Vertical handle: resize detail panel width.
  let dragging = null;

  resizeV.addEventListener('mousedown', (e) => {
    e.preventDefault();
    dragging = 'v';
    resizeV.classList.add('resize-handle--active');
    document.body.classList.add('resize-col');
  });

  resizeH.addEventListener('mousedown', (e) => {
    e.preventDefault();
    dragging = 'h';
    resizeH.classList.add('resize-handle--active');
    document.body.classList.add('resize-row');
  });

  document.addEventListener('mousemove', (e) => {
    if (!dragging) return;
    e.preventDefault();

    if (dragging === 'v') {
      // Detail panel is on the right — width = distance from mouse to right edge of main.
      const mainRect = mainEl.getBoundingClientRect();
      const newWidth = mainRect.right - e.clientX;
      const clamped = Math.max(200, Math.min(newWidth, mainRect.width - 200));
      detailPanel.style.width = clamped + 'px';
    } else if (dragging === 'h') {
      // Log panel is at the bottom — height = distance from mouse to bottom of body
      // minus footer height.
      const footer = document.getElementById('footer');
      const footerH = footer ? footer.offsetHeight : 0;
      const bodyH = document.body.offsetHeight;
      const newHeight = bodyH - e.clientY - footerH;
      const clamped = Math.max(80, Math.min(newHeight, bodyH - 200));
      logPanel.style.height = clamped + 'px';
      // Keep logs anchored to bottom during resize.
      const ls = document.getElementById('log-scroll');
      if (ls) ls.scrollTop = ls.scrollHeight;
    }
  });

  document.addEventListener('mouseup', () => {
    if (!dragging) return;
    resizeV.classList.remove('resize-handle--active');
    resizeH.classList.remove('resize-handle--active');
    document.body.classList.remove('resize-col', 'resize-row');
    dragging = null;
  });
})();

// ---------------------------------------------------------------------------
// Canvas Pan & Zoom
// ---------------------------------------------------------------------------

const viewport = { panX: 0, panY: 0, zoom: 1 };
const ZOOM_MIN = 0.25;
const ZOOM_MAX = 4;
const ZOOM_STEP = 0.1;

function applyViewportTransform() {
  canvasWorld.setAttribute(
    'transform',
    `translate(${viewport.panX},${viewport.panY}) scale(${viewport.zoom})`
  );
}

(function initPanZoom() {
  let isPanning = false;
  let startX = 0;
  let startY = 0;
  let startPanX = 0;
  let startPanY = 0;

  canvas.addEventListener('mousedown', (e) => {
    // Only pan on direct canvas/background clicks, not on nodes.
    if (e.target !== canvas && !e.target.closest('#canvas-world') === null) return;
    if (e.target.closest('.loop-node')) return;
    if (e.button !== 0) return;

    isPanning = true;
    startX = e.clientX;
    startY = e.clientY;
    startPanX = viewport.panX;
    startPanY = viewport.panY;
    canvas.style.cursor = 'grabbing';
    e.preventDefault();
  });

  document.addEventListener('mousemove', (e) => {
    if (!isPanning) return;
    viewport.panX = startPanX + (e.clientX - startX);
    viewport.panY = startPanY + (e.clientY - startY);
    applyViewportTransform();
  });

  document.addEventListener('mouseup', () => {
    if (!isPanning) return;
    isPanning = false;
    canvas.style.cursor = '';
  });

  canvas.addEventListener('wheel', (e) => {
    e.preventDefault();

    // Zoom toward cursor position.
    const rect = canvas.getBoundingClientRect();
    const mouseX = e.clientX - rect.left;
    const mouseY = e.clientY - rect.top;

    // World coordinates under cursor before zoom.
    const wx = (mouseX - viewport.panX) / viewport.zoom;
    const wy = (mouseY - viewport.panY) / viewport.zoom;

    // Apply zoom delta.
    const delta = e.deltaY > 0 ? -ZOOM_STEP : ZOOM_STEP;
    const newZoom = Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, viewport.zoom + delta));
    viewport.zoom = newZoom;

    // Adjust pan so the world point under cursor stays fixed.
    viewport.panX = mouseX - wx * viewport.zoom;
    viewport.panY = mouseY - wy * viewport.zoom;

    applyViewportTransform();
  }, { passive: false });

  // Double-click to reset view.
  canvas.addEventListener('dblclick', (e) => {
    if (e.target.closest('.loop-node')) return;
    viewport.panX = 0;
    viewport.panY = 0;
    viewport.zoom = 1;
    applyViewportTransform();
  });
})();

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

connect();
fetchVersionInfo();
fetchSystemStatus();
// Refresh uptime display every second.
setInterval(updateUptime, 1000);
// Refresh system status every 10s.
setInterval(fetchSystemStatus, 10000);
requestAnimationFrame(tick);
