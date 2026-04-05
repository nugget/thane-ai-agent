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
  notifications: [],      // active dashboard notifications (newest first)
  sleepTimers: new Map(), // id -> { startedAt: number (ms timestamp), durationMs: number }
  iterationHistory: new Map(), // id -> array of iteration snapshots (newest first)
  system: null,           // system status object from /api/system
  prevIterations: new Map(), // id -> last known iteration count (for flash detection)
  prevErrors: new Map(),     // id -> last known error string (for shake detection)
  knownLoopIds: new Set(),   // ids we've rendered before (for enter animation)
  canvasRect: null,          // last observed canvas viewport for responsive graph reflow
  conversationIndex: {
    fetchedAt: 0,
    loading: null,
    summaries: new Map(),
  },
  conversationDetails: new Map(), // conversation_id -> derived dashboard summary
  conversationLoads: new Map(),   // conversation_id -> in-flight loader promise
};

const MAX_EVENTS = 50;
const MAX_NOTIFICATIONS = 8;
const CONVERSATION_SUMMARY_TTL_MS = 15000;
const CONVERSATION_SESSION_LIMIT = 8;

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
  wallStrength:       0.045,
  collisionPadding:   16,
  resizeVelocityGain: 0.12,
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

function cloneRect(rect) {
  return rect ? { width: rect.width, height: rect.height } : null;
}

function getCanvasRectSnapshot() {
  return cloneRect(canvas.getBoundingClientRect());
}

function isCanvasRectChanged(prevRect, nextRect) {
  if (!prevRect || !nextRect) return true;
  return Math.abs(prevRect.width - nextRect.width) > 0.5 ||
    Math.abs(prevRect.height - nextRect.height) > 0.5;
}

function reflowPhysicsNodes(prevRect, nextRect) {
  if (!prevRect || !nextRect || prevRect.width <= 0 || prevRect.height <= 0 ||
      nextRect.width <= 0 || nextRect.height <= 0) {
    return;
  }

  const prevCx = prevRect.width / 2;
  const prevCy = prevRect.height / 2;
  const nextCx = nextRect.width / 2;
  const nextCy = nextRect.height / 2;
  const scaleX = Math.max(0.65, Math.min(1.65, nextRect.width / prevRect.width));
  const scaleY = Math.max(0.65, Math.min(1.65, nextRect.height / prevRect.height));

  for (const [id, nd] of physics.nodes) {
    if (id === '__system__') {
      nd.x = nextCx;
      nd.y = nextCy;
      nd.vx = 0;
      nd.vy = 0;
      continue;
    }

    const oldX = nd.x;
    const oldY = nd.y;
    const dx = oldX - prevCx;
    const dy = oldY - prevCy;
    const nextX = nextCx + dx * scaleX;
    const nextY = nextCy + dy * scaleY;
    nd.x = nextX;
    nd.y = nextY;
    nd.vx += (nextX - oldX) * physics.resizeVelocityGain;
    nd.vy += (nextY - oldY) * physics.resizeVelocityGain;
  }
}

function refreshCanvasViewport() {
  const nextRect = getCanvasRectSnapshot();
  if (!nextRect) return null;
  if (isCanvasRectChanged(state.canvasRect, nextRect)) {
    reflowPhysicsNodes(state.canvasRect, nextRect);
    state.canvasRect = cloneRect(nextRect);
  }
  return nextRect;
}

