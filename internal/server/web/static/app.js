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
  sleepTimers: new Map(), // id -> { startedAt: number (ms timestamp), durationMs: number }
  iterationHistory: new Map(), // id -> array of iteration snapshots (newest first)
  system: null,           // system status object from /api/system
  prevIterations: new Map(), // id -> last known iteration count (for flash detection)
  prevErrors: new Map(),     // id -> last known error string (for shake detection)
  knownLoopIds: new Set(),   // ids we've rendered before (for enter animation)
};

const MAX_EVENTS = 50;

// ---------------------------------------------------------------------------
// Force-Directed Physics Layout
// ---------------------------------------------------------------------------

const physics = {
  nodes: new Map(),  // id -> { x, y, vx, vy, pinned }
  // Tuning constants — tweak these for feel.
  centerGravity:      0.002,
  springStrength:     0.02,
  springRestLength:   120,    // system ↔ top-level
  childSpringStrength: 0.06,  // parent ↔ child (3× stronger)
  childRestLength:    80,     // parent ↔ child (tighter cluster)
  repulsionStrength:  5000,
  damping:            0.92,
  maxVelocity:        5,
};

// Ensure physics.nodes matches the current set of loops + system node.
// New nodes spawn at their parent position (or center with jitter).
function syncPhysicsNodes(cx, cy) {
  // System node — always pinned at center.
  if (state.system) {
    const sys = physics.nodes.get('__system__');
    if (sys) {
      sys.x = cx; sys.y = cy;
    } else {
      physics.nodes.set('__system__', { x: cx, y: cy, vx: 0, vy: 0, pinned: true });
    }
  } else {
    physics.nodes.delete('__system__');
  }

  // Loop nodes.
  for (const loop of state.loops.values()) {
    if (physics.nodes.has(loop.id)) continue;
    let sx, sy;
    if (loop.parent_id) {
      // Children spawn near their parent.
      const parent = physics.nodes.get(loop.parent_id);
      if (parent) {
        // Spawn at child rest length from parent with random angle
        // so the spring starts near equilibrium.
        const a = Math.random() * 2 * Math.PI;
        sx = parent.x + physics.childRestLength * Math.cos(a);
        sy = parent.y + physics.childRestLength * Math.sin(a);
      } else {
        sx = cx + (Math.random() * 40 - 20);
        sy = cy + (Math.random() * 40 - 20);
      }
    } else {
      // Top-level nodes spawn at the spring rest length from center
      // (random angle) so the spring starts near equilibrium instead
      // of repelling the node outward from inside the rest length.
      const angle = Math.random() * 2 * Math.PI;
      const r = physics.springRestLength * (0.8 + Math.random() * 0.4);
      sx = cx + r * Math.cos(angle);
      sy = cy + r * Math.sin(angle);
    }
    physics.nodes.set(loop.id, { x: sx, y: sy, vx: 0, vy: 0, pinned: false });
  }

  // Remove physics nodes for loops that no longer exist (and aren't system).
  // Exiting nodes stay until their DOM animationend handler cleans them up.
  for (const id of physics.nodes.keys()) {
    if (id === '__system__') continue;
    if (!state.loops.has(id) && !canvasWorld.querySelector(`[data-loop-id="${id}"].loop-node--exiting`)) {
      physics.nodes.delete(id);
    }
  }
}

// Run one physics simulation step. Applies center gravity, spring
// attraction (system↔top-level, parent↔child), pairwise repulsion,
// then integrates velocity and position with damping.
function physicsStep(cx, cy, vw, vh) {
  const P = physics;
  const nodes = Array.from(P.nodes.values());
  const ids = Array.from(P.nodes.keys());
  const n = nodes.length;
  if (n === 0) return;

  // Anisotropic gravity — scale per-axis so the node cloud stretches
  // to fill non-square viewports. On a square viewport the factors
  // are both 1.0 (no-op). On a 2:1 ultrawide, X gravity drops ~30%
  // and Y rises ~40%, naturally spreading nodes along the wide axis.
  const aspect = (vw && vh && vh > 0) ? vw / vh : 1;
  const sqrtA = Math.sqrt(aspect);
  const gravX = P.centerGravity / sqrtA;
  const gravY = P.centerGravity * sqrtA;

  // Reset forces.
  for (const nd of nodes) { nd.fx = 0; nd.fy = 0; }

  // 1. Center gravity — anisotropic pull toward (cx, cy).
  for (const nd of nodes) {
    if (nd.pinned) continue;
    nd.fx += (cx - nd.x) * gravX;
    nd.fy += (cy - nd.y) * gravY;
  }

  // 2. Spring forces — build edge list from loop relationships.
  for (const loop of state.loops.values()) {
    if (!P.nodes.has(loop.id)) continue;
    if (loop.parent_id && P.nodes.has(loop.parent_id)) {
      // Parent↔child: shorter rest length, stronger spring for tight clusters.
      applySpring(P.nodes.get(loop.parent_id), P.nodes.get(loop.id), P.childSpringStrength, P.childRestLength);
    } else if (P.nodes.has('__system__')) {
      // System↔top-level (or orphaned child fallback): standard spring.
      applySpring(P.nodes.get('__system__'), P.nodes.get(loop.id), P.springStrength, P.springRestLength);
    }
  }

  // 3. Pairwise repulsion (O(n²), fine for ≤20 nodes).
  const EPS = 100; // prevents division-by-zero / explosion at overlap
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) {
      const a = nodes[i], b = nodes[j];
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const distSq = dx * dx + dy * dy + EPS;
      const force = P.repulsionStrength / distSq;
      const dist = Math.sqrt(distSq);
      const fx = (dx / dist) * force;
      const fy = (dy / dist) * force;
      a.fx -= fx; a.fy -= fy;
      b.fx += fx; b.fy += fy;
    }
  }

  // 4. Integration + damping.
  for (const nd of nodes) {
    if (nd.pinned) continue;
    nd.vx = (nd.vx + nd.fx) * P.damping;
    nd.vy = (nd.vy + nd.fy) * P.damping;
    // Clamp velocity.
    const speed = Math.sqrt(nd.vx * nd.vx + nd.vy * nd.vy);
    if (speed > P.maxVelocity) {
      const scale = P.maxVelocity / speed;
      nd.vx *= scale;
      nd.vy *= scale;
    }
    nd.x += nd.vx;
    nd.y += nd.vy;
  }
}