function getPhysicsNodeExtent(id) {
  if (id === '__system__') return 38;
  const loop = state.loops.get(id);
  if (!loop) return DEFAULT_NODE_R + 14;
  // Space the graph by the visible outer ring/halo footprint rather than
  // just the core circle so neighboring node borders don't visually touch.
  return getLoopVisualCapacity(loop).radius + 14;
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

  // 3. Soft border gravity keeps the cloud within the viewport while still
  // letting the force layout breathe and adapt to different aspect ratios.
  const padX = Math.max(44, vw * 0.08);
  const padY = Math.max(52, vh * 0.1);
  for (let i = 0; i < n; i++) {
    const id = ids[i];
    const nd = nodes[i];
    if (nd.pinned) continue;
    const radius = getPhysicsNodeExtent(id);
    const minX = padX + radius;
    const maxX = Math.max(minX, vw - padX - radius);
    const minY = padY + radius;
    const maxY = Math.max(minY, vh - padY - radius);

    if (nd.x < minX) nd.fx += (minX - nd.x) * P.wallStrength;
    else if (nd.x > maxX) nd.fx -= (nd.x - maxX) * P.wallStrength;

    if (nd.y < minY) nd.fy += (minY - nd.y) * P.wallStrength;
    else if (nd.y > maxY) nd.fy -= (nd.y - maxY) * P.wallStrength;
  }

  // 4. Pairwise repulsion (O(n²), fine for dashboard-sized graphs).
  const EPS = 100; // prevents division-by-zero / explosion at overlap
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) {
      const a = nodes[i], b = nodes[j];
      const dx = b.x - a.x;
      const dy = b.y - a.y;
      const distSq = dx * dx + dy * dy + EPS;
      const dist = Math.sqrt(distSq);
      const baseForce = P.repulsionStrength / distSq;
      const minGap = getPhysicsNodeExtent(ids[i]) + getPhysicsNodeExtent(ids[j]) + P.collisionPadding;
      const overlapForce = dist < minGap ? (minGap - dist) * 0.14 : 0;
      const force = baseForce + overlapForce;
      const fx = (dx / dist) * force;
      const fy = (dy / dist) * force;
      a.fx -= fx; a.fy -= fy;
      b.fx += fx; b.fy += fy;
    }
  }

  // 5. Integration + damping.
  for (let i = 0; i < n; i++) {
    const id = ids[i];
    const nd = nodes[i];
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

    // Safety clamp after integration so a burst of forces cannot eject nodes
    // off-screen between frames.
    const radius = getPhysicsNodeExtent(id);
    const minX = padX + radius;
    const maxX = Math.max(minX, vw - padX - radius);
    const minY = padY + radius;
    const maxY = Math.max(minY, vh - padY - radius);
    if (nd.x < minX) {
      nd.x = minX;
      nd.vx *= 0.5;
    } else if (nd.x > maxX) {
      nd.x = maxX;
      nd.vx *= 0.5;
    }
    if (nd.y < minY) {
      nd.y = minY;
      nd.vy *= 0.5;
    } else if (nd.y > maxY) {
      nd.y = maxY;
      nd.vy *= 0.5;
    }
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
// Graph Visual Grammar: Category, State, and Capacity
// ---------------------------------------------------------------------------

const CATEGORY_LABELS = {
  metacognitive: 'metacognitive',
  channel: 'channel',
  delegate: 'delegate',
  scheduled: 'scheduled',
  generic: 'generic',
};

const NODE_LABEL_GAP = 24;
const SYSTEM_LABEL_GAP = 20;

// Derive a visual category from loop data. Drives fill tone and sigil.
function getLoopCategoryInfo(loop) {
  const hints = loop.config && loop.config.Hints;
  if (hints && hints.source === 'metacognitive') {
    return { category: 'metacognitive', source: 'hint source=metacognitive' };
  }
  const meta = loop.config && loop.config.Metadata;
  if (meta && meta.category) {
    return {
      category: normalizeVisualCategory(meta.category),
      source: 'metadata.category=' + meta.category,
    };
  }
  if (loop.parent_id) {
    return { category: 'delegate', source: 'parent_id relationship' };
  }
  const name = (loop.name || '').toLowerCase();
  if (/signal|email|mqtt|slack|irc/.test(name)) {
    return { category: 'channel', source: 'name heuristic' };
  }
  if (/sched|cron|timer/.test(name)) {
    return { category: 'scheduled', source: 'name heuristic' };
  }
  return { category: 'generic', source: 'default fallback' };
}

function getLoopCategory(loop) {
  return getLoopCategoryInfo(loop).category;
}

// Category → compact center sigil. The graph uses circles throughout, so
// entity meaning rides on fill, rings, and restrained iconography instead
// of divergent node shapes.
const CATEGORY_SIGILS = {
  metacognitive: 'M',
  channel:       '@',
  delegate:      '↗',
  scheduled:     '◷',
  generic:       '•',
};

function normalizeVisualCategory(category) {
  return CATEGORY_SIGILS[category] ? category : 'generic';
}

// Context-window tiers drive node radius. The steps are intentionally
// weighted for readability instead of being proportional to raw tokens.
const CONTEXT_TIERS = [
  { key: 'ctx-8k',   max: 8192,   label: '8k',   radius: 24 },
  { key: 'ctx-32k',  max: 32768,  label: '32k',  radius: 28 },
  { key: 'ctx-128k', max: 131072, label: '128k', radius: 34 },
  { key: 'ctx-256k', max: 262144, label: '256k', radius: 40 },
  { key: 'ctx-512k', max: Infinity, label: '512k', radius: 46 },
];

// Model name → approximate parameter count (billions).
// Used only as a fallback when we do not know the active context window yet.
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
// This is only used as a fallback capacity estimate when we do not yet have
// a real context window for the loop.
const MIN_NODE_R = 22;
const MAX_NODE_R = 50;
const DEFAULT_NODE_R = 32;

function getModelRadiusFromParams(params) {
  if (params === null) return DEFAULT_NODE_R;

  const minParams = 3;    // floor (smallest model we'd see)
  const maxParams = 700;  // ceiling (largest model we'd see)
  const t = (Math.sqrt(params) - Math.sqrt(minParams)) /
            (Math.sqrt(maxParams) - Math.sqrt(minParams));
  const clamped = Math.max(0, Math.min(1, t));
  return MIN_NODE_R + clamped * (MAX_NODE_R - MIN_NODE_R);
}

function getModelRadius(modelName) {
  return getModelRadiusFromParams(getModelParams(modelName));
}

function getLoopContextWindow(loop) {
  if (!loop) return 0;
  const recent = loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
  const candidates = [
    loop.context_window,
    loop._llmContext && loop._llmContext.context_window,
    loop._llmContext && loop._llmContext.max_context_length,
    recent && recent.context_window,
  ];
  for (const candidate of candidates) {
    const n = Number(candidate);
    if (Number.isFinite(n) && n > 0) return n;
  }
  return 0;
}

function getContextTier(contextWindow) {
  for (const tier of CONTEXT_TIERS) {
    if (contextWindow <= tier.max) return tier;
  }
  return CONTEXT_TIERS[CONTEXT_TIERS.length - 1];
}

function getLoopVisualCapacity(loop) {
  if (loop.handler_only) {
    return {
      radius: DEFAULT_NODE_R,
      label: '',
      key: 'none',
      basis: 'none',
      contextWindow: 0,
    };
  }

  const contextWindow = getLoopContextWindow(loop);
  if (contextWindow > 0) {
    const tier = getContextTier(contextWindow);
    return {
      radius: tier.radius,
      label: tier.label,
      key: tier.key,
      basis: 'context',
      contextWindow,
    };
  }

  const recent = loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
  const modelName = loop._liveModel || loop._lastModel || (recent && recent.model) || '';
  const params = getModelParams(modelName);
  if (params !== null) {
    return {
      radius: getModelRadiusFromParams(params),
      label: params + 'b',
      key: 'model-estimate',
      basis: 'model',
      contextWindow: 0,
    };
  }

  return {
    radius: DEFAULT_NODE_R,
    label: '',
    key: 'none',
    basis: 'none',
    contextWindow: 0,
  };
}

function getLoopVisualState(loop) {
  const isSup = loop._supervisor && loop.state === 'processing';
  const svcDegraded = isServiceDegraded(loop.name);
  if (isSup) return 'supervisor';
  if (svcDegraded && (loop.state === 'sleeping' || loop.state === 'waiting' || loop.state === 'pending')) {
    return 'degraded';
  }
  return loop.state || 'pending';
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

function positionNodeRimBadge(badge, nodeR) {
  if (!badge) return;
  const angle = -40 * Math.PI / 180;
  const orbit = nodeR + 14;
  const x = Math.cos(angle) * orbit;
  const y = Math.sin(angle) * orbit;
  badge.setAttribute('transform', `translate(${x.toFixed(1)} ${y.toFixed(1)})`);
}

function notificationTTL(level) {
  switch (level) {
    case 'error':
      return 0;
    case 'warn':
      return 20000;
    default:
      return 8000;
  }
}

function pruneNotificationSignatures(now) {
  for (const [signature, ts] of recentNotificationSignatures.entries()) {
    if (now - ts > 5 * 60 * 1000) {
      recentNotificationSignatures.delete(signature);
    }
  }
}

function dismissNotification(id) {
  const idx = state.notifications.findIndex((note) => note.id === id);
  if (idx === -1) return;
  state.notifications.splice(idx, 1);
  renderNotifications();
}

function dismissNotificationBySignature(signature) {
  const idx = state.notifications.findIndex((note) => note.signature === signature);
  if (idx === -1) return;
  state.notifications.splice(idx, 1);
  renderNotifications();
}

function scheduleNotificationExpiry(note) {
  if (!note.expiresAt) return;
  const delay = Math.max(0, note.expiresAt - Date.now()) + 50;
  window.setTimeout(() => {
    const current = state.notifications.find((entry) => entry.id === note.id);
    if (!current || current.expiresAt !== note.expiresAt) return;
    dismissNotification(note.id);
  }, delay);
}

function addNotification(opts) {
  if (!notificationStack) return;
  const now = Date.now();
  pruneNotificationSignatures(now);

  const level = opts.level || 'info';
  const signature = opts.signature || `${level}:${opts.title}:${opts.message || ''}`;
  const ttlMs = Object.prototype.hasOwnProperty.call(opts, 'ttlMs')
    ? opts.ttlMs
    : notificationTTL(level);
  const expiresAt = ttlMs > 0 ? now + ttlMs : null;
  const existing = state.notifications.find((note) => note.signature === signature);
  if (existing) {
    existing.level = level;
    existing.title = opts.title;
    existing.message = opts.message || '';
    existing.createdAt = now;
    existing.expiresAt = expiresAt;
    existing.action = opts.action || null;
    existing.actionLabel = opts.actionLabel || '';
    existing.sourceLabel = opts.sourceLabel || '';
    state.notifications = [existing, ...state.notifications.filter((note) => note.id !== existing.id)];
    renderNotifications();
    scheduleNotificationExpiry(existing);
    return;
  }

  const cooldownMs = opts.cooldownMs == null ? 15000 : opts.cooldownMs;
  const lastSeen = recentNotificationSignatures.get(signature);
  if (lastSeen && cooldownMs > 0 && (now - lastSeen) < cooldownMs) return;
  recentNotificationSignatures.set(signature, now);

  const note = {
    id: nextNotificationID++,
    signature,
    level,
    title: opts.title,
    message: opts.message || '',
    sourceLabel: opts.sourceLabel || '',
    action: opts.action || null,
    actionLabel: opts.actionLabel || '',
    createdAt: now,
    expiresAt,
  };
  state.notifications.unshift(note);
  if (state.notifications.length > MAX_NOTIFICATIONS) {
    state.notifications.length = MAX_NOTIFICATIONS;
  }
  renderNotifications();
  scheduleNotificationExpiry(note);
}

function renderNotifications() {
  if (!notificationStack) return;
  notificationStack.innerHTML = '';
  notificationStack.hidden = state.notifications.length === 0;
  for (const note of state.notifications) {
    const card = document.createElement('article');
    card.className = 'notification-card notification-card--' + note.level;

    const header = document.createElement('div');
    header.className = 'notification-card__header';

    const eyebrow = document.createElement('div');
    eyebrow.className = 'notification-card__eyebrow';
    eyebrow.textContent = note.sourceLabel || (note.level === 'error' ? 'Error' : note.level === 'warn' ? 'Warning' : 'Notice');
    header.appendChild(eyebrow);

    const age = document.createElement('time');
    age.className = 'notification-card__age';
    age.textContent = timeAgo(new Date(note.createdAt));
    header.appendChild(age);
    card.appendChild(header);

    const title = document.createElement('h3');
    title.className = 'notification-card__title';
    title.textContent = note.title;
    card.appendChild(title);

    if (note.message) {
      const body = document.createElement('p');
      body.className = 'notification-card__body';
      body.textContent = note.message;
      card.appendChild(body);
    }

    const actions = document.createElement('div');
    actions.className = 'notification-card__actions';
    if (typeof note.action === 'function') {
      const inspect = document.createElement('button');
      inspect.className = 'notification-card__action';
      inspect.type = 'button';
      inspect.textContent = note.actionLabel || 'Inspect';
      inspect.addEventListener('click', () => note.action());
      actions.appendChild(inspect);
    }

    const dismiss = document.createElement('button');
    dismiss.className = 'notification-card__dismiss';
    dismiss.type = 'button';
    dismiss.title = 'Dismiss notification';
    dismiss.setAttribute('aria-label', 'Dismiss notification');
    dismiss.textContent = '×';
    dismiss.addEventListener('click', () => dismissNotification(note.id));
    actions.appendChild(dismiss);

    card.appendChild(actions);
    notificationStack.appendChild(card);
  }
}

function formatSchemaToken(value) {
  if (!value) return '';
  return String(value).replace(/_/g, ' ');
}

function getLoopLatestSnapshot(loop) {
  const history = state.iterationHistory.get(loop.id);
  if (history && history.length > 0) return history[0];
  return loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
}

function describeLoopExecutionMode(loop) {
  if (loop.handler_only) return 'handler';
  if (loop.event_driven) return 'event-driven llm';
  return 'timer-driven llm';
}

function buildLoopEntity(loop) {
  const categoryInfo = getLoopCategoryInfo(loop);
  const latest = getLoopLatestSnapshot(loop);
  const hints = (loop.config && loop.config.Hints) || {};
  const metadata = (loop.config && loop.config.Metadata) || {};
  const configTags = ((loop.config && loop.config.Tags) || []).slice().sort();
  const activeTags = ((loop.active_tags || [])).slice().sort();
  const allTags = Array.from(new Set([...configTags, ...activeTags])).sort();
  const latestModel = loop._liveModel || loop._lastModel || (latest && latest.model) || '';
  const latestRequestID = (latest && latest.request_id) || '';
  const currentConvID = loop._currentConvID || '';
  const recentConvIDs = Array.from(new Set(
    (Array.isArray(loop.recent_conv_ids) ? loop.recent_conv_ids : []).filter(Boolean),
  ));
  const trustZone = metadata.trust_zone || '';
  const subsystem = metadata.subsystem || '';

  return {
    kind: 'loop_run',
    title: loop.name || loop.id,
    state: loop.state || 'pending',
    stateLabel: loop._supervisor && loop.state === 'processing' ? 'supervisor' : (loop.state || 'pending'),
    category: categoryInfo.category,
    categoryLabel: CATEGORY_LABELS[categoryInfo.category] || categoryInfo.category,
    categorySource: categoryInfo.source,
    executionMode: describeLoopExecutionMode(loop),
    relation: loop.parent_id ? 'child' : 'root',
    loopID: loop.id,
    parentID: loop.parent_id || '',
    currentConvID,
    recentConvIDs,
    latestRequestID,
    latestSnapshot: latest,
    latestModel,
    startedAt: loop.started_at || '',
    lastWakeAt: loop.last_wake_at || '',
    iterations: loop.iterations || 0,
    attempts: loop.attempts || 0,
    lastInputTokens: loop.last_input_tokens || 0,
    lastOutputTokens: loop.last_output_tokens || 0,
    totalInputTokens: loop.total_input_tokens || 0,
    totalOutputTokens: loop.total_output_tokens || 0,
    contextWindow: getLoopContextWindow(loop),
    lastError: loop.last_error || '',
    consecutiveErrors: loop.consecutive_errors || 0,
    handlerOnly: !!loop.handler_only,
    eventDriven: !!loop.event_driven,
    hints,
    metadata,
    subsystem,
    trustZone,
    configTags,
    activeTags,
    allTags,
  };
}

function inferConversationFamily(conversationID) {
  if (!conversationID) return 'conversation';
  if (conversationID.startsWith('owu-')) return 'owu';
  if (conversationID.startsWith('signal-')) return 'signal';
  if (conversationID.startsWith('delegate-')) return 'delegate';
  return 'conversation';
}

function formatConversationChannel(channel) {
  switch ((channel || '').toLowerCase()) {
    case 'owu': return 'OWU';
    case 'signal': return 'Signal';
    case 'mqtt': return 'MQTT';
    case 'email': return 'Email';
    default: return channel ? String(channel) : '';
  }
}

function describeConversationFallback(conversationID) {
  switch (inferConversationFamily(conversationID)) {
    case 'owu':
      return 'OWU conversation';
    case 'signal':
      return 'Signal conversation';
    case 'delegate':
      return 'Delegate conversation';
    default:
      return 'Conversation ' + shortID(conversationID);
  }
}

function buildConversationLabel(conversationID, binding, latestSession) {
  if (binding && binding.contact_name) return binding.contact_name;
  const title = (latestSession && latestSession.title ? latestSession.title : '').trim();
  if (title) return title;
  const oneLiner = (((latestSession || {}).metadata || {}).one_liner || '').trim();
  if (oneLiner) return oneLiner;
  const channel = formatConversationChannel(binding && binding.channel);
  if (channel) return channel + ' conversation';
  return describeConversationFallback(conversationID);
}

function buildConversationSummary(conversationID, conversation, sessions, opts = {}) {
  const latestSession = Array.isArray(sessions) && sessions.length > 0 ? sessions[0] : null;
  const metadata = (latestSession && latestSession.metadata) || {};
  const binding = (metadata && metadata.channel_binding) || null;
  const channelLabel = formatConversationChannel((binding && binding.channel) || inferConversationFamily(conversationID));
  const updatedAtRaw = (conversation && conversation.updated_at) ||
    (latestSession && (latestSession.ended_at || latestSession.started_at)) || '';
  const updatedAt = parseTimestamp(updatedAtRaw);
  const sessionTimestampRaw = latestSession ? (latestSession.ended_at || latestSession.started_at || '') : '';
  const sessionTimestamp = parseTimestamp(sessionTimestampRaw);
  const sessionCount = Array.isArray(sessions) ? sessions.length : 0;
  const latestTitle = (latestSession && latestSession.title ? latestSession.title : '').trim();
  const summaryText = (metadata.paragraph || latestSession?.summary || metadata.one_liner || '').trim();
  const label = buildConversationLabel(conversationID, binding, latestSession);
  const active = !!(latestSession && !latestSession.ended_at);
  const metaParts = [];
  if (channelLabel) metaParts.push(channelLabel);
  if (binding && binding.trust_zone) metaParts.push(binding.trust_zone);
  if (updatedAt) metaParts.push('active ' + timeAgo(updatedAt));
  if (Number.isFinite(conversation && conversation.message_count)) metaParts.push(formatNumber(conversation.message_count) + ' msgs');
  if (sessionCount > 0) metaParts.push(formatNumber(sessionCount) + ' sessions');

  return {
    id: conversationID,
    label,
    active,
    error: !!opts.error,
    loading: !!opts.loading,
    metaLine: metaParts.join(' · '),
    channelLabel,
    trustZone: binding && binding.trust_zone ? binding.trust_zone : '',
    contactName: binding && binding.contact_name ? binding.contact_name : '',
    address: binding && binding.address ? binding.address : '',
    linkSource: binding && binding.link_source ? binding.link_source : '',
    updatedAt,
    updatedAtRaw,
    messageCount: Number.isFinite(conversation && conversation.message_count) ? conversation.message_count : null,
    sessionCount,
    latestSessionID: latestSession && latestSession.id ? latestSession.id : '',
    latestSessionTitle: latestTitle,
    latestSessionSummary: summaryText,
    latestSessionAge: sessionTimestamp ? timeAgo(sessionTimestamp) : '',
    latestSessionWhen: sessionTimestamp ? formatTimeShort(sessionTimestamp) : '',
    latestSessionState: latestSession ? (latestSession.ended_at ? 'closed' : 'active') : '',
    fetchedAt: Date.now(),
  };
}

function buildPendingConversationSummary(conversationID) {
  return buildConversationSummary(conversationID, null, [], { loading: true });
}

async function ensureConversationIndex() {
  if (state.conversationIndex.summaries.size > 0 &&
      (Date.now() - state.conversationIndex.fetchedAt) < CONVERSATION_SUMMARY_TTL_MS) {
    return state.conversationIndex.summaries;
  }
  if (state.conversationIndex.loading) return state.conversationIndex.loading;

  state.conversationIndex.loading = fetch('/v1/conversations')
    .then((resp) => {
      if (!resp.ok) throw new Error('conversation index unavailable: ' + resp.status);
      return resp.json();
    })
    .then((body) => {
      const summaries = new Map();
      for (const conv of body.conversations || []) {
        if (conv && conv.id) summaries.set(conv.id, conv);
      }
      state.conversationIndex.summaries = summaries;
      state.conversationIndex.fetchedAt = Date.now();
      return summaries;
    })
    .catch((err) => {
      console.warn('Failed to load conversation index:', err);
      if (state.conversationIndex.summaries.size > 0) {
        return state.conversationIndex.summaries;
      }
      throw err;
    })
    .finally(() => {
      state.conversationIndex.loading = null;
    });

  return state.conversationIndex.loading;
}

function refreshSelectedLoopInspector() {
  if (state.selected && state.selected !== '__system__' && state.loops.has(state.selected)) {
    renderDetail();
  }
}

function ensureConversationSummary(conversationID) {
  if (!conversationID) return;
  const cached = state.conversationDetails.get(conversationID);
  if (cached && (Date.now() - cached.fetchedAt) < CONVERSATION_SUMMARY_TTL_MS) {
    return;
  }
  if (state.conversationLoads.has(conversationID)) return;

  const load = Promise.allSettled([
    ensureConversationIndex(),
    fetch('/v1/archive/sessions?conversation_id=' + encodeURIComponent(conversationID) + '&limit=' + CONVERSATION_SESSION_LIMIT)
      .then((resp) => {
        if (!resp.ok) throw new Error('archive sessions unavailable: ' + resp.status);
        return resp.json();
      })
      .then((body) => Array.isArray(body.sessions) ? body.sessions : []),
  ]).then(([conversationResult, sessionsResult]) => {
    const index = conversationResult.status === 'fulfilled' ? conversationResult.value : new Map();
    const sessions = sessionsResult.status === 'fulfilled' ? sessionsResult.value : [];
    const conversation = index.get(conversationID) || null;
    const detail = buildConversationSummary(conversationID, conversation, sessions, {
      error: conversationResult.status !== 'fulfilled' && sessionsResult.status !== 'fulfilled',
    });
    state.conversationDetails.set(conversationID, detail);
    refreshSelectedLoopInspector();
  }).catch((err) => {
    console.warn('Failed to load conversation summary:', conversationID, err);
    state.conversationDetails.set(conversationID, buildConversationSummary(conversationID, null, [], { error: true }));
    refreshSelectedLoopInspector();
  }).finally(() => {
    state.conversationLoads.delete(conversationID);
  });

  state.conversationLoads.set(conversationID, load);
}

function buildSystemEntity(sys) {
  const health = (sys && sys.health) || {};
  const serviceKeys = Object.keys(health);
  const readyCount = serviceKeys.filter((key) => health[key] && health[key].ready).length;
  const registry = (sys && sys.model_registry) || {};
  const routerStats = (sys && sys.router_stats) || {};
  const resources = Array.isArray(registry.resources) ? registry.resources : [];
  const deployments = Array.isArray(registry.deployments) ? registry.deployments : [];
  const version = (sys && sys.version) || {};
  const rootLoops = Array.from(state.loops.values()).filter((loop) => !loop.parent_id).length;
  const childLoops = Math.max(0, state.loops.size - rootLoops);
  const routingMode = registry.local_first ? 'local-first' : 'policy';
  return {
    kind: 'runtime_anchor',
    title: 'Core',
    state: sys.status || 'unknown',
    uptime: sys.uptime || '',
    version: version.version || '',
    commit: version.git_commit || '',
    goVersion: version.go_version || '',
    arch: version.os && version.arch ? `${version.os}/${version.arch}` : '',
    serviceCount: serviceKeys.length,
    readyCount,
    liveLoopCount: state.loops.size,
    rootLoopCount: rootLoops,
    childLoopCount: childLoops,
    totalRequests: Number(routerStats.total_requests || 0),
    routingMode,
    defaultModel: registry.default_model || '',
    registryGeneration: Number(registry.generation || 0),
    resourceCount: resources.length,
    deploymentCount: deployments.length,
    routerStats,
    registry,
    health,
  };
}

function buildLoopNodeTitle(loop, capacity) {
  const entity = buildLoopEntity(loop);
  const parts = [entity.title];
  parts.push('Kind: ' + entity.kind);
  parts.push('State: ' + formatSchemaToken(entity.stateLabel));
  parts.push('Visual: ' + entity.categoryLabel + ' (' + entity.categorySource + ')');
  parts.push('Execution: ' + entity.executionMode);
  if (entity.parentID) {
    parts.push('Parent: ' + entity.parentID);
  } else {
    parts.push('Anchor: core');
  }
  if (entity.currentConvID) {
    parts.push('Conversation: ' + entity.currentConvID);
  }
  if (entity.trustZone) {
    parts.push('Trust: ' + entity.trustZone);
  }
  if (capacity.basis === 'context' && capacity.contextWindow > 0) {
    parts.push('Context: ' + formatNumber(capacity.contextWindow));
  } else if (capacity.basis === 'model') {
    parts.push('Capacity: est. ' + capacity.label);
  }
  if (entity.latestModel) {
    parts.push('Model: ' + entity.latestModel);
  }
  return parts.join('\n');
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
const detailEntity = $('#detail-entity');
const emptyState = $('#empty-state');
const logEmpty = $('#log-empty');
const logScroll = $('#log-scroll');
const logBody = $('#log-body');
const notificationStack = $('#notification-stack');
const legendPanel = $('#legend-panel');
const legendBackdrop = $('#legend-backdrop');
const legendToggleBtn = $('#toggle-legend');
const legendCloseBtn = $('#legend-close');

const DASHBOARD_PREFS_KEY = 'thane.dashboard.ui.v1';
const DEFAULT_DASHBOARD_PREFS = {
  inspectorVisible: true,
  logsVisible: false,
};

function loadDashboardPrefs() {
  try {
    const raw = window.localStorage.getItem(DASHBOARD_PREFS_KEY);
    if (!raw) return { ...DEFAULT_DASHBOARD_PREFS };
    const parsed = JSON.parse(raw);
    return {
      inspectorVisible: parsed.inspectorVisible !== false,
      logsVisible: parsed.logsVisible === true,
    };
  } catch (_) {
    return { ...DEFAULT_DASHBOARD_PREFS };
  }
}

function saveDashboardPrefs(prefs) {
  try {
    window.localStorage.setItem(DASHBOARD_PREFS_KEY, JSON.stringify({
      inspectorVisible: prefs.inspectorVisible !== false,
      logsVisible: prefs.logsVisible === true,
    }));
  } catch (_) {
    // Ignore storage failures; UI still functions with in-memory state.
  }
}

const dashboardPrefs = loadDashboardPrefs();
let nextNotificationID = 1;
const recentNotificationSignatures = new Map();
let connectionWasDegraded = false;
let lastDetailSelectionKey = null;

function currentDetailSelectionKey() {
  if (typeof activeRequestID !== 'undefined' && activeRequestID) return 'request:' + activeRequestID;
  if (state.selected === '__system__') return 'system';
  if (state.selected) return 'loop:' + state.selected;
  return null;
}

function withPreservedDetailScroll(renderFn) {
  const panel = document.getElementById('detail-panel');
  const selectionKey = currentDetailSelectionKey();
  const preserve = !!panel && selectionKey !== null && selectionKey === lastDetailSelectionKey;
  const previousTop = preserve ? panel.scrollTop : 0;

  renderFn();

  if (panel) {
    if (preserve) {
      requestAnimationFrame(() => {
        const maxTop = Math.max(0, panel.scrollHeight - panel.clientHeight);
        panel.scrollTop = Math.min(previousTop, maxTop);
      });
    } else {
      panel.scrollTop = 0;
    }
  }

  lastDetailSelectionKey = selectionKey;
}

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
    if (!connectionWasDegraded) {
      connectionWasDegraded = true;
      addNotification({
        level: 'warn',
        sourceLabel: 'Connection',
        title: 'Live event stream disconnected',
        message: 'Dashboard updates may be stale until the event stream reconnects.',
        action: () => selectSystem(),
        actionLabel: 'Inspect core',
        signature: 'sse-disconnected',
        cooldownMs: 0,
      });
    }
    // EventSource auto-reconnects; the snapshot on reconnect
    // will restore full state.
  };

  eventSource.onopen = () => {
    setConnState('connected');
    if (connectionWasDegraded) {
      connectionWasDegraded = false;
      dismissNotificationBySignature('sse-disconnected');
      addNotification({
        level: 'info',
        sourceLabel: 'Connection',
        title: 'Live event stream restored',
        message: 'Dashboard updates are live again.',
        signature: 'sse-restored',
        ttlMs: 6000,
        cooldownMs: 0,
      });
    }
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

  // Capability tools change active_tags — refetch loop status so
  // the dashboard shows the updated capability state immediately.
  if (result && result.capabilityChanged) {
    fetchLoops();
  }

  if (evt.kind === 'loop_error') {
    const message = (evt.data && evt.data.error) || loop.last_error || 'Loop iteration failed.';
    addNotification({
      level: 'error',
      sourceLabel: 'Loop',
      title: (loop.name || loopId) + ' failed',
      message: truncate(message, 220),
      action: () => selectLoop(loopId),
      actionLabel: 'Inspect loop',
      signature: `loop-error:${loopId}:${message}`,
      cooldownMs: 30000,
    });
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

      if (evt.data.error || evt.data.exhausted) {
        const targetLoopID = entry ? syntheticId : (evt.data.parent_loop_id || '');
        addNotification({
          level: evt.data.error ? 'error' : 'warn',
          sourceLabel: 'Delegate',
          title: evt.data.error ? 'Background delegate failed' : 'Background delegate exhausted',
          message: truncate(evt.data.error || evt.data.exhaust_reason || entry?._delegateTask || 'Delegate did not complete successfully.', 220),
          action: targetLoopID
            ? () => {
              if (state.loops.has(targetLoopID)) selectLoop(targetLoopID);
              else selectSystem();
            }
            : () => selectSystem(),
          actionLabel: targetLoopID ? 'Inspect loop' : 'Inspect core',
          signature: `delegate-failure:${did}:${evt.data.error || evt.data.exhaust_reason || ''}`,
          cooldownMs: 30000,
        });
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
    const previous = state.system;
    const resp = await fetch('/api/system');
    if (resp.status === 404) {
      state.system = null;
      return;
    }
    const next = await resp.json();
    state.system = next;
    // Derive start time so we can tick uptime locally.
    if (state.system.uptime) {
      const uptimeMs = parseDuration(state.system.uptime);
      systemStartTime = Date.now() - uptimeMs;
    }
    emitSystemNotifications(previous, next);
    renderAll();
  } catch (err) {
    console.warn('Failed to fetch system status:', err);
  }
}

function emitSystemNotifications(previous, next) {
  if (!previous || !next) return;
  const prevHealth = previous.health || {};
  const nextHealth = next.health || {};
  const names = new Set([...Object.keys(prevHealth), ...Object.keys(nextHealth)]);

  for (const name of names) {
    const prevReady = prevHealth[name] ? prevHealth[name].ready : undefined;
    const nextReady = nextHealth[name] ? nextHealth[name].ready : undefined;
    if (prevReady === true && nextReady === false) {
      addNotification({
        level: 'warn',
        sourceLabel: 'Service',
        title: `${name} degraded`,
        message: truncate((nextHealth[name] && nextHealth[name].last_error) || `${name} became unavailable.`, 220),
        action: () => selectSystem(),
        actionLabel: 'Inspect core',
        signature: `service-degraded:${name}:${(nextHealth[name] && nextHealth[name].last_error) || ''}`,
        cooldownMs: 0,
      });
    } else if (prevReady === false && nextReady === true) {
      addNotification({
        level: 'info',
        sourceLabel: 'Service',
        title: `${name} recovered`,
        message: `${name} is ready again.`,
        action: () => selectSystem(),
        actionLabel: 'Inspect core',
        signature: `service-recovered:${name}`,
        ttlMs: 7000,
        cooldownMs: 0,
      });
    }
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
  const rect = refreshCanvasViewport() || canvas.getBoundingClientRect();
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

  // Write positions immediately for newly created nodes and edges so they do
  // not flash at the SVG origin or briefly appear disconnected while waiting
  // for the next animation tick.
  updateNodePositions();
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
  const category = normalizeVisualCategory(getLoopCategory(loop));
  const capacity = getLoopVisualCapacity(loop);
  const nodeR = capacity.radius;
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
      showContextMenu(e.clientX, e.clientY, buildLoopContextMenu(loop));
    });

    // Inner group for enter/exit scale animation (children drawn at origin).
    const inner = createSVG('g', { class: 'node-inner' });

    // Native SVG tooltip — instant, no delay.
    const title = createSVG('title', {});
    title.textContent = buildLoopNodeTitle(loop, capacity);
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

    const selectionRing = createSVG('circle', {
      class: 'selection-ring',
      r: nodeR + 18,
      fill: 'none',
    });

    // Glow ring (always a circle regardless of shape).
    const ring = createSVG('circle', {
      class: 'node-ring node-ring--pending',
      r: ringR,
      fill: 'none',
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
      'font-size': Math.round(nodeR * 0.5),
    });
    icon.textContent = CATEGORY_SIGILS[category] || CATEGORY_SIGILS.generic;

    // Supervisor ring (larger circle outside the node).
    const supDot = createSVG('circle', {
      class: 'supervisor-dot',
      r: nodeR + 10,
    });

    const rimBadge = createSVG('g', {
      class: 'node-rim-badge',
    });
    const rimBadgeBody = createSVG('circle', {
      class: 'node-rim-badge__body',
      r: 11,
    });
    const rimBadgeText = createSVG('text', {
      class: 'node-rim-badge__text',
      'text-anchor': 'middle',
      'dominant-baseline': 'central',
    });
    rimBadgeText.textContent = capacity.label;
    rimBadge.appendChild(rimBadgeBody);
    rimBadge.appendChild(rimBadgeText);
    positionNodeRimBadge(rimBadge, nodeR);

    // Label.
    const label = createSVG('text', {
      class: 'node-label',
      y: nodeR + NODE_LABEL_GAP,
    });
    // Child loops show just the suffix after "/" since the parent
    // line makes the hierarchy clear (e.g., "signal/Alice" → "Alice").
    const displayName = loop.name || loop.id.slice(0, 8);
    const slash = loop.parent_id ? displayName.indexOf('/') : -1;
    label.textContent = slash > 0 ? displayName.slice(slash + 1) : displayName;

    inner.appendChild(selectionRing);
    inner.appendChild(ring);
    inner.appendChild(sleepRing);
    inner.appendChild(shapeEl);
    inner.appendChild(icon);
    inner.appendChild(supDot);
    inner.appendChild(rimBadge);
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

  const title = group.querySelector('title');
  if (title) title.textContent = buildLoopNodeTitle(loop, capacity);

  // Dynamic resizing — update shape, rings, label when model changes.
  const prevR = parseFloat(group.dataset.nodeR) || DEFAULT_NODE_R;
  if (Math.abs(nodeR - prevR) > 0.5) {
    group.dataset.nodeR = nodeR;
    const shapeEl = group.querySelector('.node-shape');
    updateNodeShape(shapeEl, category, nodeR);

    // Update dependent radii.
    const newRingR = nodeR + 12;
    group.querySelector('.selection-ring').setAttribute('r', nodeR + 18);
    group.querySelector('.node-ring').setAttribute('r', newRingR);
    const sleepRing = group.querySelector('.sleep-ring');
    const newSleepR = nodeR;
    sleepRing.setAttribute('r', newSleepR);
    const circ = 2 * Math.PI * newSleepR;
    sleepRing.setAttribute('stroke-dasharray', circ);
    group.querySelector('.supervisor-dot').setAttribute('r', nodeR + 10);
    positionNodeRimBadge(group.querySelector('.node-rim-badge'), nodeR);
    const iconEl = group.querySelector('.node-icon');
    if (iconEl) iconEl.setAttribute('font-size', Math.round(nodeR * 0.5));
    group.querySelector('.node-label').setAttribute('y', nodeR + NODE_LABEL_GAP);
  }
  group.dataset.nodeR = nodeR;
  group.setAttribute('data-category', category);

  const shapeEl = group.querySelector('.node-shape');
  const iconEl = group.querySelector('.node-icon');
  const visualState = getLoopVisualState(loop);
  shapeEl.setAttribute('class', 'node-shape node-shape--category-' + category + ' node-shape--activity-' + visualState);
  iconEl.textContent = CATEGORY_SIGILS[category] || CATEGORY_SIGILS.generic;
  iconEl.setAttribute('class', 'node-icon node-icon--' + category);

  const ring = group.querySelector('.node-ring');
  ring.setAttribute('class', 'node-ring node-ring--' + visualState);

  // Ring thickness represents context utilization percentage.
  const ctxPct = (capacity.contextWindow > 0 && loop.last_input_tokens > 0)
    ? Math.min(1, loop.last_input_tokens / capacity.contextWindow)
    : 0;
  const minStroke = 2;
  const maxStroke = 8;
  const strokeW = ctxPct > 0
    ? minStroke + ctxPct * (maxStroke - minStroke)
    : minStroke;
  ring.setAttribute('stroke-width', strokeW.toFixed(1));

  const rimBadge = group.querySelector('.node-rim-badge');
  rimBadge.setAttribute('class', 'node-rim-badge node-rim-badge--' + capacity.basis);
  const rimBadgeText = rimBadge.querySelector('.node-rim-badge__text');
  rimBadgeText.textContent = capacity.label;
  positionNodeRimBadge(rimBadge, nodeR);

  // Supervisor ring (outer pulsing ring around node).
  const supDot = group.querySelector('.supervisor-dot');
  const isSup = visualState === 'supervisor';
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
      showContextMenu(e.clientX, e.clientY, buildSystemContextMenu(sys));
    });

    const title = createSVG('title', {});
    title.textContent = buildSystemNodeTitle(sys);
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
      y: s / 2 + SYSTEM_LABEL_GAP,
    });
    label.textContent = 'core';
    group.appendChild(label);

    canvasWorld.appendChild(group);
  }

  // Update health-based fill.
  const rect = group.querySelector('.system-rect');
  const cls = sys.status === 'healthy'
    ? 'system-rect system-rect--healthy'
    : 'system-rect system-rect--degraded';
  rect.setAttribute('class', cls);
  const title = group.querySelector('title');
  if (title) title.textContent = buildSystemNodeTitle(sys);

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
  renderSystemEntityDetail(sys);
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

function makeSchemaCard(title, meta) {
  const card = document.createElement('section');
  card.className = 'detail-card schema-card';

  const header = document.createElement('div');
  header.className = 'schema-card__header';

  const titleEl = document.createElement('h3');
  titleEl.className = 'schema-card__title';
  titleEl.textContent = title;
  header.appendChild(titleEl);

  if (meta) {
    const metaEl = document.createElement('span');
    metaEl.className = 'schema-card__meta';
    metaEl.textContent = meta;
    header.appendChild(metaEl);
  }

  card.appendChild(header);

  const body = document.createElement('div');
  body.className = 'schema-card__body';
  card.appendChild(body);
  return { card, body, header };
}

function appendSchemaRow(body, label, value, opts = {}) {
  if (value === null || value === undefined || value === '') return;
  const row = document.createElement('div');
  row.className = 'schema-row' + (opts.multiline ? ' schema-row--multiline' : '');

  const labelEl = document.createElement('div');
  labelEl.className = 'schema-row__label';
  labelEl.textContent = label;
  row.appendChild(labelEl);

  const valueEl = document.createElement('div');
  valueEl.className = 'schema-row__value';
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
    valueEl.textContent = String(value);
  } else {
    valueEl.appendChild(value);
  }
  row.appendChild(valueEl);
  body.appendChild(row);
}