// Apply a spring force between two nodes.
function applySpring(a, b, strength, restLength) {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const dist = Math.sqrt(dx * dx + dy * dy) || 1;
  const force = strength * (dist - restLength);
  const fx = (dx / dist) * force;
  const fy = (dy / dist) * force;
  if (!a.pinned) { a.fx += fx; a.fy += fy; }
  if (!b.pinned) { b.fx -= fx; b.fy -= fy; }
}

// Write physics positions to DOM — node transforms and linking line endpoints.
function updateNodePositions() {
  // System node.
  const sysP = physics.nodes.get('__system__');
  if (sysP) {
    const sysG = canvasWorld.querySelector('.system-node');
    if (sysG) sysG.setAttribute('transform', `translate(${sysP.x},${sysP.y})`);
  }

  // Loop nodes.
  for (const [id, nd] of physics.nodes) {
    if (id === '__system__') continue;
    const g = canvasWorld.querySelector(`[data-loop-id="${id}"]`);
    if (g) g.setAttribute('transform', `translate(${nd.x},${nd.y})`);
  }

  // Linking line endpoints.
  const lines = canvasWorld.querySelectorAll('.link-line');
  for (const line of lines) {
    const targetId = line.dataset.targetLoop;
    const parentLoop = line.dataset.parentLoop;
    // Source is either a parent loop or the system node.
    const srcId = parentLoop || '__system__';
    const src = physics.nodes.get(srcId);
    const tgt = physics.nodes.get(targetId);
    if (src && tgt) {
      line.setAttribute('x1', src.x);
      line.setAttribute('y1', src.y);
      line.setAttribute('x2', tgt.x);
      line.setAttribute('y2', tgt.y);
    }
  }
}

// ---------------------------------------------------------------------------
// Loop Category + Shape + Model Sizing
// ---------------------------------------------------------------------------

// Derive a visual category from loop data. Drives which SVG shape is drawn.
function getLoopCategory(loop) {
  const hints = loop.config && loop.config.Hints;
  if (hints && hints.source === 'metacognitive') return 'metacognitive';
  const meta = loop.config && loop.config.Metadata;
  if (meta && meta.category) return meta.category;
  if (loop.parent_id) return 'delegate';
  const name = (loop.name || '').toLowerCase();
  if (/signal|email|mqtt|slack|irc/.test(name)) return 'channel';
  if (/sched|cron|timer/.test(name)) return 'scheduled';
  return 'generic';
}

// Category → icon displayed inside the node circle.
const CATEGORY_ICONS = {
  metacognitive: '🧠',
  channel:       '💬',
  delegate:      '🔀',
  scheduled:     '🕐',
  generic:       '⚙️',
};

// Model name → approximate parameter count (billions).
// Used for area-proportional node sizing with sqrt compression.
const MODEL_SIZES = {
  // Anthropic
  'claude-haiku':        8,
  'claude-3-haiku':      8,
  'claude-3-5-haiku':    8,
  'claude-haiku-4-5':    8,
  'claude-sonnet':       70,
  'claude-3-5-sonnet':   70,
  'claude-sonnet-4':     70,
  'claude-opus':         300,
  'claude-opus-4':       300,
  // Common local models
  'gemma':               9,
  'gemma2':              9,
  'gemma3':              12,
  'phi':                 4,
  'phi3':                4,
  'phi4':                14,
  'llama3':              8,
  'llama3.1':            8,
  'llama3.2':            3,
  'llama3.3':            70,
  'mistral':             7,
  'mixtral':             47,
  'qwen2':               7,
  'qwen2.5':             7,
  'deepseek':            7,
  'deepseek-r1':         671,
  'command-r':           35,
};

// Resolve a model name string to approximate billions of parameters.
// Tries exact match, then prefix match, then extracts trailing size suffix.
function getModelParams(modelName) {
  if (!modelName) return null;
  const m = modelName.toLowerCase();

  // Exact match.
  if (MODEL_SIZES[m] !== undefined) return MODEL_SIZES[m];

  // Prefix match (e.g. "claude-sonnet-4-20250514" → "claude-sonnet-4").
  for (const [key, val] of Object.entries(MODEL_SIZES)) {
    if (m.startsWith(key)) return val;
  }

  // Extract trailing size like ":8b", ":70b", ":7b-q4".
  const sizeMatch = m.match(/:(\d+)b/i);
  if (sizeMatch) return parseInt(sizeMatch[1], 10);

  return null;
}

// Compute node radius from model parameters using sqrt compression.
// Returns a radius in [MIN_NODE_R, MAX_NODE_R].
const MIN_NODE_R = 22;
const MAX_NODE_R = 50;
const DEFAULT_NODE_R = 32;

function getModelRadius(modelName) {
  const params = getModelParams(modelName);
  if (params === null) return DEFAULT_NODE_R;

  // sqrt compression: area ∝ sqrt(params).
  // Calibrated so 8B → MIN_NODE_R, 300B → MAX_NODE_R.
  const minParams = 3;    // floor (smallest model we'd see)
  const maxParams = 700;  // ceiling (largest model we'd see)
  const t = (Math.sqrt(params) - Math.sqrt(minParams)) /
            (Math.sqrt(maxParams) - Math.sqrt(minParams));
  const clamped = Math.max(0, Math.min(1, t));
  return MIN_NODE_R + clamped * (MAX_NODE_R - MIN_NODE_R);
}

// Check whether a loop's backing service is degraded (not ready) in
// the runtime health data. Matches by loop name against service keys.
function isServiceDegraded(loopName) {
  if (!state.system || !state.system.health || !loopName) return false;
  const health = state.system.health;
  // Direct match: loop name === service key (e.g., "signal" → "signal").
  if (health[loopName] && !health[loopName].ready) return true;
  // Prefix match for child loops: "signal/Alice" → check "signal".
  const slash = loopName.indexOf('/');
  if (slash > 0) {
    const prefix = loopName.slice(0, slash);
    if (health[prefix] && !health[prefix].ready) return true;
  }
  return false;
}