function makeSchemaIDList(ids, opts = {}) {
  const wrap = document.createElement('div');
  wrap.className = 'schema-chip-list schema-chip-list--ids';
  const limit = Number.isFinite(opts.maxVisible) ? Math.max(0, Number(opts.maxVisible)) : ids.length;
  const visible = ids.slice(0, limit);
  for (const id of visible) {
    wrap.appendChild(opts.request ? makeRequestChip(id) : makeIDChip(id));
  }
  if (limit < ids.length) {
    const more = document.createElement('span');
    more.className = 'id-chip id-chip--muted';
    more.textContent = '+' + (ids.length - limit);
    wrap.appendChild(more);
  }
  return wrap;
}

function makeSchemaChipList(values, className = 'tag-chip') {
  const wrap = document.createElement('div');
  wrap.className = 'schema-chip-list';
  for (const value of values) {
    const chip = document.createElement('span');
    chip.className = className;
    chip.textContent = value;
    wrap.appendChild(chip);
  }
  return wrap;
}

function makeConversationFact(label, value) {
  if (!value) return null;
  const row = document.createElement('div');
  row.className = 'conversation-summary__fact';

  const labelEl = document.createElement('span');
  labelEl.className = 'conversation-summary__fact-label';
  labelEl.textContent = label;
  row.appendChild(labelEl);

  const valueEl = document.createElement('span');
  valueEl.className = 'conversation-summary__fact-value';
  valueEl.textContent = value;
  row.appendChild(valueEl);

  return row;
}