// Create an SVG circle shape element at origin with radius r.
function createNodeShape(category, r) {
  return createSVG('circle', { class: 'node-shape', r: r });
}

// Update an existing circle shape element's radius.
function updateNodeShape(el, category, r) {
  el.setAttribute('r', r);
}

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
// Trust Zone Underglow
// ---------------------------------------------------------------------------

const TRUST_ZONE_COLORS = {
  admin:     '#26a69a',  // teal
  household: '#e040fb',  // purple
  trusted:   '#69f0ae',  // green
  known:     '#ffd740',  // amber
  unknown:   '#ff5252',  // red — stranger danger
};

// Inject SVG defs for the Gaussian blur filter used by trust zone underglow.
(function initTrustGlowFilter() {
  const svg = canvas;
  const defs = createSVG('defs', {});
  const filter = createSVG('filter', { id: 'trust-blur' });
  const blur = createSVG('feGaussianBlur', { in: 'SourceGraphic', stdDeviation: '6' });
  filter.appendChild(blur);
  defs.appendChild(filter);
  svg.insertBefore(defs, svg.firstChild);
})();

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
    state.iterationHistory.clear();
    for (const s of statuses) {
      // Seed iteration history from server-side ring buffer.
      if (s.recent_iterations && s.recent_iterations.length > 0) {
        state.iterationHistory.set(s.id, s.recent_iterations.slice());
      }
      // Seed live telemetry for loops already in processing state
      // so the Live Activity section shows immediately on connect.
      if (s.state === 'processing') {
        s._iterStartTs = s.last_wake_at ? new Date(s.last_wake_at).getTime() : Date.now();
        s._liveTools = [];
        s._liveModel = '';
        // Restore LLM context from snapshot so late-connecting clients
        // see enrichment data (model, tokens, complexity, etc.) immediately.
        s._llmContext = s.llm_context || null;
        if (s._llmContext && s._llmContext.model) {
          s._liveModel = s._llmContext.model;
        }
      }
      state.loops.set(s.id, s);
    }
    renderAll();
    setConnState('connected');
  });

  eventSource.addEventListener('loop', (e) => {
    const evt = JSON.parse(e.data);
    handleLoopEvent(evt);
  });

  eventSource.addEventListener('delegate', (e) => {
    const evt = JSON.parse(e.data);
    handleDelegateEvent(evt);
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

// extractDelegateCalls is in shared.js.

function handleLoopEvent(evt) {
  const loopId = evt.data && evt.data.loop_id;
  const loopName = evt.data && evt.data.loop_name;

  // Push to event log.
  state.events.unshift(evt);
  if (state.events.length > MAX_EVENTS) state.events.length = MAX_EVENTS;

  // loop_started requires a full fetch — not a per-loop mutation.
  // Also bootstrap a minimal entry immediately so that in-flight
  // events arriving before fetchLoops() completes aren't discarded.
  if (evt.kind === 'loop_started') {
    if (loopId && !state.loops.has(loopId)) {
      state.loops.set(loopId, {
        id: loopId,
        name: loopName || loopId,
        state: 'processing',
        parent_id: evt.data.parent_id || null,
        iterations: 0,
        _iterStartTs: Date.now(),
      });
    }
    fetchLoops();
    renderAll();
    return;
  }

  if (!loopId) {
    renderAll();
    return;
  }

  // Create a minimal entry for unknown loops so in-flight events
  // (e.g. loop_iteration_start arriving before fetchLoops() returns)
  // aren't silently dropped.
  if (!state.loops.has(loopId)) {
    state.loops.set(loopId, {
      id: loopId,
      name: loopName || loopId,
      state: 'processing',
      iterations: 0,
      _iterStartTs: Date.now(),
    });
  }

  const loop = state.loops.get(loopId);
  const history = state.iterationHistory.get(loopId) || [];
  const result = applyLoopEventToLoop(evt, {
    loop,
    loopId,
    sleepTimers: state.sleepTimers,
    history,
  });

  if (result && result.snapshot) {
    prependIterationSnapshot(loopId, result.snapshot);
    // Auto-refresh logs when the selected loop completes an iteration.
    if (state.selected === loopId) {
      fetchLogs(loopId);
    }
  }

  renderAll();
}

// ---------------------------------------------------------------------------
// Delegate Events → Ephemeral Nodes
// ---------------------------------------------------------------------------

// Handle delegate lifecycle events from the SSE stream. Spawn creates
// a synthetic loop entry; complete removes it (triggering exit animation).
function handleDelegateEvent(evt) {
  const did = evt.data && evt.data.delegate_id;
  if (!did) return;

  switch (evt.kind) {
    case 'spawn': {
      // Create a synthetic loop entry so the existing rendering
      // infrastructure (physics, connectors, icons) works unchanged.
      const syntheticId = 'delegate-' + did;
      state.loops.set(syntheticId, {
        id: syntheticId,
        name: evt.data.name || syntheticId,
        state: 'processing',
        parent_id: evt.data.parent_loop_id || null,
        config: {
          Metadata: { category: 'delegate' },
        },
        _delegate: true,
        _delegateId: did,
        _delegateTask: evt.data.task || '',
        _delegateProfile: evt.data.profile || '',
        _delegateGuidance: evt.data.guidance || '',
        _delegateTags: evt.data.tags || [],
        _iterStartTs: Date.now(),
      });
      renderAll();
      break;
    }
    case 'complete': {
      const syntheticId = 'delegate-' + did;
      state.sleepTimers.delete(syntheticId);

      // Update state but keep the node around so it's still clickable.
      const entry = state.loops.get(syntheticId);
      if (entry) {
        entry.state = evt.data.exhausted ? 'error' : 'completed';
        entry._delegateExhausted = !!evt.data.exhausted;
        entry._delegateExhaustReason = evt.data.exhaust_reason || '';
        entry._delegateDurationMs = evt.data.duration_ms || 0;
        entry._delegateIterations = evt.data.iterations || 0;
      }

      // Fade to translucent, then remove after a linger period.
      const node = canvasWorld.querySelector(`[data-loop-id="${syntheticId}"]`);
      if (node) node.classList.add('loop-node--fading');

      setTimeout(() => {
        // Don't remove if user has it selected — let them inspect.
        if (state.selected === syntheticId) {
          // Re-check after another delay.
          const recheck = () => {
            if (state.selected !== syntheticId) {
              removeDelegateNode(syntheticId);
            } else {
              setTimeout(recheck, 5000);
            }
          };
          setTimeout(recheck, 5000);
        } else {
          removeDelegateNode(syntheticId);
        }
      }, 15000); // linger 15s

      renderAll();
      break;
    }
  }
}

function removeDelegateNode(syntheticId) {
  const node = canvasWorld.querySelector(`[data-loop-id="${syntheticId}"]`);
  if (node) {
    node.classList.add('loop-node--exiting');
    node.addEventListener('animationend', () => {
      node.remove();
      physics.nodes.delete(syntheticId);
    }, { once: true });
  } else {
    physics.nodes.delete(syntheticId);
  }
  state.loops.delete(syntheticId);
  if (state.selected === syntheticId) {
    state.selected = null;
  }
  renderAll();
}

// ---------------------------------------------------------------------------
// Data Fetching
// ---------------------------------------------------------------------------

async function fetchLoops() {
  try {
    const resp = await fetch('/api/loops');
    if (!resp.ok) return;
    const statuses = await resp.json();

    // Merge server state with existing entries to preserve transient
    // telemetry (_iterStartTs, _liveTools, _liveModel, etc.) that may
    // have been set by in-flight SSE events before this fetch returned.
    const serverIds = new Set();
    for (const s of statuses) {
      serverIds.add(s.id);
      const existing = state.loops.get(s.id);
      if (existing) {
        // Preserve transient fields that the server doesn't track.
        const transient = [
          '_iterStartTs', '_liveTools', '_liveModel', '_llmContext',
          '_supervisor', '_currentConvID', '_lastModel', '_lastSupervisor',
          '_delegate', '_delegateId', '_delegateTask', '_delegateProfile',
          '_delegateGuidance', '_delegateTags', '_delegateIterations',
          '_delegateDurationMs', '_delegateExhausted', '_delegateExhaustReason',
        ];
        for (const key of transient) {
          if (existing[key] !== undefined && s[key] === undefined) {
            s[key] = existing[key];
          }
        }
      }
      state.loops.set(s.id, s);
    }

    // Remove loops that the server no longer reports, but keep
    // delegate nodes (they're client-only ephemeral entries).
    for (const id of state.loops.keys()) {
      if (!serverIds.has(id) && !state.loops.get(id)?._delegate) {
        state.loops.delete(id);
      }
    }

    renderAll();
  } catch (err) {
    console.warn('Failed to fetch loops:', err);
  }
}

async function fetchLogs(loopId) {
  if (!loopId) return;
  // Ephemeral delegate nodes aren't real loops — no logs endpoint.
  const loop = state.loops.get(loopId);
  if (loop && loop._delegate) {
    renderLogs([]);
    return;
  }
  const level = $('#log-level').value;
  let url = '/api/loops/' + encodeURIComponent(loopId) + '/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);

  try {
    const resp = await fetch(url);
    if (!resp.ok) return;
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

let _renderRAF = 0;

// Schedule a render on the next animation frame. Coalesces multiple
// calls (e.g. SSE event bursts after a background-tab wakeup) into a
// single paint, preventing DOM thrashing and race conditions.
function renderAll() {
  if (_renderRAF) return;          // already scheduled
  _renderRAF = requestAnimationFrame(() => {
    _renderRAF = 0;
    // Each sub-render is isolated so a failure in one doesn't block the rest.
    try { renderNodes(); }      catch (e) { console.error('renderNodes:', e); }
    try { renderDetail(); }     catch (e) { console.error('renderDetail:', e); }
  });
}

function renderNodes() {
  const loops = Array.from(state.loops.values());
  const hasSystem = state.system !== null;
  emptyState.hidden = loops.length > 0 || hasSystem;

  // Canvas center — used as gravity anchor and for new-node spawn.
  const rect = canvas.getBoundingClientRect();
  const cx = rect.width / 2;
  const cy = rect.height / 2;

  // Sync physics state with current loops (add new, remove stale).
  syncPhysicsNodes(cx, cy);

  // Detect new nodes for enter animation.
  const newIds = new Set();
  for (const loop of loops) {
    if (!state.knownLoopIds.has(loop.id)) newIds.add(loop.id);
  }

  // Create/update DOM nodes (no position-setting — physics handles that).
  for (const loop of loops) {
    renderNode(loop);
  }

  // Enter animations — physics naturally opens space, so animate immediately.
  for (const id of newIds) {
    const group = canvasWorld.querySelector(`[data-loop-id="${id}"]`);
    if (!group) continue;
    const inner = group.querySelector('.node-inner');
    if (!inner) continue;
    inner.classList.add('node-inner--entering');
    inner.addEventListener('animationend', () => {
      inner.classList.remove('node-inner--entering');
    }, { once: true });
  }

  // Remove nodes for loops that no longer exist (with exit animation).
  const existingGroups = canvasWorld.querySelectorAll('.loop-node');
  for (const g of existingGroups) {
    const id = g.dataset.loopId;
    if (!state.loops.has(id)) {
      if (!g.classList.contains('loop-node--exiting')) {
        g.classList.add('loop-node--exiting');
        g.addEventListener('animationend', () => {
          g.remove();
          physics.nodes.delete(id);
        }, { once: true });
        state.knownLoopIds.delete(id);
        state.prevIterations.delete(id);
        state.prevErrors.delete(id);
      }
    }
  }

  // System node.
  if (hasSystem) {
    renderSystemNode();
  } else {
    const existing = canvasWorld.querySelector('.system-node');
    if (existing) existing.remove();
  }

  // Linking lines: create/remove DOM elements and apply state classes.
  // Position updates (x1/y1/x2/y2) are handled by updateNodePositions().
  renderLinkingLines(hasSystem, loops);
}

// Manage linking line DOM lifecycle — create/remove elements and apply
// state classes. Positions (x1/y1/x2/y2) are set by updateNodePositions().
function renderLinkingLines(hasSystem, loops) {
  // Top-level loops are those without a parent_id.
  const topLevel = loops.filter(l => !l.parent_id);
  const activeIds = new Set(topLevel.map(l => l.id));

  // Child loops are those with a parent_id.
  const children = loops.filter(l => l.parent_id);
  const childKeys = new Set(children.map(l => l.id));

  // Build a set of all valid link targets.
  const allValidTargets = new Set([...activeIds, ...childKeys]);

  // Remove stale link lines for loops that no longer exist.
  const existing = canvasWorld.querySelectorAll('.link-line');
  for (const el of existing) {
    const target = el.dataset.targetLoop;
    const isSystemLink = !el.dataset.parentLoop;
    if (isSystemLink && (!hasSystem || !activeIds.has(target))) {
      el.remove();
    } else if (!isSystemLink && !allValidTargets.has(target)) {
      el.remove();
    }
  }

  // System → top-level lines.
  if (hasSystem) {
    for (const loop of topLevel) {
      let line = canvasWorld.querySelector(`.link-line[data-target-loop="${loop.id}"]:not([data-parent-loop])`);

      if (!line) {
        line = createSVG('line', {
          class: 'link-line',
          'data-target-loop': loop.id,
        });
        // Insert before nodes so lines draw behind them.
        canvasWorld.insertBefore(line, canvasWorld.firstChild);
      }

      // State-driven styling: error or degraded service turns the line orange/red.
      if (loop.state === 'error') {
        line.setAttribute('class', 'link-line link-line--error');
      } else if (isServiceDegraded(loop.name)) {
        line.setAttribute('class', 'link-line link-line--degraded');
      } else {
        line.setAttribute('class', 'link-line');
      }
      line.dataset.targetLoop = loop.id;
    }
  }

  // Parent → child lines.
  for (const child of children) {
    const selector = `.link-line[data-target-loop="${child.id}"][data-parent-loop="${child.parent_id}"]`;
    let line = canvasWorld.querySelector(selector);

    if (!line) {
      line = createSVG('line', {
        class: 'link-line link-line--child',
        'data-target-loop': child.id,
        'data-parent-loop': child.parent_id,
      });
      canvasWorld.insertBefore(line, canvasWorld.firstChild);
    }

    // State-driven styling for child lines.
    let cls = 'link-line link-line--child';
    if (child.state === 'error') {
      cls += ' link-line--error';
    }
    line.setAttribute('class', cls);
    line.dataset.targetLoop = child.id;
    line.dataset.parentLoop = child.parent_id;
  }
}

// Flash a linking line briefly (called on supervisor events).
// When loopId is provided, only flash that loop's line; otherwise flash all.
function flashLinkingLine(loopId) {
  const selector = loopId
    ? `.link-line[data-target-loop="${loopId}"]`
    : '.link-line';
  const lines = canvasWorld.querySelectorAll(selector);
  for (const line of lines) {
    const baseClass = line.getAttribute('class').replace(' link-line--flash', '');
    line.setAttribute('class', baseClass + ' link-line--flash');
    setTimeout(() => {
      line.setAttribute('class', baseClass);
    }, 300);
  }
}

function renderNode(loop) {
  const category = getLoopCategory(loop);
  const nodeR = getModelRadius(loop._lastModel);
  const ringR = nodeR + 12;
  let group = canvasWorld.querySelector(`[data-loop-id="${loop.id}"]`);

  if (!group) {
    group = createSVG('g', {
      class: 'loop-node',
      'data-loop-id': loop.id,
      'data-category': category,
    });
    group.addEventListener('click', () => selectLoop(loop.id));
    group.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      const items = [];
      // Synthetic delegate nodes have no backend endpoint — skip detail popup.
      if (!loop.id.startsWith('delegate-')) {
        items.push({ label: 'Open in window', action: () => openDetailWindow('loop', loop.id) });
        items.push({ separator: true });
      }
      items.push({ label: 'Copy loop ID', action: () => navigator.clipboard.writeText(loop.id) });
      showContextMenu(e.clientX, e.clientY, items);
    });

    // Inner group for enter/exit scale animation (children drawn at origin).
    const inner = createSVG('g', { class: 'node-inner' });

    // Native SVG tooltip — instant, no delay.
    const title = createSVG('title', {});
    title.textContent = loop.name || loop.id;
    inner.appendChild(title);

    // Trust zone underglow — diffused coloured circle behind the node.
    const trustZone = loop.config && loop.config.Metadata && loop.config.Metadata.trust_zone;
    if (trustZone && TRUST_ZONE_COLORS[trustZone]) {
      const glow = createSVG('circle', {
        class: 'trust-glow',
        r: nodeR + 3,
        fill: TRUST_ZONE_COLORS[trustZone],
        filter: 'url(#trust-blur)',
      });
      inner.appendChild(glow);
    }

    // Glow ring (always a circle regardless of shape).
    const ring = createSVG('circle', {
      class: 'node-ring',
      r: ringR,
      fill: 'none',
      stroke: 'var(--accent)',
      'stroke-width': 2,
    });

    // Sleep progress ring (always a circle).
    const circumference = 2 * Math.PI * (nodeR);
    const sleepRing = createSVG('circle', {
      class: 'sleep-ring',
      r: nodeR,
      'stroke-dasharray': circumference,
      'stroke-dashoffset': circumference,
    });

    // Main shape — always a circle.
    const shapeEl = createNodeShape(category, nodeR);

    // Category icon centered inside the node.
    const icon = createSVG('text', {
      class: 'node-icon',
      'text-anchor': 'middle',
      'dominant-baseline': 'central',
      'font-size': Math.round(nodeR * 0.7),
    });
    icon.textContent = CATEGORY_ICONS[category] || CATEGORY_ICONS.generic;

    // Supervisor ring (larger circle outside the node).
    const supDot = createSVG('circle', {
      class: 'supervisor-dot',
      r: nodeR + 10,
    });

    // Label.
    const label = createSVG('text', {
      class: 'node-label',
      y: nodeR + 18,
    });
    // Child loops show just the suffix after "/" since the parent
    // line makes the hierarchy clear (e.g., "signal/Alice" → "Alice").
    const displayName = loop.name || loop.id.slice(0, 8);
    const slash = loop.parent_id ? displayName.indexOf('/') : -1;
    label.textContent = slash > 0 ? displayName.slice(slash + 1) : displayName;

    inner.appendChild(ring);
    inner.appendChild(sleepRing);
    inner.appendChild(shapeEl);
    inner.appendChild(icon);
    inner.appendChild(supDot);
    inner.appendChild(label);
    group.appendChild(inner);
    canvasWorld.appendChild(group);

    // Mark as known — enter animation is triggered by renderNodes().
    state.knownLoopIds.add(loop.id);
  }

  // Update trust zone underglow colour if it changed or appeared.
  const trustZone = loop.config && loop.config.Metadata && loop.config.Metadata.trust_zone;
  const glowEl = group.querySelector('.trust-glow');
  if (trustZone && TRUST_ZONE_COLORS[trustZone]) {
    if (glowEl) {
      glowEl.setAttribute('fill', TRUST_ZONE_COLORS[trustZone]);
      glowEl.setAttribute('r', nodeR + 3);
    } else {
      // Trust zone appeared after initial render — insert glow.
      const inner = group.querySelector('.node-inner');
      const glow = createSVG('circle', {
        class: 'trust-glow',
        r: nodeR + 3,
        fill: TRUST_ZONE_COLORS[trustZone],
        filter: 'url(#trust-blur)',
      });
      // Insert after <title> (first child) so it's behind everything.
      const title = inner.querySelector('title');
      if (title && title.nextSibling) {
        inner.insertBefore(glow, title.nextSibling);
      } else {
        inner.appendChild(glow);
      }
    }
  } else if (glowEl) {
    glowEl.remove();
  }

  // Dynamic resizing — update shape, rings, label when model changes.
  const prevR = parseFloat(group.dataset.nodeR) || DEFAULT_NODE_R;
  if (Math.abs(nodeR - prevR) > 0.5) {
    group.dataset.nodeR = nodeR;
    const shapeEl = group.querySelector('.node-shape');
    updateNodeShape(shapeEl, category, nodeR);

    // Update dependent radii.
    const newRingR = nodeR + 12;
    group.querySelector('.node-ring').setAttribute('r', newRingR);
    const sleepRing = group.querySelector('.sleep-ring');
    const newSleepR = nodeR;
    sleepRing.setAttribute('r', newSleepR);
    const circ = 2 * Math.PI * newSleepR;
    sleepRing.setAttribute('stroke-dasharray', circ);
    group.querySelector('.supervisor-dot').setAttribute('r', nodeR + 10);
    const iconEl = group.querySelector('.node-icon');
    if (iconEl) iconEl.setAttribute('font-size', Math.round(nodeR * 0.7));
    group.querySelector('.node-label').setAttribute('y', nodeR + 18);
  }
  group.dataset.nodeR = nodeR;

  // Update state class on main shape — supervisor processing gets its own style.
  // If the loop's backing service is degraded, override idle states with the
  // degraded visual so the canvas reflects connwatch health.
  const shapeEl = group.querySelector('.node-shape');
  const isSup = loop._supervisor && loop.state === 'processing';
  const svcDegraded = isServiceDegraded(loop.name);
  let stateClass;
  if (isSup) {
    stateClass = 'node-shape--supervisor';
  } else if (svcDegraded && (loop.state === 'sleeping' || loop.state === 'waiting')) {
    stateClass = 'node-shape--degraded';
  } else {
    stateClass = 'node-shape--' + (loop.state || 'pending');
  }
  shapeEl.setAttribute('class', 'node-shape ' + stateClass);

  // Stroke width represents context utilization percentage.
  const ctxPct = (loop.context_window > 0 && loop.last_input_tokens > 0)
    ? Math.min(1, loop.last_input_tokens / loop.context_window)
    : 0;
  const minStroke = 2;
  const maxStroke = 10;
  const strokeW = ctxPct > 0
    ? minStroke + ctxPct * (maxStroke - minStroke)
    : minStroke;
  shapeEl.setAttribute('stroke-width', strokeW.toFixed(1));

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
    ring.addEventListener('animationend', () => {
      ring.classList.remove('node-ring--flash');
    }, { once: true });

    // Brief green pulse on the shape — guarantees visual feedback for
    // fast handler loops where processing state is too brief to render.
    const shape = group.querySelector('.node-shape');
    shape.classList.remove('node-shape--iter-pulse');
    void shape.offsetWidth;
    shape.classList.add('node-shape--iter-pulse');
    shape.addEventListener('animationend', () => {
      shape.classList.remove('node-shape--iter-pulse');
    }, { once: true });

    // Flash the linking line if this is the metacognitive loop and a supervisor fired.
    if (loop.name === 'metacognitive' && loop._lastSupervisor) {
      flashLinkingLine(loop.id);
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

  const elapsed = Date.now() - timer.startedAt;
  const progress = Math.min(1, elapsed / timer.durationMs);
  const offset = circumference * (1 - progress);
  sleepRing.setAttribute('stroke-dashoffset', offset);
}

function renderSystemNode() {
  const sys = state.system;
  const s = 48, r = 10; // 1:1 square, s = side length
  const ringR = s / 2 + 12; // glow ring radius (matches loop node pattern)
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

    // Glow/selection ring (same as loop nodes).
    const ring = createSVG('circle', {
      class: 'node-ring',
      r: ringR,
      fill: 'none',
      stroke: 'var(--accent)',
      'stroke-width': 2,
    });
    group.appendChild(ring);

    // Rounded square (1:1 aspect ratio).
    const rect = createSVG('rect', {
      class: 'system-rect',
      x: -s / 2, y: -s / 2,
      width: s, height: s,
      rx: r, ry: r,
    });
    group.appendChild(rect);

    const label = createSVG('text', {
      class: 'node-label',
      y: s / 2 + 16,
    });
    label.textContent = 'runtime';
    group.appendChild(label);

    canvasWorld.appendChild(group);
  }

  // Update health-based fill.
  const rect = group.querySelector('.system-rect');
  const cls = sys.status === 'healthy'
    ? 'system-rect system-rect--healthy'
    : 'system-rect system-rect--degraded';
  rect.setAttribute('class', cls);

  // Selection highlight (uses node-ring halo, same as loop nodes).
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

  // Delegate detail (task, profile, guidance, tags).
  renderDelegateDetail(loop);

  // Aggregate stats bar.
  renderAggregates(loop, $('#detail-aggregates'));

  // Iteration timeline.
  renderTimeline(loop, $('#detail-timeline'), state.iterationHistory.get(loop.id) || [], loop.id, state.sleepTimers);

  // Capabilities: show configured tags (muted if inactive) and
  // dynamically activated tags (dashed border if not in config).
  const configTags = (loop.config && loop.config.Tags) || [];
  const activeTags = new Set(loop.active_tags || []);
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

// renderAggregates, renderTimeline, clearLiveTelemetry are in shared.js.

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

  // Recent conversation IDs — skip for handler-only loops where the
  // IDs are just iteration counters with no associated LLM conversation.
  const convs = loop.recent_conv_ids;
  const MAX_VISIBLE_CONVS = 5;
  if (convs && convs.length > 0 && !loop.handler_only) {
    const row = document.createElement('div');
    row.className = 'id-row';

    const label = document.createElement('span');
    label.className = 'id-label';
    label.textContent = 'recent';
    row.appendChild(label);

    const chips = document.createElement('span');
    chips.className = 'id-convs';
    const visible = convs.slice(0, MAX_VISIBLE_CONVS);
    for (const cid of visible) {
      chips.appendChild(makeIDChip(cid));
    }
    if (convs.length > MAX_VISIBLE_CONVS) {
      const more = document.createElement('span');
      more.className = 'id-chip id-chip--muted';
      more.textContent = '+' + (convs.length - MAX_VISIBLE_CONVS);
      chips.appendChild(more);
    }
    row.appendChild(chips);
    container.appendChild(row);
  }
}

// ---------------------------------------------------------------------------
// Rendering — Delegate Detail
// ---------------------------------------------------------------------------

function renderDelegateDetail(loop) {
  const container = $('#detail-delegate');
  if (!loop._delegate) {
    container.hidden = true;
    return;
  }
  container.hidden = false;
  container.innerHTML = '';

  // Task.
  if (loop._delegateTask) {
    const taskEl = document.createElement('div');
    taskEl.className = 'delegate-field';
    const label = document.createElement('span');
    label.className = 'delegate-label';
    label.textContent = 'Task';
    taskEl.appendChild(label);
    const val = document.createElement('span');
    val.className = 'delegate-value delegate-task';
    val.textContent = loop._delegateTask;
    taskEl.appendChild(val);
    container.appendChild(taskEl);
  }

  // Guidance.
  if (loop._delegateGuidance) {
    const guidEl = document.createElement('div');
    guidEl.className = 'delegate-field';
    const label = document.createElement('span');
    label.className = 'delegate-label';
    label.textContent = 'Guidance';
    guidEl.appendChild(label);
    const val = document.createElement('span');
    val.className = 'delegate-value';
    val.textContent = loop._delegateGuidance;
    guidEl.appendChild(val);
    container.appendChild(guidEl);
  }

  // Profile + tags row.
  const metaRow = document.createElement('div');
  metaRow.className = 'delegate-meta';
  if (loop._delegateProfile) {
    const chip = document.createElement('span');
    chip.className = 'tag-chip';
    chip.textContent = loop._delegateProfile;
    metaRow.appendChild(chip);
  }
  if (loop._delegateTags && loop._delegateTags.length > 0) {
    for (const tag of loop._delegateTags) {
      const chip = document.createElement('span');
      chip.className = 'tag-chip tag-chip--muted';
      chip.textContent = tag;
      metaRow.appendChild(chip);
    }
  }
  if (metaRow.children.length > 0) container.appendChild(metaRow);

  // Completion result (shown after delegate finishes).
  if (loop.state === 'completed' || loop.state === 'error') {
    const resultEl = document.createElement('div');
    resultEl.className = 'delegate-result ' + (loop._delegateExhausted ? 'delegate-result--failed' : 'delegate-result--ok');

    const icon = loop._delegateExhausted ? '\u2717' : '\u2713';
    const status = loop._delegateExhausted
      ? 'Failed' + (loop._delegateExhaustReason ? ' \u2014 ' + loop._delegateExhaustReason : '')
      : 'Succeeded';

    const parts = [icon + ' ' + status];
    if (loop._delegateIterations > 0) parts.push(loop._delegateIterations + ' iter');
    if (loop._delegateDurationMs > 0) parts.push(formatFuzzy(loop._delegateDurationMs));
    resultEl.textContent = parts.join(' \u00b7 ');
    container.appendChild(resultEl);
  }
}

// makeIDRow, makeIDChip, shortID, shortModelName, buildToolCounts,
// escapeHTML, truncate are in shared.js.

function prependIterationSnapshot(loopId, snap) {
  let arr = state.iterationHistory.get(loopId);
  if (!arr) {
    arr = [];
    state.iterationHistory.set(loopId, arr);
  }
  arr.unshift(snap);
  if (arr.length > MAX_ITERATION_HISTORY) arr.length = MAX_ITERATION_HISTORY;
}

// ---------------------------------------------------------------------------
// Rendering — Log Panel
// ---------------------------------------------------------------------------

// renderLogRows and buildLogDetail are in shared.js.
function renderLogs(entries) {
  renderLogRows(entries, { logEmpty, logScroll, logBody });
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

function selectLoop(loopId) {
  clearInterval(systemLogInterval);
  systemLogInterval = null;
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

let systemLogInterval = null;

function selectSystem() {
  clearInterval(systemLogInterval);
  systemLogInterval = null;
  if (state.selected === '__system__') {
    state.selected = null;
    logEmpty.hidden = false;
    logEmpty.querySelector('p').textContent = 'Click a loop node to load logs';
    logScroll.hidden = true;
  } else {
    state.selected = '__system__';
    fetchSystemLogs();
    systemLogInterval = setInterval(fetchSystemLogs, 10000);
  }
  renderAll();
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
// Animation Loop (sleep countdowns + progress rings)
// ---------------------------------------------------------------------------

let _lastTickSec = 0;

function tick() {
  // Physics simulation — run every frame for smooth organic motion.
  const rect = canvas.getBoundingClientRect();
  if (rect.width > 0 && rect.height > 0) {
    physicsStep(rect.width / 2, rect.height / 2, rect.width, rect.height);
    updateNodePositions();
  }

  // Throttle detail updates to ~1Hz (sleep countdowns don't need 60fps).
  const nowSec = Math.floor(Date.now() / 1000);
  if (nowSec !== _lastTickSec) {
    _lastTickSec = nowSec;
    if (state.selected && state.loops.has(state.selected)) {
      try { renderDetail(); } catch (e) { console.error('tick renderDetail:', e); }
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
    'popup=yes,width=900,height=450'
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
      if (activeRequestID) {
        closeRequestDetail();
      }
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

// formatNumber, formatTokens, formatDuration, formatTime, formatTimeShort,
// timeAgo, parseDuration, formatUptimeLong are in shared.js.

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
    if (e.target !== canvas && e.target.closest('#canvas-world') !== null) return;
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
// Request Detail Panel
// ---------------------------------------------------------------------------

const requestDetailPanel = $('#request-detail');
const requestDetailEls = {
  ids: $('#request-detail-ids'),
  meta: $('#request-detail-meta'),
  content: $('#request-detail-content'),
  waterfall: $('#request-detail-waterfall'),
};

// Currently displayed request ID (for deep linking and back button).
let activeRequestID = null;

// Cached raw detail JSON for copy-as-JSON feature.
let activeRequestJSON = null;

// AbortController for in-flight request detail fetches. Prevents stale
// data from overwriting the panel when the user clicks rapidly.
let requestDetailAbort = null;

async function showRequestDetail(requestID) {
  if (!requestID) return;

  // Cancel any in-flight fetch for a previous request.
  if (requestDetailAbort) {
    requestDetailAbort.abort();
  }
  const controller = new AbortController();
  requestDetailAbort = controller;

  try {
    const resp = await fetch('/api/requests/' + encodeURIComponent(requestID), {
      signal: controller.signal,
    });

    // Verify this is still the active request — a newer click may have
    // replaced the controller while we were awaiting the response.
    if (requestDetailAbort !== controller) return;

    if (!resp.ok) {
      if (resp.status === 404) {
        console.warn('Request detail not found:', requestID);
      }
      // Close the panel so a stale previous request doesn't remain visible.
      closeRequestDetail();
      return;
    }
    const detail = await resp.json();

    // Re-check after parsing — another click could have landed.
    if (requestDetailAbort !== controller) return;

    activeRequestID = requestID;
    activeRequestJSON = JSON.stringify(detail, null, 2);

    // Show the request detail panel, hide others.
    detailPlaceholder.hidden = true;
    detailContent.hidden = true;
    systemDetail.hidden = true;
    requestDetailPanel.hidden = false;

    renderRequestDetail(detail, requestDetailEls);

    // Update URL fragment for deep linking. Use location.hash (which
    // creates a history entry) so the browser Back button closes the panel.
    window.location.hash = 'request/' + requestID;
  } catch (err) {
    if (err.name === 'AbortError') return; // Superseded by a newer request.
    console.warn('Failed to fetch request detail:', err);
  }
}

function closeRequestDetail() {
  activeRequestID = null;
  activeRequestJSON = null;
  // Cancel any in-flight fetch so a stale response can't re-open the panel.
  if (requestDetailAbort) {
    requestDetailAbort.abort();
    requestDetailAbort = null;
  }
  requestDetailPanel.hidden = true;
  // Restore the previous detail panel state.
  renderAll();
  // Clear hash while preserving path and query string.
  history.replaceState(null, '', window.location.pathname + window.location.search);
}

$('#request-detail-close').addEventListener('click', closeRequestDetail);
$('#request-detail-copy').addEventListener('click', () => {
  if (!activeRequestJSON) return;
  const btn = $('#request-detail-copy');
  navigator.clipboard.writeText(activeRequestJSON).then(() => {
    btn.textContent = 'Copied';
    btn.classList.add('copy-btn--copied');
    setTimeout(() => {
      btn.textContent = 'JSON';
      btn.classList.remove('copy-btn--copied');
    }, 1200);
  });
});

// Override renderDetail to respect active request detail view.
const _origRenderDetail = renderDetail;
// eslint-disable-next-line no-global-assign
renderDetail = function() {
  if (activeRequestID && !requestDetailPanel.hidden) {
    return; // Don't overwrite the request detail panel.
  }
  _origRenderDetail();
};

// Probe whether content retention is enabled. The callback is only set
// if the API endpoint is available (not 503), so request ID chips in
// shared.js render as plain copy-on-click when retention is disabled.
async function probeContentRetention() {
  try {
    // Use a dummy ID — we only care about the status code.
    const resp = await fetch('/api/requests/_probe');
    // 404 = endpoint works, no such request. 503 = retention disabled.
    if (resp.status !== 503) {
      window.onRequestChipClick = showRequestDetail;
    }
  } catch (_) {
    // Network error — leave chips as non-inspectable.
  }
}

// ---------------------------------------------------------------------------
// Deep Link Routing (URL Fragment)
// ---------------------------------------------------------------------------

function handleHashRoute() {
  const hash = window.location.hash;
  const match = hash && hash.match(/^#request\/(.+)$/);

  if (match) {
    const id = decodeURIComponent(match[1]);
    // Skip if already showing this request (avoids re-fetch when
    // showRequestDetail sets location.hash and triggers hashchange).
    if (id !== activeRequestID) {
      showRequestDetail(id);
    }
    return;
  }

  // Hash is empty or doesn't match a request route — close any open
  // request detail so UI stays in sync with URL (e.g. browser Back).
  if (requestDetailPanel && !requestDetailPanel.hidden) {
    activeRequestID = null;
    activeRequestJSON = null;
    if (requestDetailAbort) {
      requestDetailAbort.abort();
      requestDetailAbort = null;
    }
    requestDetailPanel.hidden = true;
    renderAll();
  }
}

window.addEventListener('hashchange', handleHashRoute);

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

connect();
fetchVersionInfo();
fetchSystemStatus();
probeContentRetention().then(handleHashRoute);
// Refresh uptime display every second.
setInterval(updateUptime, 1000);
// Refresh system status every 10s.
setInterval(fetchSystemStatus, 10000);
requestAnimationFrame(tick);