function makeConversationSummaryEntry(summary, opts = {}) {
  const details = document.createElement('details');
  details.className = 'conversation-summary' + (opts.current ? ' conversation-summary--current' : '');

  const summaryEl = document.createElement('summary');
  summaryEl.className = 'conversation-summary__summary';

  const copy = document.createElement('div');
  copy.className = 'conversation-summary__copy';

  const titleRow = document.createElement('div');
  titleRow.className = 'conversation-summary__title-row';

  const title = document.createElement('span');
  title.className = 'conversation-summary__title';
  title.textContent = summary.label;
  titleRow.appendChild(title);

  if (opts.current) {
    const badge = document.createElement('span');
    badge.className = 'conversation-summary__badge conversation-summary__badge--current';
    badge.textContent = 'current';
    titleRow.appendChild(badge);
  }
  if (summary.active) {
    const badge = document.createElement('span');
    badge.className = 'conversation-summary__badge conversation-summary__badge--active';
    badge.textContent = 'active';
    titleRow.appendChild(badge);
  }
  if (summary.loading) {
    const badge = document.createElement('span');
    badge.className = 'conversation-summary__badge';
    badge.textContent = 'loading';
    titleRow.appendChild(badge);
  }
  copy.appendChild(titleRow);

  const meta = document.createElement('div');
  meta.className = 'conversation-summary__meta';
  meta.textContent = summary.error
    ? 'Conversation details unavailable'
    : (summary.metaLine || 'No archived session detail yet');
  copy.appendChild(meta);
  summaryEl.appendChild(copy);

  const chevron = document.createElement('span');
  chevron.className = 'conversation-summary__chevron';
  chevron.textContent = 'Details';
  summaryEl.appendChild(chevron);

  details.appendChild(summaryEl);

  const body = document.createElement('div');
  body.className = 'conversation-summary__body';

  if (summary.latestSessionSummary) {
    const desc = document.createElement('p');
    desc.className = 'conversation-summary__description';
    desc.textContent = summary.latestSessionSummary;
    body.appendChild(desc);
  }

  const facts = document.createElement('div');
  facts.className = 'conversation-summary__facts';
  const factEls = [
    makeConversationFact('channel', summary.channelLabel),
    makeConversationFact('contact', summary.contactName),
    makeConversationFact('address', summary.address),
    makeConversationFact('trust', summary.trustZone),
    makeConversationFact('session title', summary.latestSessionTitle && summary.latestSessionTitle !== summary.label
      ? summary.latestSessionTitle
      : ''),
    makeConversationFact('last active', summary.updatedAt ? timeAgo(summary.updatedAt) : ''),
    makeConversationFact('latest session', summary.latestSessionState
      ? summary.latestSessionState + (summary.latestSessionAge ? ' · ' + summary.latestSessionAge : '')
      : ''),
    makeConversationFact('history', summary.sessionCount ? formatNumber(summary.sessionCount) + ' sessions' : ''),
    makeConversationFact('working set', Number.isFinite(summary.messageCount) ? formatNumber(summary.messageCount) + ' messages' : ''),
  ].filter(Boolean);
  for (const fact of factEls) facts.appendChild(fact);
  if (factEls.length > 0) body.appendChild(facts);

  const ids = document.createElement('div');
  ids.className = 'conversation-summary__ids';

  const convBlock = document.createElement('div');
  convBlock.className = 'conversation-summary__id-block';
  const convLabel = document.createElement('span');
  convLabel.className = 'conversation-summary__id-label';
  convLabel.textContent = 'conversation';
  convBlock.appendChild(convLabel);
  convBlock.appendChild(makeIDChip(summary.id));
  ids.appendChild(convBlock);

  if (summary.latestSessionID) {
    const sessBlock = document.createElement('div');
    sessBlock.className = 'conversation-summary__id-block';
    const sessLabel = document.createElement('span');
    sessLabel.className = 'conversation-summary__id-label';
    sessLabel.textContent = 'latest session';
    sessBlock.appendChild(sessLabel);
    sessBlock.appendChild(makeIDChip(summary.latestSessionID));
    ids.appendChild(sessBlock);
  }

  body.appendChild(ids);
  details.appendChild(body);
  return details;
}

function makeConversationSummaryList(ids, opts = {}) {
  const uniqueIDs = Array.from(new Set((ids || []).filter(Boolean)));
  const list = document.createElement('div');
  list.className = 'conversation-summary-list';

  for (const conversationID of uniqueIDs) {
    ensureConversationSummary(conversationID);
    const summary = state.conversationDetails.get(conversationID) || buildPendingConversationSummary(conversationID);
    list.appendChild(makeConversationSummaryEntry(summary, {
      current: !!(opts.currentIDs && opts.currentIDs.has(conversationID)),
    }));
  }

  return list;
}

function makeRequestChip(requestID) {
  const chip = document.createElement('span');
  chip.className = 'id-chip id-chip--responsive' + (typeof window.onRequestChipClick === 'function' ? ' schema-request-chip' : '');
  chip.title = (typeof window.onRequestChipClick === 'function'
    ? 'Click to inspect request · Shift+click to copy\n'
    : 'Click to copy request ID\n') + requestID;

  const txt = document.createElement('span');
  txt.className = 'id-chip-text';
  txt.textContent = 'req:' + requestID;
  chip.appendChild(txt);

  chip.addEventListener('click', (e) => {
    e.stopPropagation();
    if (!e.shiftKey && typeof window.onRequestChipClick === 'function') {
      window.onRequestChipClick(requestID);
      return;
    }
    navigator.clipboard.writeText(requestID).then(() => {
      chip.classList.add('id-chip--copied');
      setTimeout(() => chip.classList.remove('id-chip--copied'), 1200);
    });
  });
  return chip;
}

function objectEntriesExcluding(obj, excludedKeys) {
  const excluded = new Set(excludedKeys || []);
  return Object.entries(obj || {}).filter(([key, value]) => !excluded.has(key) && value !== '');
}

function renderSystemEntityDetail(sys) {
  const entity = buildSystemEntity(sys);
  detailEntity.innerHTML = '';

  const hero = document.createElement('section');
  hero.className = 'detail-card schema-card schema-card--hero';
  hero.innerHTML = `
    <div class="schema-hero">
      <div class="schema-hero__copy">
        <div class="schema-kind">${entity.kind}</div>
        <h2 class="detail-name">${escapeHTML(entity.title)}</h2>
        <div class="schema-subtitle">
          Root anchor for <strong>${formatNumber(entity.liveLoopCount)} live loops</strong>
          across ${formatNumber(entity.serviceCount)} runtime services
        </div>
      </div>
      <div class="schema-badge-list">
        <span class="state-badge state-badge--${escapeHTML(entity.state === 'healthy' ? 'sleeping' : 'error')}">${escapeHTML(formatSchemaToken(entity.state))}</span>
        <span class="schema-badge">${escapeHTML(entity.routingMode)}</span>
        <span class="schema-badge">${escapeHTML(`${entity.readyCount}/${entity.serviceCount} ready`)}</span>
      </div>
    </div>
  `;
  detailEntity.appendChild(hero);

  const identity = makeSchemaCard('Identity', 'runtime snapshot and build metadata');
  appendSchemaRow(identity.body, 'anchor kind', entity.kind);
  appendSchemaRow(identity.body, 'status', formatSchemaToken(entity.state));
  appendSchemaRow(identity.body, 'uptime', entity.uptime);
  appendSchemaRow(identity.body, 'version', entity.version);
  if (entity.commit) {
    appendSchemaRow(identity.body, 'commit', makeSchemaIDList([entity.commit], { maxVisible: 1 }));
  }
  appendSchemaRow(identity.body, 'go', entity.goVersion);
  appendSchemaRow(identity.body, 'arch', entity.arch);
  detailEntity.appendChild(identity.card);

  const topology = makeSchemaCard('Topology', 'how the runtime is currently shaped');
  appendSchemaRow(topology.body, 'live loops', formatNumber(entity.liveLoopCount));
  appendSchemaRow(topology.body, 'root loops', formatNumber(entity.rootLoopCount));
  appendSchemaRow(topology.body, 'child loops', formatNumber(entity.childLoopCount));
  appendSchemaRow(topology.body, 'services ready', `${formatNumber(entity.readyCount)} / ${formatNumber(entity.serviceCount)}`);
  appendSchemaRow(topology.body, 'router requests', formatNumber(entity.totalRequests));
  appendSchemaRow(topology.body, 'routing mode', entity.routingMode);
  appendSchemaRow(topology.body, 'default model', entity.defaultModel);
  appendSchemaRow(topology.body, 'registry generation', formatNumber(entity.registryGeneration));
  appendSchemaRow(topology.body, 'model resources', formatNumber(entity.resourceCount));
  appendSchemaRow(topology.body, 'deployments', formatNumber(entity.deploymentCount));
  detailEntity.appendChild(topology.card);

  const services = makeSchemaCard('Services', 'runtime health and readiness');
  const servicesEl = document.createElement('div');
  servicesEl.className = 'system-services';
  renderSystemServices(servicesEl, entity.health);
  services.body.appendChild(servicesEl);
  detailEntity.appendChild(services.card);

  const registryCard = makeSchemaCard('Model Registry');
  const registryMeta = document.createElement('span');
  registryMeta.className = 'schema-card__meta';
  registryCard.header.appendChild(registryMeta);

  const registrySummary = document.createElement('div');
  registrySummary.className = 'system-summary-grid';
  registryCard.body.appendChild(registrySummary);

  const resourcesWrap = document.createElement('div');
  resourcesWrap.className = 'schema-subsection';
  resourcesWrap.innerHTML = '<h4 class="schema-subsection__title">Resources</h4>';
  const registryResources = document.createElement('div');
  registryResources.className = 'system-list';
  resourcesWrap.appendChild(registryResources);
  registryCard.body.appendChild(resourcesWrap);

  const deploymentsWrap = document.createElement('div');
  deploymentsWrap.className = 'schema-subsection';
  deploymentsWrap.innerHTML = '<h4 class="schema-subsection__title">Deployments</h4>';
  const registryDeployments = document.createElement('div');
  registryDeployments.className = 'system-list';
  deploymentsWrap.appendChild(registryDeployments);
  registryCard.body.appendChild(deploymentsWrap);

  renderModelRegistry(
    registrySummary,
    registryResources,
    registryDeployments,
    registryMeta,
    entity.registry,
    entity.routerStats,
  );
  detailEntity.appendChild(registryCard.card);

  updateSystemUptime();
}

function renderLoopEntityDetail(loop) {
  const entity = buildLoopEntity(loop);
  detailEntity.innerHTML = '';

  const hero = document.createElement('section');
  hero.className = 'detail-card schema-card schema-card--hero';
  hero.innerHTML = `
    <div class="schema-hero">
      <div class="schema-hero__copy">
        <div class="schema-kind">${entity.kind}</div>
        <h2 class="detail-name">${escapeHTML(entity.title)}</h2>
        <div class="schema-subtitle">
          Visual category <strong>${escapeHTML(entity.categoryLabel)}</strong>
          via ${escapeHTML(entity.categorySource)}
        </div>
      </div>
      <div class="schema-badge-list">
        <span class="state-badge state-badge--${escapeHTML(entity.stateLabel === 'supervisor' ? 'supervisor' : entity.state)}">${escapeHTML(formatSchemaToken(entity.stateLabel))}</span>
        <span class="schema-badge">${escapeHTML(entity.executionMode)}</span>
        <span class="schema-badge">${escapeHTML(entity.relation)}</span>
      </div>
    </div>
  `;
  detailEntity.appendChild(hero);

  const identity = makeSchemaCard('Identity', 'what this node is');
  appendSchemaRow(identity.body, 'loop_id', makeIDChip(entity.loopID));
  appendSchemaRow(identity.body, 'entity kind', entity.kind);
  appendSchemaRow(identity.body, 'execution mode', entity.executionMode);
  appendSchemaRow(identity.body, 'visual category', entity.categoryLabel);
  appendSchemaRow(identity.body, 'classification source', entity.categorySource);
  if (entity.subsystem) appendSchemaRow(identity.body, 'subsystem', entity.subsystem);
  detailEntity.appendChild(identity.card);

  const relationships = makeSchemaCard('Relationships', 'how this run is connected');
  if (entity.parentID) {
    appendSchemaRow(relationships.body, 'parent loop', makeIDChip(entity.parentID));
  } else {
    appendSchemaRow(relationships.body, 'root anchor', 'core');
  }
  const recentHistoryIDs = entity.recentConvIDs.filter((id) => id !== entity.currentConvID);
  if (entity.currentConvID) {
    appendSchemaRow(
      relationships.body,
      'conversation',
      makeConversationSummaryList([entity.currentConvID], { currentIDs: new Set([entity.currentConvID]) }),
      { multiline: true },
    );
  }
  if (recentHistoryIDs.length > 0) {
    appendSchemaRow(
      relationships.body,
      'conversation history',
      makeConversationSummaryList(recentHistoryIDs),
      { multiline: true },
    );
  }
  if (entity.latestRequestID) {
    appendSchemaRow(relationships.body, 'latest request', makeSchemaIDList([entity.latestRequestID], { request: true }));
  }
  detailEntity.appendChild(relationships.card);

  const execution = makeSchemaCard('Execution', 'current execution state');
  appendSchemaRow(execution.body, 'state', formatSchemaToken(entity.stateLabel));
  appendSchemaRow(execution.body, 'started', entity.startedAt ? timeAgo(new Date(entity.startedAt)) : '');
  appendSchemaRow(execution.body, 'last wake', entity.lastWakeAt ? timeAgo(new Date(entity.lastWakeAt)) : '');
  appendSchemaRow(execution.body, 'iterations', formatNumber(entity.iterations));
  appendSchemaRow(execution.body, 'attempts', formatNumber(entity.attempts));
  appendSchemaRow(execution.body, 'consecutive errors', entity.consecutiveErrors ? formatNumber(entity.consecutiveErrors) : '');
  appendSchemaRow(execution.body, 'latest model', entity.latestModel);
  appendSchemaRow(execution.body, 'context window', entity.contextWindow ? formatNumber(entity.contextWindow) : '');
  appendSchemaRow(execution.body, 'last io', entity.lastInputTokens || entity.lastOutputTokens ? `${formatTokens(entity.lastInputTokens)} in · ${formatTokens(entity.lastOutputTokens)} out` : '');
  appendSchemaRow(execution.body, 'total io', entity.totalInputTokens || entity.totalOutputTokens ? `${formatTokens(entity.totalInputTokens)} in · ${formatTokens(entity.totalOutputTokens)} out` : '');
  if (entity.lastError) {
    const err = document.createElement('div');
    err.className = 'system-item__error';
    err.textContent = entity.lastError;
    appendSchemaRow(execution.body, 'last error', err, { multiline: true });
  }
  detailEntity.appendChild(execution.card);

  const profile = makeSchemaCard('Profile', 'hints, metadata, and capability context');
  if (entity.hints.mission) appendSchemaRow(profile.body, 'mission', entity.hints.mission);
  if (entity.hints.source) appendSchemaRow(profile.body, 'source hint', entity.hints.source);
  if (entity.hints.delegation_gating) appendSchemaRow(profile.body, 'delegation gating', entity.hints.delegation_gating);
  if (entity.trustZone) appendSchemaRow(profile.body, 'trust zone', entity.trustZone);

  const extraHints = objectEntriesExcluding(entity.hints, ['mission', 'source', 'delegation_gating']);
  if (extraHints.length > 0) {
    const wrap = document.createElement('div');
    wrap.className = 'schema-map';
    for (const [key, value] of extraHints) {
      const item = document.createElement('span');
      item.className = 'schema-map__item';
      item.textContent = key + '=' + value;
      wrap.appendChild(item);
    }
    appendSchemaRow(profile.body, 'extra hints', wrap, { multiline: true });
  }

  const extraMetadata = objectEntriesExcluding(entity.metadata, ['category', 'subsystem', 'trust_zone', 'delegate_task', 'delegate_guidance', 'delegate_profile']);
  if (extraMetadata.length > 0) {
    const wrap = document.createElement('div');
    wrap.className = 'schema-map';
    for (const [key, value] of extraMetadata) {
      const item = document.createElement('span');
      item.className = 'schema-map__item';
      item.textContent = key + '=' + value;
      wrap.appendChild(item);
    }
    appendSchemaRow(profile.body, 'metadata', wrap, { multiline: true });
  }

  if (entity.configTags.length > 0) {
    appendSchemaRow(profile.body, 'configured tags', makeSchemaChipList(entity.configTags, 'tag-chip tag-chip--muted'));
  }
  if (entity.activeTags.length > 0) {
    appendSchemaRow(profile.body, 'active tags', makeSchemaChipList(entity.activeTags, 'tag-chip tag-chip--active'));
  }

  const delegateTask = entity.metadata.delegate_task || '';
  const delegateGuidance = entity.metadata.delegate_guidance || '';
  const delegateProfile = entity.metadata.delegate_profile || '';
  if (delegateTask || delegateGuidance || delegateProfile) {
    if (delegateProfile) appendSchemaRow(profile.body, 'delegate profile', delegateProfile);
    if (delegateTask) appendSchemaRow(profile.body, 'delegate task', delegateTask, { multiline: true });
    if (delegateGuidance) appendSchemaRow(profile.body, 'delegate guidance', delegateGuidance, { multiline: true });
  }
  detailEntity.appendChild(profile.card);

  const activity = makeSchemaCard('Activity', 'recent loop execution');
  const aggregates = document.createElement('div');
  aggregates.className = 'detail-aggregates';
  renderAggregates(loop, aggregates);
  activity.body.appendChild(aggregates);

  const timeline = document.createElement('div');
  timeline.className = 'iter-timeline';
  renderTimeline(loop, timeline, state.iterationHistory.get(loop.id) || [], loop.id, state.sleepTimers);
  activity.body.appendChild(timeline);
  detailEntity.appendChild(activity.card);
}

function buildLoopContextMenu(loop) {
  const entity = buildLoopEntity(loop);
  const items = [
    { label: 'kind: ' + entity.kind, disabled: true },
    { label: 'visual: ' + entity.categoryLabel + ' · ' + entity.categorySource, disabled: true },
    { label: 'relation: ' + entity.relation + (entity.parentID ? ' · parent ' + shortID(entity.parentID) : ' · anchored to core'), disabled: true },
    entity.currentConvID ? { label: 'conversation: ' + shortID(entity.currentConvID), disabled: true } : null,
    entity.trustZone ? { label: 'trust: ' + entity.trustZone, disabled: true } : null,
    { separator: true },
  ].filter(Boolean);
  if (!loop.id.startsWith('delegate-')) {
    items.push({ label: 'Open in window', action: () => openDetailWindow('loop', loop.id) });
  }
  if (entity.parentID && state.loops.has(entity.parentID)) {
    items.push({ label: 'Select parent loop', action: () => selectLoop(entity.parentID) });
  } else if (!entity.parentID && state.system) {
    items.push({ label: 'Select core anchor', action: () => selectSystem() });
  }
  if (entity.latestRequestID && typeof window.onRequestChipClick === 'function') {
    items.push({ label: 'Open latest request', action: () => showRequestDetail(entity.latestRequestID) });
  }
  items.push({ separator: true });
  items.push({ label: 'Copy loop ID', action: () => navigator.clipboard.writeText(entity.loopID) });
  if (entity.parentID) {
    items.push({ label: 'Copy parent loop ID', action: () => navigator.clipboard.writeText(entity.parentID) });
  }
  if (entity.currentConvID) {
    items.push({ label: 'Copy current conversation ID', action: () => navigator.clipboard.writeText(entity.currentConvID) });
  }
  if (entity.latestRequestID) {
    items.push({ label: 'Copy latest request ID', action: () => navigator.clipboard.writeText(entity.latestRequestID) });
  }
  return items;
}

function buildSystemNodeTitle(sys) {
  const entity = buildSystemEntity(sys || state.system || {});
  return [
    entity.title,
    'Kind: ' + entity.kind,
    'Status: ' + formatSchemaToken(entity.state),
    'Topology: ' + formatNumber(entity.liveLoopCount) + ' loops · ' + formatNumber(entity.rootLoopCount) + ' roots',
    'Services: ' + entity.readyCount + '/' + entity.serviceCount + ' ready',
    'Routing: ' + entity.routingMode + (entity.defaultModel ? ' (' + entity.defaultModel + ')' : ''),
  ].join('\n');
}

function buildSystemContextMenu(sys) {
  const entity = buildSystemEntity(sys || state.system || {});
  return [
    { label: 'kind: ' + entity.kind, disabled: true },
    { label: 'status: ' + formatSchemaToken(entity.state), disabled: true },
    { label: 'topology: ' + formatNumber(entity.liveLoopCount) + ' loops · ' + formatNumber(entity.rootLoopCount) + ' roots', disabled: true },
    { label: 'services: ' + entity.readyCount + '/' + entity.serviceCount + ' ready', disabled: true },
    { label: 'routing: ' + entity.routingMode + (entity.defaultModel ? ' · ' + entity.defaultModel : ''), disabled: true },
    { separator: true },
    { label: 'Open in window', action: () => openDetailWindow('system') },
    { label: 'Inspect core', action: () => selectSystem() },
  ];
}

function renderDetail() {
  withPreservedDetailScroll(() => {
    const isSystem = state.selected === '__system__';
    const isLoop = state.selected && state.loops.has(state.selected);

    if (isSystem && state.system) {
      detailPlaceholder.hidden = true;
      detailContent.hidden = false;
      renderSystemDetail();
      return;
    }

    if (!isLoop) {
      detailPlaceholder.hidden = false;
      detailContent.hidden = true;
      return;
    }

    detailPlaceholder.hidden = true;
    detailContent.hidden = false;

    const loop = state.loops.get(state.selected);
    renderLoopEntityDetail(loop);
  });
}

// renderAggregates, renderTimeline, clearLiveTelemetry are in shared.js.

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

function showLogHint(message) {
  logBody.innerHTML = '';
  logScroll.hidden = true;
  logEmpty.hidden = false;
  logEmpty.querySelector('p').textContent = message;
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

function selectLoop(loopId) {
  if (state.selected === loopId) {
    // Deselect.
    state.selected = null;
    showLogHint('Select a loop node to inspect its diagnostic tail');
  } else {
    state.selected = loopId;
    fetchLogs(loopId);
  }
  renderAll();
}

function selectSystem() {
  if (state.selected === '__system__') {
    state.selected = null;
    showLogHint('Select a loop node to inspect its diagnostic tail');
  } else {
    state.selected = '__system__';
    showLogHint('Logs in the dashboard are node-scoped. Select a loop to inspect its diagnostic tail.');
  }
  renderAll();
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
    showLogHint('Logs in the dashboard are node-scoped. Select a loop to inspect its diagnostic tail.');
  } else if (state.selected) {
    fetchLogs(state.selected);
  } else {
    showLogHint('Select a loop node to inspect its diagnostic tail');
  }
}

$('#log-level').addEventListener('change', refreshLogs);
$('#log-refresh').addEventListener('click', refreshLogs);

// ---------------------------------------------------------------------------
// Panel Toggle
// ---------------------------------------------------------------------------

function toggleInspector() {
  setInspectorVisible(document.getElementById('detail-panel').hidden);
}

function toggleLogs() {
  setLogsVisible(document.getElementById('log-panel').hidden);
}

function setInspectorVisible(visible) {
  const panel = document.getElementById('detail-panel');
  const handle = document.getElementById('resize-v');
  const btn = document.getElementById('toggle-inspector');
  panel.hidden = !visible;
  handle.hidden = !visible;
  btn.classList.toggle('toggle-btn--active', visible);
  dashboardPrefs.inspectorVisible = visible;
  saveDashboardPrefs(dashboardPrefs);
}

function setLogsVisible(visible) {
  const panel = document.getElementById('log-panel');
  const handle = document.getElementById('resize-h');
  const btn = document.getElementById('toggle-logs');
  panel.hidden = !visible;
  handle.hidden = !visible;
  btn.classList.toggle('toggle-btn--active', visible);
  dashboardPrefs.logsVisible = visible;
  saveDashboardPrefs(dashboardPrefs);
}

function setLegendVisible(visible) {
  if (!legendPanel || !legendBackdrop || !legendToggleBtn) return;
  legendPanel.hidden = !visible;
  legendBackdrop.hidden = !visible;
  legendToggleBtn.classList.toggle('toggle-btn--active', visible);
}

function toggleLegend() {
  if (!legendPanel) return;
  setLegendVisible(legendPanel.hidden);
}

$('#toggle-inspector').addEventListener('click', toggleInspector);
$('#toggle-logs').addEventListener('click', toggleLogs);
legendToggleBtn?.addEventListener('click', toggleLegend);
legendCloseBtn?.addEventListener('click', () => setLegendVisible(false));
legendBackdrop?.addEventListener('click', () => setLegendVisible(false));

setInspectorVisible(dashboardPrefs.inspectorVisible);
setLogsVisible(dashboardPrefs.logsVisible);

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
    if (item.disabled) {
      li.className = 'context-menu-item context-menu-item--disabled';
    } else {
      li.addEventListener('click', () => {
        hideContextMenu();
        item.action();
      });
    }
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
    ? 'Core'
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
    case '?':
      toggleLegend();
      break;
    case 'escape':
      if (activeRequestID) {
        closeRequestDetail();
      }
      hideContextMenu();
      setLegendVisible(false);
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

// Keep the graph responsive to real canvas size changes, including panel
// toggles and drag-resizing, not just top-level window resizes.
(function initCanvasViewportObserver() {
  const canvasPanel = document.getElementById('canvas-panel');
  const syncViewport = () => {
    refreshCanvasViewport();
    updateNodePositions();
  };

  if (typeof ResizeObserver !== 'undefined' && canvasPanel) {
    const observer = new ResizeObserver(() => syncViewport());
    observer.observe(canvasPanel);
  }

  window.addEventListener('resize', syncViewport);
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
