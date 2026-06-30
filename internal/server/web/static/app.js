// Cognition Engine — vanilla JS, native ES modules, no build step.
// Connects to the SSE event stream and renders loop nodes as SVG.

import * as defaultClient from './data/client.js';
import { subscribe as defaultSubscribeLoopEvents } from './data/events.js';
import { createLoopStore } from './data/loops.js';

// Data seams. Default to the real /v1 client and SSE stream, but createGraph()
// can swap them — a fixture client for tests, or a native bridge for a host
// (e.g. a SwiftUI/WebKit embed). Held in `let` so the override applies before
// the boot in createGraph() and is seen by every reader through closure.
let api = defaultClient;
let subscribeLoopEvents = defaultSubscribeLoopEvents;

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
  system: null,           // system status object from /v1/system
  prevIterations: new Map(), // id -> last known iteration count (for flash detection)
  prevErrors: new Map(),     // id -> last known error string (for shake detection)
  knownLoopIds: new Set(),   // ids we've rendered before (for enter animation)
  canvasRect: null,          // last observed canvas viewport for responsive graph reflow
  conversationDetails: new Map(), // conversation_id -> derived dashboard summary
  conversationLoads: new Map(),   // conversation_id -> in-flight loader promise
  loopDefs: new Map(),            // loop name -> { status, def, at } cached /v1/loop-definitions
  specExpanded: new Set(),        // "name::field" keys whose spec text is expanded inline
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
  // A homage to Gource's force model (acaudwell/Gource, src/dirnode.cpp): one
  // force accumulator per node holding gravity-to-ring + overlap-only
  // repulsion, integrated once with no stored velocity. Angle is emergent —
  // repulsion spaces siblings around the ring rather than assigning precomputed
  // slots — so the layout negotiates a single equilibrium and settles to rest.
  springRestLength:   164,   // baseline root-ring radius
  childRestLength:    112,   // baseline child-ring radius
  // Gravity pulls each child onto its family ring around its parent's current
  // position: force = gravityStrength * (dist - ringRadius), signed so it
  // reels a node in when it drifts out and pushes it off when it sinks inward.
  gravityStrength:    0.06,
  // Per-edge "rim spring" for CHILD (non-root) families. Instead of pulling a
  // child onto a sqrt-area family RING (which grows with family size + spread and
  // made container-child edges long), pull it to a SHORT rest = the two nodes'
  // own extents + a small gap — the Gource rim, where a child sits tangent on its
  // parent. Rest depends only on the two endpoints, so the edge strains short
  // regardless of sibling count, subtree depth, or viewport.spread. Stronger than
  // gravity so it wins the RADIUS while sibling/overlap repulsion spread the
  // children ANGULARLY around the rim.
  edgeSpringStrength: 0.12,
  edgeGap:            26,
  // Sibling angular spread: a decaying (1/d^2) central repulsion among
  // same-parent siblings (scaled by ring^2) that reaches across gaps, so a
  // clustered or cold-start-piled family spreads around its whole ring —
  // overlap-only repulsion is too short-range to detangle a pile. It decays
  // with distance, so an evenly-spaced ring feels almost none and still settles
  // (a constant-magnitude spread never vanishes and orbits forever).
  // siblingRepelMax caps the per-pair force near contact.
  siblingRepelStrength: 1.2,
  siblingRepelMax:    6,
  // Outward bias (a conservative analog of Gource's parent-normal fan,
  // dirnode.cpp:675): a constant push on non-root children radially away from
  // the FIXED core, so a subtree fans onto the far side of its parent instead of
  // swinging core-facing and wedging into the tier-1 shell. Anchored to the
  // fixed core (not the moving parent) it is torque-free about the core — it
  // can't drive a slow cloud rotation the way a moving-apex pull does. Gentle:
  // it must lose to overlap separation and the cold-start sibling-spread.
  outwardFanStrength: 1,
  // Root-ring aspect stretch — fills a non-square canvas (partially; a radial
  // layout resists extreme aspects) and pins the layout's orientation so a
  // circular cloud can't slowly spin.
  orbitAspectStrength: 0.85,
  // Puddle walls (disabled): edge forces only push in, but the puddle bulge
  // needs the cloud to expand out — gravity-to-ring fights it, so the cloud
  // compresses/churns instead of filling. Kept at 0; aspect ellipse does the job.
  wallStrength: 0,
  puddleExtent: 1.18,
  pinnedAnchorStrength: 0.16,
  pinnedAnchorDamping: 0.72,
  // Overlap repulsion fires ONLY when node footprints intersect; the force is
  // the penetration depth, so deep overlaps separate hard and settled nodes
  // feel no long-range field (Gource applyForceDir: if(distance2>0) return).
  overlapRepulsionStrength: 0.22,
  parentChildRepulsionMultiplier: 1.55,
  // Area-conserving ring growth (Gource calcRadius): family ring scales as
  // sqrt(summed child footprint area) * padding, so it stays compact and grows
  // slowly with member count. orbitSubtreeReserve is how much of a child's
  // descendant footprint it reserves as its own ring room.
  orbitAreaPadding:   1.1,
  orbitSubtreeReserve: 0.2,
  channelChildSpacingMultiplier: 1.22,
  nodeEnterDurationMs: 700,
  nodeEnterExtentBoost: 0.34,
  // Overdamped integrator (Gource has no stored node velocity): position moves
  // by force * stepScale each frame, capped at maxStep. No momentum means the
  // layout descends to a true fixed point and stops dead — no orbiting or slosh.
  stepScale:          3,
  maxStep:            18,
  collisionPadding:   24,
};

function buildLoopBranchLoads() {
  const registryCoreID = getRegistryCoreID();
  const childrenByParent = new Map();
  for (const loop of state.loops.values()) {
    const parentID = getEffectiveParentID(loop, registryCoreID);
    if (!parentID) continue;
    if (!childrenByParent.has(parentID)) childrenByParent.set(parentID, []);
    childrenByParent.get(parentID).push(loop.id);
  }

  const cache = new Map();

  function measure(loopID) {
    if (cache.has(loopID)) return cache.get(loopID);
    const ownExtent = getPhysicsNodeExtent(loopID);
    const childIDs = childrenByParent.get(loopID) || [];
    let descendants = 0;
    let branchExtent = ownExtent;
    for (const childID of childIDs) {
      const childLoad = measure(childID);
      descendants += 1 + childLoad.descendants;
      branchExtent += childLoad.branchExtent;
    }
    const load = { ownExtent, descendants, branchExtent };
    cache.set(loopID, load);
    return load;
  }

  for (const loop of state.loops.values()) {
    measure(loop.id);
  }

  return cache;
}

function getLoopBranchFootprint(load) {
  if (!load) return 0;
  return Math.max(0, load.branchExtent - load.ownExtent);
}

function getLoopMetadata(loop) {
  return (loop && loop.config && loop.config.Metadata) || {};
}

function isChannelConversationRelation(parentLoop, childLoop) {
  if (!parentLoop || !childLoop) return false;
  const parentMeta = getLoopMetadata(parentLoop);
  const childMeta = getLoopMetadata(childLoop);
  return parentMeta.category === 'channel' &&
    childMeta.category === 'channel' &&
    !!parentMeta.subsystem &&
    parentMeta.subsystem === childMeta.subsystem &&
    childLoop.parent_id === parentLoop.id;
}

function getPairSpacingMultiplier(loopA, loopB) {
  if (isChannelConversationRelation(loopA, loopB) || isChannelConversationRelation(loopB, loopA)) {
    return physics.channelChildSpacingMultiplier;
  }
  return 1;
}

function isParentChildRelation(loopA, loopB) {
  if (!loopA || !loopB) return false;
  return loopA.parent_id === loopB.id || loopB.parent_id === loopA.id;
}

// Orbit ring radius for a family of siblings, using Gource's area-conserving
// growth (calcRadius, dirnode.cpp:574): the disc that holds every child's
// footprint, so the ring grows as sqrt(member count) instead of linearly and
// stays compact. Each child reserves its own extent plus a fraction of its
// subtree footprint, so subtree-heavy children claim more ring room without the
// quadratic blow-up of squaring the full branch extent.
function getOrbitFamilyRadius(parentID, loops, branchLoads) {
  if (!loops || loops.length === 0) return parentID ? physics.childRestLength : physics.springRestLength;
  const baseRadius = parentID ? physics.childRestLength : physics.springRestLength;
  let area = 0;
  for (const loop of loops) {
    const extent = getPhysicsNodeExtent(loop.id);
    const footprint = getLoopBranchFootprint(branchLoads.get(loop.id));
    const reserve = extent + footprint * physics.orbitSubtreeReserve;
    area += reserve * reserve;
  }
  let radius = Math.max(baseRadius, Math.sqrt(area) * physics.orbitAreaPadding);
  if (parentID) {
    // Never let a child ring sink inside the parent's own footprint.
    const parentExtent = getPhysicsNodeExtent(parentID);
    const maxChildExtent = loops.reduce((max, loop) => Math.max(max, getPhysicsNodeExtent(loop.id)), 0);
    radius = Math.max(radius, parentExtent + maxChildExtent + physics.collisionPadding);
  }
  return radius;
}

function getGraphMotionScale(nodeCount) {
  if (nodeCount <= 8) return 1;
  return Math.max(0.52, 1 - ((nodeCount - 8) * 0.03));
}

function getNowMs() {
  return (typeof performance !== 'undefined' && typeof performance.now === 'function')
    ? performance.now()
    : Date.now();
}

function clampUnit(value) {
  return Math.max(0, Math.min(1, value));
}

function easeOutBack(t) {
  const x = clampUnit(t) - 1;
  const c1 = 1.70158;
  const c3 = c1 + 1;
  return 1 + (c3 * x * x * x) + (c1 * x * x);
}

function getNodeEnterInfluence(id) {
  const nd = physics.nodes.get(id);
  if (!nd || !nd.createdAt) return 0;
  const elapsed = getNowMs() - nd.createdAt;
  const duration = physics.nodeEnterDurationMs || 0;
  if (duration <= 0 || elapsed >= duration) return 0;
  const progress = clampUnit(elapsed / duration);
  return Math.max(0, 1 - easeOutBack(progress));
}

function updatePinnedAnchorPositions() {
  for (const nd of physics.nodes.values()) {
    if (!nd || !nd.pinned) continue;
    if (!Number.isFinite(nd.targetX) || !Number.isFinite(nd.targetY)) continue;

    const dx = nd.targetX - nd.x;
    const dy = nd.targetY - nd.y;
    if (Math.abs(dx) < 0.001 && Math.abs(dy) < 0.001 && Math.abs(nd.vx || 0) < 0.001 && Math.abs(nd.vy || 0) < 0.001) {
      nd.x = nd.targetX;
      nd.y = nd.targetY;
      nd.vx = 0;
      nd.vy = 0;
      continue;
    }

    nd.vx = ((nd.vx || 0) + dx * physics.pinnedAnchorStrength) * physics.pinnedAnchorDamping;
    nd.vy = ((nd.vy || 0) + dy * physics.pinnedAnchorStrength) * physics.pinnedAnchorDamping;
    nd.x += nd.vx;
    nd.y += nd.vy;

    if (Math.abs(nd.targetX - nd.x) < 0.08 && Math.abs(nd.targetY - nd.y) < 0.08 && Math.abs(nd.vx) < 0.08 && Math.abs(nd.vy) < 0.08) {
      nd.x = nd.targetX;
      nd.y = nd.targetY;
      nd.vx = 0;
      nd.vy = 0;
    }
  }
}

// isRegistryCoreLoop reports whether a loop in state.loops is the
// well-known structural-root container the runtime auto-creates as
// the singleton "core" loop. The dashboard already represents that
// role via the pinned __system__ pseudo-node (rendered as a labeled
// square), so we hide the loop's own graph node to avoid two nodes
// both labeled "core" — one square, one round, both labeled "core,"
// with a single connection between them and every other loop hanging
// off the round one.
//
// The loop itself stays in state.loops so detail panels and API
// consumers continue to see its data.
//
// Predicate matches the backend's Loop.IsCore: name == "core" and
// the config Operation is "container." Falling back on name alone
// would misfire for unrelated loops a user might name "core."
function isRegistryCoreLoop(loop) {
  if (!loop) return false;
  if (loop.name !== 'core') return false;
  const op = loop.config && loop.config.Operation;
  return op === 'container';
}

// isContainerLoop reports whether a loop is a non-executing container — a
// semantic grouping node (config.Operation === "container") that holds child
// loops but never runs iterations itself. Distinct from isRegistryCoreLoop,
// which is the name-bound special case for the singleton "core" container
// (collapsed into __system__). Containers are rendered as small, dimmed nodes
// with no activity affordances; see projectLoopCharacteristics + style.css.
function isContainerLoop(loop) {
  return !!(loop && loop.config && loop.config.Operation === 'container');
}

// getRegistryCoreID returns the id of the collapsed registry-core loop
// when one is present and the __system__ node is active, otherwise null.
// Returns null whenever state.system is absent so that, before system
// status arrives, the registry core lays out as an ordinary root (the
// same boot-order gating syncPhysicsNodes/renderNodes use).
//
// Each layout builder (buildLoopBranchLoads, buildOrbitRings) calls this
// once at its top — as do physicsStep and syncPhysicsNodes — then threads
// the result into [getEffectiveParentID] so the per-loop parent lookup
// stays O(1). The
// scan here is O(loops): run a small constant number of times per tick
// over a Map of at most a few dozen loops, it's negligible and not
// worth threading a shared value through every builder's signature.
function getRegistryCoreID() {
  if (!state.system) return null;
  for (const loop of state.loops.values()) {
    if (isRegistryCoreLoop(loop)) return loop.id;
  }
  return null;
}

// getEffectiveParentID is the layout-layer counterpart to the edge
// re-rooting in renderLinkingLines: a loop that named the collapsed
// registry core as its parent is treated as a root (parent = none),
// because its visual slot — node, drawn edge, and spring anchor — is
// the centered __system__ node, not the hidden core's phantom
// position on the root ring. Every layout builder must agree on this; if
// one re-roots a child to center but another files it under the core,
// gravity-to-ring would pull the node toward two different centers and the
// layout never settles. Pass the precomputed registryCoreID from
// [getRegistryCoreID] to avoid an O(n) scan per loop.
function getEffectiveParentID(loop, registryCoreID) {
  if (!loop || !loop.parent_id) return null;
  if (registryCoreID && loop.parent_id === registryCoreID) return null;
  return loop.parent_id;
}

// Ensure physics.nodes matches the current set of loops + system node.
// New nodes spawn at their parent position (or center with jitter).
function syncPhysicsNodes(cx, cy) {
  // System node — always pinned at center.
  if (state.system) {
    const sys = physics.nodes.get('__system__');
    if (sys) {
      sys.targetX = cx;
      sys.targetY = cy;
      if (!Number.isFinite(sys.x) || !Number.isFinite(sys.y)) {
        sys.x = cx;
        sys.y = cy;
      }
    } else {
      physics.nodes.set('__system__', { x: cx, y: cy, vx: 0, vy: 0, pinned: true, targetX: cx, targetY: cy });
    }
  } else {
    physics.nodes.delete('__system__');
  }

  const branchLoads = buildLoopBranchLoads();
  const registryCoreID = getRegistryCoreID();
  const { ringRadius } = buildOrbitRings(branchLoads, registryCoreID);
  const nowMs = getNowMs();

  // Loop nodes. Roots spawn on the macro ring around the core at a free angle;
  // child nodes spawn AT their parent's short rim, biased to the away-from-core
  // side, so they land roughly where the rim spring settles them (no long reel-in
  // from the old big ring) and fan outward instead of overlapping their siblings.
  for (const loop of state.loops.values()) {
    // Skip the registry's structural-root core loop — it shares its
    // visual slot with __system__ and rendering both produces the
    // duplicate "core" node operators see in the graph. See
    // [isRegistryCoreLoop] for the predicate's reasoning.
    if (state.system && isRegistryCoreLoop(loop)) continue;
    if (physics.nodes.has(loop.id)) continue;
    const parentID = getEffectiveParentID(loop, registryCoreID);
    const hasLiveParent = !!(parentID && physics.nodes.has(parentID));
    const anchor = hasLiveParent ? physics.nodes.get(parentID) : physics.nodes.get('__system__');
    let sx, sy;
    if (anchor) {
      let spawnR, angle;
      if (hasLiveParent) {
        // Child: seed AT the short rim (the rim-spring rest) on the away-from-core
        // side of its parent, so it lands roughly where it settles — no long
        // reel-in from the old macro ring, and an outward seed is the cheapest
        // crossing preventer (subtree fans away from the grandparent).
        spawnR = getPhysicsNodeExtent(parentID) + getPhysicsNodeExtent(loop.id) + physics.edgeGap;
        const ax = anchor.x - cx, ay = anchor.y - cy; // core -> parent direction
        const base = (ax || ay) ? Math.atan2(ay, ax) : Math.random() * Math.PI * 2;
        angle = base + (Math.random() - 0.5) * 1.2;   // outward, with a little spread
      } else {
        // Root: seed on the macro ring around the core at a free angle.
        spawnR = (ringRadius.get('__root__') || physics.springRestLength) + 6 + Math.random() * 10;
        angle = Math.random() * Math.PI * 2;
      }
      sx = anchor.x + Math.cos(angle) * spawnR;
      sy = anchor.y + Math.sin(angle) * spawnR;
    } else {
      sx = cx + (Math.random() * 40 - 20);
      sy = cy + (Math.random() * 40 - 20);
    }
    physics.nodes.set(loop.id, { x: sx, y: sy, vx: 0, vy: 0, pinned: false, createdAt: nowMs });
  }

  // Remove physics nodes for loops that no longer exist (and aren't system).
  // Exiting nodes stay until their DOM animationend handler cleans them up.
  // Also handles the boot-order race: an initial render may complete before
  // /v1/system returns, in which case the registry core gets a physics
  // node like any other loop. When system status later arrives, suppress
  // the registry core's physics node so it stops participating in layout.
  for (const id of physics.nodes.keys()) {
    if (id === '__system__') continue;
    const loop = state.loops.get(id);
    if (state.system && loop && isRegistryCoreLoop(loop)) {
      physics.nodes.delete(id);
      continue;
    }
    if (!state.loops.has(id) && !canvasWorld.querySelector(`[data-loop-id="${id}"].loop-node--exiting`)) {
      physics.nodes.delete(id);
    }
  }

}

function cloneRect(rect) {
  return rect
    ? { width: rect.width, height: rect.height, cx: rect.cx, cy: rect.cy,
        pxWidth: rect.pxWidth, pxHeight: rect.pxHeight }
    : null;
}

function getLayoutViewportRect() {
  // Physics runs in raw canvas-pixel space, independent of the camera: pan and
  // zoom are a purely visual transform (applyViewportTransform), and the
  // auto-fit camera reframes the graph by moving the camera, never the layout.
  // Decoupling the layout frame from the camera is what keeps auto-fit free of
  // a feedback loop with the pinned core.
  const rect = canvas.getBoundingClientRect();
  return {
    width: rect.width,
    height: rect.height,
    cx: rect.width / 2,
    cy: rect.height / 2,
    pxWidth: rect.width,
    pxHeight: rect.height,
  };
}

function getCanvasRectSnapshot() {
  return cloneRect(getLayoutViewportRect());
}

function isCanvasRectChanged(prevRect, nextRect) {
  if (!prevRect || !nextRect) return true;
  // Compare the raw (un-zoomed) pixel size so a zoom isn't mistaken for a
  // physical canvas resize — only a real panel/window resize reflows physics.
  return Math.abs(prevRect.pxWidth - nextRect.pxWidth) > 0.5 ||
    Math.abs(prevRect.pxHeight - nextRect.pxHeight) > 0.5;
}

function reflowPhysicsNodes(prevRect, nextRect) {
  if (!prevRect || !nextRect || prevRect.width <= 0 || prevRect.height <= 0 ||
      nextRect.width <= 0 || nextRect.height <= 0) {
    return;
  }

  const prevCx = Number.isFinite(prevRect.cx) ? prevRect.cx : (prevRect.width / 2);
  const prevCy = Number.isFinite(prevRect.cy) ? prevRect.cy : (prevRect.height / 2);
  const nextCx = Number.isFinite(nextRect.cx) ? nextRect.cx : (nextRect.width / 2);
  const nextCy = Number.isFinite(nextRect.cy) ? nextRect.cy : (nextRect.height / 2);
  const shiftX = nextCx - prevCx;
  const shiftY = nextCy - prevCy;

  // Translate the whole cloud (don't rescale it) so it rigidly follows the
  // recentered core and keeps its natural non-overlapping size. Rescaling to
  // the new viewport would squeeze a large graph back into overlap.
  for (const [id, nd] of physics.nodes) {
    if (id === '__system__') {
      nd.targetX = nextCx;
      nd.targetY = nextCy;
      continue;
    }
    nd.x += shiftX;
    nd.y += shiftY;
  }
}

function refreshCanvasViewport() {
  const nextRect = getCanvasRectSnapshot();
  if (!nextRect) return null;
  // While the graph is hidden (a surface view such as Processes is showing),
  // the canvas reports a zero-size rect. Keep the last-good viewport instead of
  // recomputing from it: otherwise the pinned center (cx,cy) collapses to
  // ~(0,0) and the __system__ node's target is yanked to the corner, drifting
  // there until the next real render snaps it back to center.
  if (nextRect.pxWidth <= 0 || nextRect.pxHeight <= 0) {
    return state.canvasRect;
  }
  if (isCanvasRectChanged(state.canvasRect, nextRect)) {
    const prevRect = state.canvasRect;
    reflowPhysicsNodes(prevRect, nextRect);
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
  const base = getLoopVisualCapacity(loop).radius + 14;
  const enterInfluence = getNodeEnterInfluence(id);
  return base * (1 + enterInfluence * physics.nodeEnterExtentBoost);
}

// Compute the orbit ring each family of siblings shares, plus the family
// grouping itself. Unlike the old explicit-slot layout, angle is NOT assigned
// here — it emerges from the sibling-spread force in physicsStep. Roots (loops
// with no effective parent) are grouped under '__root__' and orbit the pinned
// core center. Only loops with a live physics node participate.
function buildOrbitRings(branchLoads, registryCoreID) {
  const groups = new Map(); // parentKey -> [loop, ...]
  for (const loop of state.loops.values()) {
    if (!physics.nodes.has(loop.id)) continue;
    const parentID = getEffectiveParentID(loop, registryCoreID);
    const key = (parentID && physics.nodes.has(parentID)) ? parentID : '__root__';
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(loop);
  }
  const ringRadius = new Map();
  for (const [key, loops] of groups) {
    ringRadius.set(key, getOrbitFamilyRadius(key === '__root__' ? null : key, loops, branchLoads));
  }
  return { groups, ringRadius };
}

// Run one physics simulation step in the Gource force model: a single force
// accumulator per node holding gravity-to-ring + sibling-spread + overlap
// repulsion, integrated once. There is no post-integration position write, so
// attraction and repulsion negotiate one equilibrium instead of a slot
// teleport re-overlapping nodes every frame.
function physicsStep(cx, cy, vw, vh) {
  const P = physics;
  const nodes = Array.from(P.nodes.values());
  const ids = Array.from(P.nodes.keys());
  const n = nodes.length;
  if (n === 0) return;
  const branchLoads = buildLoopBranchLoads();
  const registryCoreID = getRegistryCoreID();
  const { groups, ringRadius } = buildOrbitRings(branchLoads, registryCoreID);
  const motionScale = getGraphMotionScale(n);

  // Aspect shaping: stretch the ROOT ring into an ellipse matching the canvas
  // aspect ratio. Two wins from one move: (1) the graph puddle-fills a non-square
  // canvas (a tall canvas gets a taller cloud) instead of leaving whitespace, and
  // (2) the ellipse has a preferred orientation, which breaks the circular
  // layout's rotational symmetry — a circle is free to drift/spin forever (no
  // restoring force for rotation), an ellipse is pinned to the canvas axes.
  const aspect = (vw > 0 && vh > 0) ? (vw / vh) : 1;
  const rootShapeX = 1 + ((Math.sqrt(aspect) - 1) * P.orbitAspectStrength);
  const rootShapeY = 1 + (((1 / Math.sqrt(aspect)) - 1) * P.orbitAspectStrength);

  // Reset forces.
  for (const nd of nodes) { nd.fx = 0; nd.fy = 0; }

  // 1. Gravity-to-ring + 2. sibling spread, per family. Gravity reels each
  // child onto its family's ring around the parent's CURRENT position (radius
  // set, angle free); the decaying sibling repulsion then distributes the family
  // around that ring and fills gaps so a clustered or cold-start-piled family
  // detangles, while vanishing at even spacing so the layout settles to rest.
  const sysNode = P.nodes.get('__system__');
  const corePx = sysNode ? sysNode.x : cx;
  const corePy = sysNode ? sysNode.y : cy;
  for (const [key, loops] of groups) {
    const isRoot = key === '__root__';
    // Macro ring — ROOT families only: the tier-1 cloud rings the core at a
    // sqrt-area radius, aspect-stretched and scaled by viewport.spread so the
    // wheel fans tier-1 apart. Child families no longer use the ring for their
    // edge length (they use the short rim spring below), so a big family stops
    // lengthening every edge and `spread` no longer stretches intra-family edges.
    const ring = (ringRadius.get(key) || (isRoot ? P.springRestLength : P.childRestLength)) * viewport.spread;
    // Aspect ellipse pins the macro orientation; only roots take it now (child
    // families hug their parent rim isotropically).
    const shapeX = isRoot ? rootShapeX : 1;
    const shapeY = isRoot ? rootShapeY : 1;
    // Roots orbit the pinned core node's LIVE position rather than the raw
    // viewport center it eases toward, so they stay attached to the core node
    // through the pinned-anchor easing (e.g. across a resize). Non-roots orbit
    // their parent node's current position. (cx, cy) is the boot-time fallback
    // before __system__ exists.
    const parentNode = isRoot ? sysNode : P.nodes.get(key);
    const pcx = parentNode ? parentNode.x : cx;
    const pcy = parentNode ? parentNode.y : cy;

    // The outward bias applies to non-root families (children of a container or
    // deeper); roots distribute freely around the core via sibling-spread.
    const fanActive = !isRoot;

    // Sibling-spread reach = the radius siblings actually sit at, so the 1/d^2
    // spread is calibrated to the achievable spacing and vanishes at even spread
    // (a reach tied to the big macro ring never decays on the short child arc and
    // would buzz). Roots: the macro ring. Child families: the short rim radius.
    let repelRadius = ring;
    if (!isRoot) {
      let maxChildExtent = 0;
      for (const loop of loops) {
        maxChildExtent = Math.max(maxChildExtent, getPhysicsNodeExtent(loop.id));
      }
      repelRadius = getPhysicsNodeExtent(key) + maxChildExtent + P.edgeGap;
    }

    for (const loop of loops) {
      const nd = P.nodes.get(loop.id);
      if (!nd || nd.pinned) continue;
      let dx = nd.x - pcx;
      let dy = nd.y - pcy;
      let dist = Math.sqrt((dx * dx) + (dy * dy));
      if (dist < 0.001) {
        const a = Math.random() * Math.PI * 2;
        dx = Math.cos(a); dy = Math.sin(a); dist = 1;
      }
      if (isRoot) {
        // Macro gravity-to-(elliptical)-ring: positions the tier-1 family around
        // the core, fills the canvas aspect, and pins rotational orientation.
        const tx = pcx + ((ring * shapeX) * (dx / dist));
        const ty = pcy + ((ring * shapeY) * (dy / dist));
        nd.fx += (tx - nd.x) * P.gravityStrength;
        nd.fy += (ty - nd.y) * P.gravityStrength;
      } else {
        // SHORT per-edge rim spring (Gource): rest = the two nodes' own extents +
        // a small gap, so the child sits tangent on its parent's rim and the edge
        // strains short no matter how big or deep the family is. Stronger than
        // gravity so it holds the radius; sibling/overlap repulsion spread the
        // children ANGULARLY around the rim rather than pushing them out radially.
        const rest = getPhysicsNodeExtent(key) + getPhysicsNodeExtent(loop.id) + P.edgeGap;
        const f = (dist - rest) * P.edgeSpringStrength;
        nd.fx -= (dx / dist) * f;
        nd.fy -= (dy / dist) * f;
      }

      // Outward bias (Gource parent-normal, dirnode.cpp:675): a constant push
      // away from the FIXED core. Its tangential-about-the-parent component
      // walks the child onto the far side of its parent (out of the tier-1
      // shell), while gravity-to-ring still owns the radius. Radial about the
      // core means zero torque, so it settles instead of rotating the cloud.
      if (fanActive) {
        const ox = nd.x - corePx;
        const oy = nd.y - corePy;
        const olen = Math.sqrt((ox * ox) + (oy * oy)) || 1;
        nd.fx += (ox / olen) * P.outwardFanStrength;
        nd.fy += (oy / olen) * P.outwardFanStrength;
      }
    }

    // Decaying (1/d^2) central repulsion among siblings — the long-range
    // distribution force overlap-only repulsion lacks. Scaled by ring^2 so its
    // reach tracks the family size; capped near contact to avoid a singularity.
    if (loops.length > 1) {
      const k = P.siblingRepelStrength * repelRadius * repelRadius;
      for (let i = 0; i < loops.length; i++) {
        const a = P.nodes.get(loops[i].id);
        if (!a || a.pinned) continue;
        for (let j = i + 1; j < loops.length; j++) {
          const b = P.nodes.get(loops[j].id);
          if (!b) continue;
          const dx = a.x - b.x;
          const dy = a.y - b.y;
          let d2 = (dx * dx) + (dy * dy);
          if (d2 < 1) d2 = 1;
          const f = Math.min(P.siblingRepelMax, k / d2);
          const d = Math.sqrt(d2);
          const fx = (dx / d) * f;
          const fy = (dy / d) * f;
          a.fx += fx; a.fy += fy;
          if (!b.pinned) { b.fx -= fx; b.fy -= fy; }
        }
      }
    }
  }

  // Puddle walls — reshape the cloud to the canvas aspect so it fills a
  // non-square canvas like a puddle taking its container's shape. The box is
  // sized from the STABLE root-ring radius (not the live bbox — that fed back
  // into both the walls and the camera and churned), shaped to the canvas
  // aspect: squeezing the narrow side makes the (overlap-incompressible) cloud
  // bulge into the long side instead of overlapping, and the box axes pin the
  // rotational spin. The camera auto-fit frames the result, so a small panel
  // zooms out rather than compressing.
  {
    const a = (vw > 0 && vh > 0) ? (vw / vh) : 1;
    const cloudR = (ringRadius.get('__root__') || P.springRestLength) * P.puddleExtent;
    const boxHalfW = cloudR * Math.sqrt(a);
    const boxHalfH = cloudR / Math.sqrt(a);
    const wMinX = cx - boxHalfW, wMaxX = cx + boxHalfW;
    const wMinY = cy - boxHalfH, wMaxY = cy + boxHalfH;
    for (let i = 0; i < n; i++) {
      const nd = nodes[i];
      if (nd.pinned) continue;
      if (nd.x < wMinX) nd.fx += (wMinX - nd.x) * P.wallStrength;
      else if (nd.x > wMaxX) nd.fx -= (nd.x - wMaxX) * P.wallStrength;
      if (nd.y < wMinY) nd.fy += (wMinY - nd.y) * P.wallStrength;
      else if (nd.y > wMaxY) nd.fy -= (nd.y - wMaxY) * P.wallStrength;
    }
  }

  // 3. Overlap repulsion — Gource's overlap-only, penetration-proportional
  // push (dirnode.cpp applyForceDir). Fires only when node footprints actually
  // intersect; the force is the penetration depth, so deep overlaps separate
  // hard and settled, non-touching nodes feel no long-range field.
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) {
      const a = nodes[i], b = nodes[j];
      let dx = b.x - a.x;
      let dy = b.y - a.y;
      let dist = Math.sqrt((dx * dx) + (dy * dy));
      const loopA = ids[i] === '__system__' ? null : state.loops.get(ids[i]);
      const loopB = ids[j] === '__system__' ? null : state.loops.get(ids[j]);
      let spacingMultiplier = getPairSpacingMultiplier(loopA, loopB);
      if (isParentChildRelation(loopA, loopB)) {
        spacingMultiplier *= P.parentChildRepulsionMultiplier;
      }
      const minGap = getPhysicsNodeExtent(ids[i]) + getPhysicsNodeExtent(ids[j]) + P.collisionPadding;
      if (dist >= minGap) continue;
      if (dist < 0.001) {
        const angle = Math.random() * Math.PI * 2;
        dx = Math.cos(angle);
        dy = Math.sin(angle);
        dist = 1;
      }
      const force = (minGap - dist) * P.overlapRepulsionStrength * spacingMultiplier;
      const fx = (dx / dist) * force;
      const fy = (dy / dist) * force;
      a.fx -= fx; a.fy -= fy;
      b.fx += fx; b.fy += fy;
    }
  }

  // 4. Integration — overdamped (Gource move(), dirnode.cpp:811: pos += accel
  // each frame, accel reset, no stored velocity). Without momentum the layout
  // descends straight to its fixed point and stops dead — no orbiting, slosh,
  // or limit-cycle spin — which is what makes a settled Gource graph feel calm
  // at rest. The per-frame move is capped so a deep overlap or a freshly added
  // node resolves without a jarring jump.
  for (let i = 0; i < n; i++) {
    const nd = nodes[i];
    if (nd.pinned) continue;
    let mx = nd.fx * P.stepScale * motionScale;
    let my = nd.fy * P.stepScale * motionScale;
    const move = Math.sqrt((mx * mx) + (my * my));
    if (move > P.maxStep) {
      const s = P.maxStep / move;
      mx *= s; my *= s;
    }
    nd.x += mx;
    nd.y += my;
    nd.vx = mx; // expose the last displacement for any velocity readers
    nd.vy = my;
  }
}

// Write physics positions to DOM — node transforms and linking line endpoints.
function updateNodePositions() {
  // System node.
  const sysP = physics.nodes.get('__system__');
  if (sysP) {
    const sysG = canvasNodeLayer.querySelector('.system-node');
    if (sysG) sysG.setAttribute('transform', `translate(${sysP.x},${sysP.y})`);
  }

  // Loop nodes.
  for (const [id, nd] of physics.nodes) {
    if (id === '__system__') continue;
    const g = canvasNodeLayer.querySelector(`[data-loop-id="${id}"]`);
    if (g) g.setAttribute('transform', `translate(${nd.x},${nd.y})`);
  }

  // Linking line endpoints.
  const lines = canvasEdgeLayer.querySelectorAll('.link-line');
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
  container: 'container',
};

const NODE_LABEL_GAP = 24;
const SYSTEM_LABEL_GAP = 20;

// Derive a visual category from loop data. Drives fill tone and sigil.
function getLoopCategoryInfo(loop) {
  if (isContainerLoop(loop)) {
    return { category: 'container', source: 'config.Operation=container' };
  }
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
  container:     '', // semantic grouping — empty center, no sigil
};

// categorySigil returns the center glyph for a category, preserving an
// intentional empty sigil (e.g. containers) instead of falling back to the
// generic dot.
function categorySigil(category) {
  const sigil = CATEGORY_SIGILS[category];
  return sigil != null ? sigil : CATEGORY_SIGILS.generic;
}

// Operation type (the structured config.Operation execution model) — mirrors the
// data-operation projection so a periodic timer, an event-driven loop, a
// request/reply handler, and a container each read distinctly.
function getLoopOperationType(loop) {
  if (isContainerLoop(loop)) return 'container';
  if (loop && loop.handler_only) return 'handler';
  if (loop && loop.event_driven) return 'event';
  return 'timer';
}

// Center glyph by operation type. Conversation channels read as '@' (the most
// recognizable "this is a chat" cue) regardless of operation; containers stay
// empty (their tiny knuckle is the cue). This replaces the heuristic
// category sigil, which collapsed most loops to a featureless dot.
const OPERATION_SIGILS = {
  timer:   '◷', // periodic — runs on a schedule / wake timer
  event:   '⚡', // event-driven — reacts to an incoming event
  handler: '⇄', // request / reply handler
  container: '',
};
function loopSigil(loop) {
  if (isContainerLoop(loop)) return '';
  if (getLoopCategory(loop) === 'channel') return '@';
  const sigil = OPERATION_SIGILS[getLoopOperationType(loop)];
  return sigil != null ? sigil : '·';
}

function normalizeVisualCategory(category) {
  return CATEGORY_LABELS[category] ? category : 'generic';
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
// Containers are semantic groupings, not executing entities — rendered small,
// just large enough to be a comfortable context-menu click target.
const CONTAINER_NODE_R = 16;
// Go-backed loops (no LLM model of their own) sit just above containers: they
// execute, but they aren't sized by a model the way LLM loops are.
const GO_BACKED_NODE_R = 20;

function getModelRadiusFromParams(params) {
  if (params === null) return DEFAULT_NODE_R;

  const minParams = 3;    // floor (smallest model we'd see)
  const maxParams = 700;  // ceiling (largest model we'd see)
  const t = (Math.sqrt(params) - Math.sqrt(minParams)) /
            (Math.sqrt(maxParams) - Math.sqrt(minParams));
  const clamped = Math.max(0, Math.min(1, t));
  return MIN_NODE_R + clamped * (MAX_NODE_R - MIN_NODE_R);
}

// Node radius from a model's context window, log-compressed. Context windows
// span ~4k..1M+ (≈250x); a linear map would crush small local models to dots
// and balloon frontier models off-screen, so we interpolate radius across the
// log of the window. A node's "stature" = the heft of the brain it runs: a
// frontier Opus (huge window) reads visibly larger than a small local model,
// while structural containers stay the fixed knuckle (handled separately).
const CTX_WINDOW_MIN = 4096;     // 4k — smallest model we'd expect
const CTX_WINDOW_MAX = 1048576;  // 1M — frontier ceiling (clamps above)
function getContextWindowRadius(contextWindow) {
  if (!(contextWindow > 0)) return DEFAULT_NODE_R;
  const t = (Math.log(contextWindow) - Math.log(CTX_WINDOW_MIN)) /
            (Math.log(CTX_WINDOW_MAX) - Math.log(CTX_WINDOW_MIN));
  const clamped = Math.max(0, Math.min(1, t));
  return MIN_NODE_R + clamped * (MAX_NODE_R - MIN_NODE_R);
}

// Compact human label for a context window (1M / 400k / 8k).
function formatContextWindow(n) {
  if (!(n > 0)) return '';
  if (n >= 1000000) return (n / 1000000).toFixed(n % 1000000 === 0 ? 0 : 1) + 'M';
  if (n >= 1000) return Math.round(n / 1000) + 'k';
  return String(n);
}

function getLoopContextWindow(loop) {
  if (!loop) return 0;
  const recent = loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
  const candidates = [
    loop.context_window,
    loop.config && loop.config.ContextWindow,
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

// Live context-window utilization (0..1): how full this loop's window is right
// now. Static node size encodes the model's CAPACITY; this is the dynamic USAGE
// that rides on top as an inner ring — a loop whose ring is nearly full is about
// to get slow/expensive.
function getLoopContextFill(loop) {
  const recent = loop && loop.recent_iterations && loop.recent_iterations[0];
  if (!recent) return 0;
  const win = Number(recent.context_window) || getLoopContextWindow(loop);
  const used = Number(recent.input_tokens) || 0;
  if (!(win > 0)) return 0;
  return Math.max(0, Math.min(1, used / win));
}

function getLoopConfiguredModelRef(loop) {
  return (loop && loop.config && typeof loop.config.Model === 'string' && loop.config.Model) || '';
}

function deploymentMatchesModelRef(dep, ref) {
  if (!dep || !ref) return false;
  const normalizedRef = String(ref).trim().toLowerCase();
  if (!normalizedRef) return false;
  const deploymentID = String(dep.id || '').trim().toLowerCase();
  const deploymentModel = String(dep.model || '').trim().toLowerCase();
  if (deploymentID === normalizedRef || deploymentModel === normalizedRef) return true;
  return deploymentID.endsWith('/' + normalizedRef);
}

function getRegistryContextWindowForModel(ref) {
  if (!ref) return 0;
  const registry = (state.system && state.system.models) || null;
  const deployments = registry && Array.isArray(registry.deployments) ? registry.deployments : [];
  let maxWindow = 0;
  for (const dep of deployments) {
    if (!deploymentMatchesModelRef(dep, ref)) continue;
    const candidates = [
      dep.loaded_context_window,
      dep.max_context_window,
      dep.context_window,
    ];
    for (const candidate of candidates) {
      const n = Number(candidate);
      if (Number.isFinite(n) && n > maxWindow) {
        maxWindow = n;
      }
    }
  }
  return maxWindow;
}

function getContextTier(contextWindow) {
  for (const tier of CONTEXT_TIERS) {
    if (contextWindow <= tier.max) return tier;
  }
  return CONTEXT_TIERS[CONTEXT_TIERS.length - 1];
}

function getLoopVisualCapacity(loop) {
  // Containers don't execute, so context capacity is meaningless for them:
  // fixed small radius, no tier label/badge.
  if (isContainerLoop(loop)) {
    return { radius: CONTAINER_NODE_R, label: '', key: 'container', basis: 'container', contextWindow: 0 };
  }
  const contextWindow = getLoopContextWindow(loop);
  if (contextWindow > 0) {
    const tier = getContextTier(contextWindow);
    return {
      radius: getContextWindowRadius(contextWindow),
      label: formatContextWindow(contextWindow),
      key: tier.key,
      basis: 'context',
      contextWindow,
    };
  }

  const recent = loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
  // Only a model the loop OWNS counts (configured, or actually run) — do NOT
  // fall back to the system default, or a pure-Go loop would borrow a big LLM
  // size it never uses.
  const modelName = loop._liveModel
    || loop._lastModel
    || (recent && recent.model)
    || getLoopConfiguredModelRef(loop)
    || '';
  if (modelName) {
    const registryContextWindow = getRegistryContextWindowForModel(modelName);
    if (registryContextWindow > 0) {
      const tier = getContextTier(registryContextWindow);
      return {
        radius: getContextWindowRadius(registryContextWindow),
        label: formatContextWindow(registryContextWindow),
        key: tier.key,
        basis: 'context',
        contextWindow: registryContextWindow,
      };
    }
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
    // Has a model but unknown size — treat as a generic LLM.
    return { radius: DEFAULT_NODE_R, label: '?', key: 'unknown', basis: 'unknown', contextWindow: 0 };
  }

  // No model of its own → a pure Go-backed loop (executes, but runs no LLM).
  // Sized just above a container so it reads as lightweight, not as a big brain.
  return { radius: GO_BACKED_NODE_R, label: '', key: 'go', basis: 'go', contextWindow: 0 };
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

    const created = new Date(note.createdAt);
    const age = document.createElement('time');
    age.className = 'notification-card__age';
    age.dateTime = created.toISOString();
    age.textContent = timeAgo(created);
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

function getLoopLatestSnapshot(loop) {
  const history = state.iterationHistory.get(loop.id);
  if (history && history.length > 0) return history[0];
  return loop.recent_iterations && loop.recent_iterations.length > 0
    ? loop.recent_iterations[0]
    : null;
}

function describeLoopExecutionMode(loop) {
  if (isContainerLoop(loop)) return 'container';
  if (loop.handler_only) return 'handler';
  if (loop.event_driven) return 'event-driven llm';
  return 'timer-driven llm';
}

function buildLoopEntity(loop) {
  const categoryInfo = getLoopCategoryInfo(loop);
  const latest = getLoopLatestSnapshot(loop);
  const llmContext = loop._llmContext || null;
  const hints = (loop.config && loop.config.Hints) || {};
  const metadata = (loop.config && loop.config.Metadata) || {};
  const loopTooling = normalizeTooling(loop.tooling, {
    configuredTags: (loop.config && loop.config.Tags) || [],
    loadedTags: loop.active_tags || [],
    excludedTools: (loop.config && loop.config.ExcludeTools) || [],
  });
  const latestTooling = normalizeTooling(latest && latest.tooling, {
    configuredTags: loopTooling.configuredTags,
    loadedTags: latest && latest.active_tags,
    effectiveTools: latest && latest.effective_tools,
    excludedTools: loopTooling.excludedTools,
    toolsUsed: latest && latest.tools_used,
  });
  const liveTooling = normalizeTooling(llmContext && llmContext.tooling, {
    configuredTags: loopTooling.configuredTags,
    loadedTags: llmContext && llmContext.active_tags,
    effectiveTools: llmContext && llmContext.effective_tools,
    excludedTools: loopTooling.excludedTools,
  });
  const currentTooling = (liveTooling.loadedTags.length > 0 || liveTooling.effectiveTools.length > 0 || liveTooling.loadedCapabilities.length > 0)
    ? liveTooling
    : ((latestTooling.loadedTags.length > 0 || latestTooling.effectiveTools.length > 0 || latestTooling.loadedCapabilities.length > 0)
      ? latestTooling
      : loopTooling);
  const configTags = loopTooling.configuredTags.slice();
  const excludedTools = loopTooling.excludedTools.slice();
  const activeTags = loopTooling.loadedTags.slice();
  const allTags = Array.from(new Set([...configTags, ...activeTags])).sort();
  const currentScopeTags = currentTooling.loadedTags.slice();
  const currentLoadedCapabilities = currentTooling.loadedCapabilities.slice();
  const latestLoadedCapabilities = latestTooling.loadedCapabilities.slice();
  const currentEffectiveTools = currentTooling.effectiveTools.slice();
  const latestEffectiveTools = latestTooling.effectiveTools.slice();
  const currentToolsUsed = currentTooling.toolsUsed || {};
  const latestToolsUsed = latestTooling.toolsUsed || {};
  const latestModel = loop._liveModel || loop._lastModel || (latest && latest.model) || '';
  const latestRequestID = (latest && latest.request_id) || '';
  const startedAt = parseTimestamp(loop.started_at) ? loop.started_at : '';
  const lastWakeAt = parseTimestamp(loop.last_wake_at) ? loop.last_wake_at : '';
  const currentConvID = loop._currentConvID || '';
  const configuredConvID = metadata.conversation_id || '';
  const recentConvIDs = Array.from(new Set(
    [currentConvID, configuredConvID, ...((Array.isArray(loop.recent_conv_ids) ? loop.recent_conv_ids : []).filter(Boolean))],
  ));
  const primaryConvID = currentConvID || configuredConvID || recentConvIDs[0] || '';
  const trustZone = metadata.trust_zone || '';
  const subsystem = metadata.subsystem || '';
  const liveTools = Array.isArray(loop._liveTools) ? loop._liveTools.slice() : [];
  const activeLiveTools = liveTools.filter((entry) => entry && entry.status === 'running');

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
    configuredConvID,
    primaryConvID,
    recentConvIDs,
    latestRequestID,
    latestSnapshot: latest,
    latestModel,
    startedAt,
    lastWakeAt,
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
    excludedTools,
    activeTags,
    allTags,
    availableCapabilities: getCapabilityCatalogEntries(state.system),
    currentLoadedCapabilities,
    latestLoadedCapabilities,
    currentScopeTags,
    currentEffectiveTools,
    latestEffectiveTools,
    currentToolsUsed,
    latestToolsUsed,
    liveTools,
    activeLiveTools,
  };
}

function getLoopPrimaryConversationID(entity) {
  if (!entity) return '';
  return entity.primaryConvID || entity.currentConvID || entity.configuredConvID || '';
}

function isConversationBackedLoop(entity) {
  return !!getLoopPrimaryConversationID(entity);
}

function makeLoopCurrentTurnAlert(text, kind = 'warn') {
  const note = document.createElement('div');
  note.className = 'loop-turn-alert loop-turn-alert--' + kind;
  note.textContent = text;
  return note;
}

function renderLoopCurrentTurnCard(loop, entity, conversationSummary) {
  const isProcessing = loop.state === 'processing' && !!loop._iterStartTs;
  const serviceDegraded = isServiceDegraded(loop.name);
  const latestModelLabel = entity.latestModel || 'model pending';
  const lastWakeDate = parseTimestamp(entity.lastWakeAt);
  const lastWakeAgo = lastWakeDate ? timeAgo(lastWakeDate) : '';
  const threadLabel = conversationSummary ? conversationSummary.label : (getLoopPrimaryConversationID(entity) ? shortID(getLoopPrimaryConversationID(entity)) : 'No thread bound');
  const activeToolNames = entity.activeLiveTools.map((entry) => entry.tool).filter(Boolean);
  const activeToolSet = Array.from(new Set(activeToolNames)).sort();
  const currentLoadedCapabilities = entity.currentLoadedCapabilities || [];
  const currentEffectiveTools = entity.currentEffectiveTools || [];
  const iterationLabel = isProcessing
    ? '#' + formatNumber((entity.iterations || 0) + 1)
    : (entity.iterations ? '#' + formatNumber(entity.iterations) : 'pending');
  const contextLabel = entity.contextWindow ? `${formatNumber(entity.contextWindow)} ctx` : '';

  let titleSummary = '';
  if (isProcessing) {
    titleSummary = [
      threadLabel,
      latestModelLabel !== 'model pending' ? 'on ' + latestModelLabel : '',
      activeToolNames.length > 0
        ? `${formatNumber(activeToolNames.length)} tool${activeToolNames.length === 1 ? '' : 's'} in flight`
        : `iteration ${iterationLabel} in progress`,
    ].filter(Boolean).join(' · ');
  } else if (entity.latestRequestID) {
    titleSummary = [
      threadLabel,
      formatSchemaToken(entity.stateLabel),
      'req ' + shortID(entity.latestRequestID),      lastWakeAgo ? 'wake ' + lastWakeAgo : '',
    ].filter(Boolean).join(' · ');
  } else {
    titleSummary = [
      threadLabel,
      formatSchemaToken(entity.stateLabel),
      lastWakeAgo ? 'wake ' + lastWakeAgo : 'awaiting next turn',
    ].filter(Boolean).join(' · ');
  }

  const card = makeSchemaCard('Current Turn', 'Active request, live tools, and thread health', {
    entityKind: entity.kind,
    key: 'current-turn',
    titleSummary,
    titleFacts: [
      entity.latestRequestID ? `Req ${shortID(entity.latestRequestID)}` : '',
      serviceDegraded ? 'service degraded' : '',
      entity.activeLiveTools.length > 0 ? `${formatNumber(entity.activeLiveTools.length)} active tools` : '',
      entity.trustZone || '',
    ].filter(Boolean),
    widgetSummary: titleSummary,
    widgetFacts: [
      { label: 'State', value: formatSchemaToken(entity.stateLabel) },
      { label: 'Thread', value: threadLabel },
      { label: 'Request', value: entity.latestRequestID ? shortID(entity.latestRequestID) : 'pending' },
      { label: 'Iteration', value: iterationLabel },
      { label: 'Model', value: latestModelLabel },
      { label: 'Health', value: serviceDegraded ? 'degraded' : (entity.lastError ? 'recovering' : 'steady') },
      entity.activeLiveTools.length > 0 ? { label: 'Tools live', value: activeToolSet.join(', ') } : null,
      lastWakeAgo ? { label: 'Wake', value: lastWakeAgo } : null,
    ],
    widgetNotes: [
      entity.trustZone ? { label: 'Trust', value: entity.trustZone } : null,
      conversationSummary && conversationSummary.metaLine ? conversationSummary.metaLine : null,
    ].filter(Boolean),
  });

  const turnMetrics = makeSchemaWidgetGrid([
    { label: 'State', value: formatSchemaToken(entity.stateLabel) },
    { label: 'Request', value: entity.latestRequestID ? shortID(entity.latestRequestID) : 'pending' },
    { label: 'Iteration', value: iterationLabel },
    { label: 'Model', value: latestModelLabel },
    { label: 'Health', value: serviceDegraded ? 'degraded' : (entity.lastError ? 'recovering' : 'steady') },
    entity.activeLiveTools.length > 0
      ? { label: 'Tools in flight', value: activeToolSet.join(', ') }
      : { label: 'Thread', value: threadLabel },
  ]);
  if (turnMetrics) card.body.appendChild(turnMetrics);

  const brief = document.createElement('div');
  brief.className = 'loop-turn-brief';
  const briefSummary = document.createElement('div');
  briefSummary.className = 'loop-turn-brief__summary';
  if (isProcessing) {
    briefSummary.textContent = entity.activeLiveTools.length > 0
      ? `The loop is actively working this turn, with ${entity.activeLiveTools.length} tool${entity.activeLiveTools.length === 1 ? '' : 's'} in flight${currentEffectiveTools.length > 0 ? ` across a ${formatNumber(currentEffectiveTools.length)}-tool surface` : ''}.`
      : 'The loop is actively working this turn. Watch the live telemetry below for context growth, loaded capabilities, tool surface, and model progress.';
  } else if (entity.latestSnapshot) {
    briefSummary.textContent = entity.lastError
      ? 'The latest recorded turn ended with an error. The snapshot below shows the last request detail, loaded capabilities, tool surface, timing, and tool activity for triage.'
      : 'The loop is currently idle. The latest recorded turn below is the best executive summary of recent behavior, tool surface, and near-term future.';
  } else {
    briefSummary.textContent = 'No request detail snapshot is available yet. This view will fill in once the loop completes its first recorded iteration.';
  }
  brief.appendChild(briefSummary);

  const briefGrid = document.createElement('div');
  briefGrid.className = 'loop-turn-brief__grid';
  const briefFacts = [
    { label: 'Thread', value: threadLabel },
    { label: 'Request', value: entity.latestRequestID ? shortID(entity.latestRequestID) : 'pending' },
    { label: 'Model', value: latestModelLabel },
    { label: 'State', value: formatSchemaToken(entity.stateLabel) },
    { label: 'Context', value: contextLabel || 'pending' },
    { label: 'Wake', value: lastWakeAgo || 'pending' },
  ];
  for (const item of briefFacts) {
    const cell = document.createElement('div');
    cell.className = 'loop-turn-brief__metric';
    const value = document.createElement('div');
    value.className = 'loop-turn-brief__metric-value';
    value.textContent = item.value;
    cell.appendChild(value);
    const label = document.createElement('div');
    label.className = 'loop-turn-brief__metric-label';
    label.textContent = item.label;
    cell.appendChild(label);
    briefGrid.appendChild(cell);
  }
  brief.appendChild(briefGrid);
  card.body.appendChild(brief);

  // Capabilities render once, in the shared renderActiveCapabilities() section —
  // not duplicated here (this was the conversation-vs-non-conversation divergence).

  if (entity.latestRequestID) {
    const requestWrap = document.createElement('div');
    requestWrap.className = 'schema-subsection';
    requestWrap.innerHTML = '<h4 class="schema-subsection__title">Request</h4>';
    const requestRow = document.createElement('div');
    requestRow.className = 'loop-turn-request';
    requestRow.appendChild(makeRequestChip(entity.latestRequestID));
    requestWrap.appendChild(requestRow);
    card.body.appendChild(requestWrap);
  }

  if (serviceDegraded) {
    card.body.appendChild(makeLoopCurrentTurnAlert('Backing service is degraded. New wakes or tool work may stall upstream even when the loop itself looks calm.', 'warn'));
  } else if (entity.lastError) {
    card.body.appendChild(makeLoopCurrentTurnAlert('Last error: ' + entity.lastError, 'error'));
  }

  if (conversationSummary) {
    const threadWrap = document.createElement('div');
    threadWrap.className = 'schema-subsection';
    threadWrap.innerHTML = '<h4 class="schema-subsection__title">Thread</h4>';
    threadWrap.appendChild(makeConversationSummaryEntry(conversationSummary, {
      current: !!(entity.currentConvID && conversationSummary.id === entity.currentConvID),
    }));
    card.body.appendChild(threadWrap);
  }

  const liveWrap = document.createElement('div');
  liveWrap.className = 'schema-subsection';
  liveWrap.innerHTML = `<h4 class="schema-subsection__title">${isProcessing ? 'Active Iteration' : 'Latest Iteration'}</h4>`;

  const aggregates = document.createElement('div');
  aggregates.className = 'detail-aggregates';
  renderAggregates(loop, aggregates);
  liveWrap.appendChild(aggregates);

  if (isProcessing) {
    liveWrap.appendChild(buildLiveCard(loop));
  } else if (entity.latestSnapshot) {
    liveWrap.appendChild(buildPastCard(entity.latestSnapshot, loop.handler_only, 0, true));
  } else {
    const empty = document.createElement('div');
    empty.className = 'loop-turn-empty';
    empty.textContent = entity.latestRequestID
      ? 'Waiting for the next iteration heartbeat.'
      : 'No turn data yet. The thread is attached and waiting for its first wake.';
    liveWrap.appendChild(empty);
  }

  card.body.appendChild(liveWrap);
  return card.card;
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

// fetchConversationSummary resolves a single conversation's summary by id via
// the queryable list endpoint (?ids=). The inspector already holds the loop's
// conversation ids, so this is a cheap point lookup — no all-conversations
// index. Resolves to the summary object, or null when the id is unknown.
function fetchConversationSummary(conversationID) {
  return fetch('/v1/conversations?ids=' + encodeURIComponent(conversationID))
    .then((resp) => {
      if (!resp.ok) throw new Error('conversation summary unavailable: ' + resp.status);
      return resp.json();
    })
    .then((body) => {
      const list = Array.isArray(body.conversations) ? body.conversations : [];
      return list.find((conv) => conv && conv.id === conversationID) || null;
    });
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
    fetchConversationSummary(conversationID),
    fetch('/v1/archive/sessions?conversation_id=' + encodeURIComponent(conversationID) + '&limit=' + CONVERSATION_SESSION_LIMIT)
      .then((resp) => {
        if (!resp.ok) throw new Error('archive sessions unavailable: ' + resp.status);
        return resp.json();
      })
      .then((body) => Array.isArray(body.sessions) ? body.sessions : []),
  ]).then(([conversationResult, sessionsResult]) => {
    const conversation = conversationResult.status === 'fulfilled' ? conversationResult.value : null;
    const sessions = sessionsResult.status === 'fulfilled' ? sessionsResult.value : [];
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
  const registry = (sys && sys.models) || {};
  const routerStats = (sys && sys.router) || {};
  const capabilityCatalog = (sys && sys.capabilities) || null;
  const capabilityEntries = getCapabilityCatalogEntries(sys);
  const capabilitySummary = summarizeCapabilityCatalog(capabilityEntries);
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
    uptime: formatUptime(sys.uptime_seconds),
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
    capabilityCatalog,
    capabilityEntries,
    capabilityCount: capabilitySummary.capabilityCount,
    toolboxToolCount: capabilitySummary.uniqueToolCount,
    coreCapabilityCount: capabilitySummary.coreCount,
    discoverableCapabilityCount: capabilitySummary.discoverableCount,
    routerStats,
    registry,
    health,
  };
}

function buildLoopNodeTitle(loop, capacity) {
  const entity = buildLoopEntity(loop);
  const primaryConvID = getLoopPrimaryConversationID(entity);
  const convSummary = primaryConvID ? state.conversationDetails.get(primaryConvID) : null;
  const runningTools = entity.activeLiveTools.map((entry) => entry.tool).filter(Boolean);
  const scopeTags = (entity.currentLoadedCapabilities || []).map((entry) => entry.tag);
  const toolSurface = entity.currentEffectiveTools || [];
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
  if (primaryConvID) {
    if (convSummary) {
      parts.push('Thread: ' + convSummary.label + (convSummary.metaLine ? ' · ' + convSummary.metaLine : ''));
    } else {
      parts.push('Conversation: ' + primaryConvID);
    }
  }
  if (entity.trustZone) {
    parts.push('Trust: ' + entity.trustZone);
  }
  if (entity.latestRequestID) {
    parts.push('Request: ' + shortID(entity.latestRequestID));
  }
  if (capacity.basis === 'context' && capacity.contextWindow > 0) {
    parts.push('Context: ' + formatNumber(capacity.contextWindow));
  } else if (capacity.basis === 'model') {
    parts.push('Capacity: est. ' + capacity.label);
  }
  if (entity.latestModel) {
    parts.push('Model: ' + entity.latestModel);
  }
  if (scopeTags.length > 0) {
    parts.push('Loaded capabilities: ' + scopeTags.join(', '));
  }
  if (toolSurface.length > 0) {
    parts.push('Tool surface: ' + toolSurface.join(', '));
  }
  if (runningTools.length > 0) {
    parts.push('Active tools: ' + runningTools.join(', '));
  }
  if (isServiceDegraded(loop.name)) {
    parts.push('Health: backing service degraded');
  } else if (entity.lastError) {
    parts.push('Health: last error ' + truncate(entity.lastError, 72));
  }
  if (entity.lastWakeAt) {
    const lastWakeDate = parseTimestamp(entity.lastWakeAt);
    if (lastWakeDate) parts.push('Last wake: ' + timeAgo(lastWakeDate));
  }
  return parts.join('\n');
}

// ---------------------------------------------------------------------------
// DOM References
// ---------------------------------------------------------------------------

const $ = (sel) => document.querySelector(sel);
const canvas = $('#canvas');
const canvasWorld = $('#canvas-world');
const canvasEdgeLayer = $('#canvas-edge-layer');
const canvasNodeLayer = $('#canvas-node-layer');
const connBadge = $('#conn-status');
const detailPlaceholder = $('#detail-placeholder');
const detailPanel = $('#detail-panel');
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
const DETAIL_CARD_LAYOUTS_KEY = 'thane.dashboard.cardLayouts.v1';
const DEFAULT_SCHEMA_CARD_LAYOUT = Object.freeze({ mode: 'full', height: 0 });
const SCHEMA_CARD_PRESET_HEIGHTS = Object.freeze({
  title: 44,
  widget: 220,
});
const SCHEMA_CARD_LAYOUT_ORDER = Object.freeze(['title', 'widget', 'full']);

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
const detailCardLayouts = loadDetailCardLayouts();
let nextNotificationID = 1;
const recentNotificationSignatures = new Map();
let connectionWasDegraded = false;
let lastDetailSelectionKey = null;
let detailInteractionHoldUntil = 0;
let detailPointerSelectionActive = false;
let detailInstantLayoutUntil = 0;
let detailInteractiveHoverActive = false;
let nodeLongPressTimer = 0;
let nodeLongPressState = null;
let suppressNextNodeClickUntil = 0;
const DETAIL_POINTER_GUARD_MS = 120;
const DETAIL_COPY_GUARD_MS = 220;
const DETAIL_SELECTION_RELEASE_MS = 220;
const NODE_LONG_PRESS_MS = 460;
const NODE_LONG_PRESS_MOVE_PX = 14;

function clampValue(value, min, max) {
  return Math.max(min, Math.min(max, value));
}

function clearNodeLongPress() {
  if (nodeLongPressTimer) {
    clearTimeout(nodeLongPressTimer);
    nodeLongPressTimer = 0;
  }
  nodeLongPressState = null;
}

function shouldUseTouchContextMenu(e) {
  if (!e) return false;
  return e.pointerType === 'touch' || e.pointerType === 'pen' ||
    (typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches);
}

function scheduleNodeLongPress(e, opts) {
  if (!shouldUseTouchContextMenu(e)) return;
  clearNodeLongPress();
  nodeLongPressState = {
    x: e.clientX,
    y: e.clientY,
    show: opts.show,
    select: opts.select || null,
  };
  nodeLongPressTimer = window.setTimeout(() => {
    const pending = nodeLongPressState;
    clearNodeLongPress();
    if (!pending) return;
    if (typeof pending.select === 'function') pending.select();
    if (typeof pending.show === 'function') pending.show(pending.x, pending.y);
    suppressNextNodeClickUntil = Date.now() + 700;
  }, NODE_LONG_PRESS_MS);
}

function updateNodeLongPress(e) {
  if (!nodeLongPressState) return;
  const dx = e.clientX - nodeLongPressState.x;
  const dy = e.clientY - nodeLongPressState.y;
  if (Math.hypot(dx, dy) > NODE_LONG_PRESS_MOVE_PX) {
    clearNodeLongPress();
  }
}

function loadDetailCardLayouts() {
  try {
    const raw = window.localStorage.getItem(DETAIL_CARD_LAYOUTS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return {};
    const result = {};
    for (const [key, layout] of Object.entries(parsed)) {
      if (!layout || typeof layout !== 'object') continue;
      const mode = ['title', 'widget', 'full'].includes(layout.mode) ? layout.mode : 'widget';
      const height = 0;
      result[key] = { mode, height };
    }
    return result;
  } catch (_) {
    return {};
  }
}

function saveDetailCardLayouts() {
  try {
    window.localStorage.setItem(DETAIL_CARD_LAYOUTS_KEY, JSON.stringify(detailCardLayouts));
  } catch (_) {
    // Ignore storage failures; in-memory sizing still works.
  }
}

function makeSchemaCardLayoutKey(entityKind, cardKey) {
  if (!entityKind || !cardKey) return '';
  return entityKind + ':' + cardKey;
}

function getSchemaCardLayout(entityKind, cardKey) {
  const key = makeSchemaCardLayoutKey(entityKind, cardKey);
  const stored = key ? detailCardLayouts[key] : null;
  return stored ? { ...stored } : { ...DEFAULT_SCHEMA_CARD_LAYOUT };
}

function setSchemaCardLayout(entityKind, cardKey, layout) {
  const storageKey = makeSchemaCardLayoutKey(entityKind, cardKey);
  if (!storageKey) return;
  const next = {
    mode: ['title', 'widget', 'full'].includes(layout.mode) ? layout.mode : 'full',
    height: 0,
  };
  detailCardLayouts[storageKey] = next;
  saveDetailCardLayouts();
}

function measureSchemaCardLayout(card) {
  const header = card.querySelector('.schema-card__header');
  const titleShell = card.querySelector('.schema-card__title-shell');
  const widgetShell = card.querySelector('.schema-card__widget-shell');
  const bodyShell = card.querySelector('.schema-card__body-shell');
  const body = card.querySelector('.schema-card__body');
  if (!header || !titleShell || !widgetShell || !bodyShell || !body) return null;

  const cardStyle = window.getComputedStyle(card);
  const headerStyle = window.getComputedStyle(header);
  const cardExtras = [
    cardStyle.paddingTop,
    cardStyle.paddingBottom,
    cardStyle.borderTopWidth,
    cardStyle.borderBottomWidth,
  ].reduce((sum, value) => sum + (parseFloat(value) || 0), 0);
  const headerGap = parseFloat(headerStyle.marginBottom) || 0;
  const headerHeight = header.offsetHeight;
  const prevTitleDisplay = titleShell.style.display;
  const prevWidgetDisplay = widgetShell.style.display;
  titleShell.style.display = titleShell.childNodes.length ? 'grid' : 'none';
  widgetShell.style.display = widgetShell.childNodes.length ? 'grid' : 'none';
  const titleHeightContent = titleShell.scrollHeight;
  const widgetHeightContent = widgetShell.scrollHeight;
  titleShell.style.display = prevTitleDisplay;
  widgetShell.style.display = prevWidgetDisplay;
  const bodyHeight = body.scrollHeight;

  const titleHeight = Math.max(
    Math.ceil(cardExtras + headerHeight + (titleHeightContent > 0 ? headerGap + titleHeightContent : 0)),
    SCHEMA_CARD_PRESET_HEIGHTS.title,
  );
  const widgetHeight = Math.max(
    titleHeight,
    Math.ceil(cardExtras + headerHeight + (widgetHeightContent > 0 ? headerGap + widgetHeightContent : 0)),
  );
  const fullHeight = Math.max(
    widgetHeight,
    titleHeight,
    Math.ceil(cardExtras + headerHeight + headerGap + bodyHeight),
  );

  return {
    cardExtras,
    headerGap,
    headerHeight,
    titleHeightContent,
    widgetHeightContent,
    bodyHeight,
    titleHeight,
    widgetHeight,
    fullHeight,
  };
}

function getSchemaCardHeightForLayout(metrics, layout) {
  switch (layout.mode) {
    case 'title':
      return metrics.titleHeight;
    case 'widget':
      return metrics.widgetHeight;
    case 'full':
    default:
      return metrics.fullHeight;
  }
}

function inferSchemaCardDensity(metrics, height) {
  if (height <= metrics.titleHeight + 8) return 'title';
  if (height <= metrics.widgetHeight + 18) return 'widget';
  return 'full';
}

function formatSchemaCardMode(mode) {
  return mode === 'title' ? 'Title' : mode === 'widget' ? 'Widget' : 'Full';
}

function nextSchemaCardMode(mode) {
  const current = SCHEMA_CARD_LAYOUT_ORDER.includes(mode) ? mode : 'full';
  const idx = SCHEMA_CARD_LAYOUT_ORDER.indexOf(current);
  return SCHEMA_CARD_LAYOUT_ORDER[(idx + 1) % SCHEMA_CARD_LAYOUT_ORDER.length];
}

function updateSchemaCardControls(card, activeMode) {
  const btn = card.querySelector('.schema-card__control');
  if (!btn) return;
  const current = SCHEMA_CARD_LAYOUT_ORDER.includes(activeMode) ? activeMode : 'full';
  const next = nextSchemaCardMode(current);
  btn.dataset.layoutMode = current;
  btn.dataset.targetMode = next;
  btn.innerHTML = makeSchemaCardModeIcon(current);
  btn.title = 'Current view: ' + formatSchemaCardMode(current) + '. Click for ' + formatSchemaCardMode(next);
  btn.setAttribute('aria-label', 'Current view: ' + formatSchemaCardMode(current) + '. Click for ' + formatSchemaCardMode(next));
}

function syncSchemaCardLayout(card, overrideLayout = null) {
  if (!card || !card.classList.contains('schema-card--resizable')) return;
  const entityKind = card.dataset.entityKind || '';
  const cardKey = card.dataset.cardKey || '';
  const metrics = measureSchemaCardLayout(card);
  if (!metrics) return;

  const bodyShell = card.querySelector('.schema-card__body-shell');
  const titleShell = card.querySelector('.schema-card__title-shell');
  const widgetShell = card.querySelector('.schema-card__widget-shell');
  const body = card.querySelector('.schema-card__body');
  if (!bodyShell || !titleShell || !widgetShell || !body) return;

  const stored = overrideLayout || getSchemaCardLayout(entityKind, cardKey);
  const desiredHeight = getSchemaCardHeightForLayout(metrics, stored);
  const density = inferSchemaCardDensity(metrics, desiredHeight);
  const clipped = desiredHeight < metrics.fullHeight - 1;

  card.classList.remove('schema-card--title', 'schema-card--widget', 'schema-card--full', 'schema-card--clipped');
  card.classList.add('schema-card--' + density);
  card.dataset.layoutMode = stored.mode;
  card.dataset.layoutDensity = density;

  if (density === 'full' && stored.mode === 'full') {
    card.style.removeProperty('height');
    titleShell.style.removeProperty('display');
    widgetShell.style.removeProperty('display');
    bodyShell.style.removeProperty('max-height');
    bodyShell.style.visibility = 'visible';
    body.style.removeProperty('overflow-y');
  } else {
    const totalHeight = clampValue(desiredHeight, metrics.titleHeight, metrics.fullHeight);
    const bodyLimit = Math.max(0, totalHeight - metrics.cardExtras - metrics.headerHeight - metrics.headerGap);
    card.style.height = totalHeight + 'px';
    if (density === 'title') {
      titleShell.style.display = titleShell.scrollHeight > 0 ? 'grid' : 'none';
      widgetShell.style.display = 'none';
      bodyShell.style.visibility = 'hidden';
      bodyShell.style.maxHeight = '0px';
      body.style.overflowY = 'visible';
    } else if (density === 'widget') {
      titleShell.style.display = 'none';
      widgetShell.style.display = widgetShell.scrollHeight > 0 ? 'grid' : 'none';
      bodyShell.style.visibility = 'hidden';
      bodyShell.style.maxHeight = '0px';
      body.style.overflowY = 'visible';
    } else {
      titleShell.style.display = 'none';
      widgetShell.style.display = 'none';
      bodyShell.style.visibility = 'visible';
      bodyShell.style.maxHeight = bodyLimit + 'px';
      body.style.overflowY = clipped ? 'auto' : 'visible';
    }
  }

  if (clipped && density === 'full') {
    card.classList.add('schema-card--clipped');
  }

  updateSchemaCardControls(card, stored.mode);
}

function syncAllSchemaCardLayouts() {
  for (const card of detailEntity.querySelectorAll('.schema-card--resizable')) {
    syncSchemaCardLayout(card);
  }
}

function applySchemaCardPreset(card, mode) {
  const entityKind = card.dataset.entityKind || '';
  const cardKey = card.dataset.cardKey || '';
  setSchemaCardLayout(entityKind, cardKey, { mode, height: 0 });
  detailInstantLayoutUntil = Date.now() + 250;
  bumpDetailInteractionHold(180);
  if (detailPanel) detailPanel.classList.add('detail-panel--instant');
  syncSchemaCardLayout(card, { mode, height: 0 });
  requestAnimationFrame(() => {
    renderDetail({ force: true, instantLayout: true });
  });
}

function bumpDetailInteractionHold(ms = DETAIL_COPY_GUARD_MS) {
  detailInteractionHoldUntil = Math.max(detailInteractionHoldUntil, Date.now() + ms);
}

function isDetailInteractiveTarget(node) {
  if (!detailPanel || !node) return false;
  const el = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
  return !!(el && detailPanel.contains(el) && el.closest(
    'button, .btn, .toggle-btn, .id-chip, .log-id-chip, summary, a, .schema-card__control',
  ));
}

function nodeWithinDetailPanel(node) {
  if (!detailPanel || !node) return false;
  const el = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
  return !!el && detailPanel.contains(el);
}

function detailTextSelectionActive() {
  const sel = typeof window.getSelection === 'function' ? window.getSelection() : null;
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return false;
  return nodeWithinDetailPanel(sel.anchorNode) || nodeWithinDetailPanel(sel.focusNode);
}

function shouldDeferDetailRender() {
  if (detailInteractiveHoverActive) return true;
  if (detailPointerSelectionActive) return true;
  if (detailTextSelectionActive()) {
    bumpDetailInteractionHold(1500);
    return true;
  }
  return Date.now() < detailInteractionHoldUntil;
}

function currentDetailSelectionKey() {
  if (typeof activeRequestID !== 'undefined' && activeRequestID) return 'request:' + activeRequestID;
  if (state.selected === '__system__') return 'system';
  if (state.selected) return 'loop:' + state.selected;
  return null;
}

function captureOpenDetailKeys() {
  const keys = new Set();
  for (const el of detailEntity.querySelectorAll('details[data-detail-key][open]')) {
    const key = el.dataset.detailKey;
    if (key) keys.add(key);
  }
  return keys;
}

function restoreOpenDetailKeys(keys) {
  if (!keys || keys.size === 0) return;
  for (const key of keys) {
    const el = detailEntity.querySelector(`details[data-detail-key="${CSS.escape(key)}"]`);
    if (el) el.open = true;
  }
}

function withPreservedDetailScroll(renderFn, opts = {}) {
  const selectionKey = currentDetailSelectionKey();
  if (!opts.force && detailPanel && selectionKey !== null && selectionKey === lastDetailSelectionKey && shouldDeferDetailRender()) {
    return;
  }

  const preserve = !!detailPanel && selectionKey !== null && selectionKey === lastDetailSelectionKey;
  const previousTop = preserve ? detailPanel.scrollTop : 0;
  const openDetailKeys = preserve ? captureOpenDetailKeys() : new Set();

  if (detailPanel) {
    const instant = !!opts.instantLayout || Date.now() < detailInstantLayoutUntil;
    detailPanel.classList.toggle('detail-panel--instant', instant);
  }

  renderFn();
  restoreOpenDetailKeys(openDetailKeys);
  syncAllSchemaCardLayouts();

  if (detailPanel) {
    if (preserve) {
      requestAnimationFrame(() => {
        const maxTop = Math.max(0, detailPanel.scrollHeight - detailPanel.clientHeight);
        detailPanel.scrollTop = Math.min(previousTop, maxTop);
      });
    } else {
      detailPanel.scrollTop = 0;
    }
    requestAnimationFrame(() => {
      detailPanel.classList.remove('detail-panel--instant');
    });
  }

  lastDetailSelectionKey = selectionKey;
}

// ---------------------------------------------------------------------------
// Trust Zone Underglow
// ---------------------------------------------------------------------------

// Canonical trust zones. Glow colors live in CSS (.trust-glow--*) so they
// follow the active theme; this set just gates which zones render a glow.
const TRUST_ZONES = new Set(['admin', 'household', 'trusted', 'known', 'unknown']);

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

let store = null;

// connect creates the shared loop store (data/loops.js), points the graph's
// working state at the store's canonical structures, wires the graph's
// reactions to the store's change + lifecycle signals, and starts ingestion.
// The store owns the loop data — one source of truth for every view (graph,
// table, forensics) — and the graph only reads it from here on.
function connect() {
  store = createLoopStore({ client: api, events: subscribeLoopEvents });

  // Existing readers (rendering, physics, inspector, timeline) reference these
  // by name, so pointing them at the store's structures needs no other change.
  state.loops = store.loops;
  state.iterationHistory = store.iterationHistory;
  state.sleepTimers = store.sleepTimers;
  state.events = store.events;

  // Any data change re-renders.
  store.subscribe(renderAll);

  // Connection status → badge + degraded/restored notifications.
  store.on('conn_state', (s) => {
    setConnState(s);
    if (s === 'disconnected') onStreamDisconnected();
    else if (s === 'connected') onStreamConnected();
  });

  store.on('loop_error', ({ loopId, loop, message }) => {
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
  });

  store.on('iteration_complete', ({ loopId }) => {
    // Auto-refresh logs when the selected loop completes an iteration.
    if (state.selected === loopId) fetchLogs(loopId);
  });

  store.on('delegate_complete', ({ id, entry, data }) => {
    if (data.error || data.exhausted) {
      const targetLoopID = entry ? id : (data.parent_loop_id || '');
      addNotification({
        level: data.error ? 'error' : 'warn',
        sourceLabel: 'Delegate',
        title: data.error ? 'Background delegate failed' : 'Background delegate exhausted',
        message: truncate(data.error || data.exhaust_reason || (entry && entry._delegateTask) || 'Delegate did not complete successfully.', 220),
        action: targetLoopID
          ? () => { if (state.loops.has(targetLoopID)) selectLoop(targetLoopID); else selectSystem(); }
          : () => selectSystem(),
        actionLabel: targetLoopID ? 'Inspect loop' : 'Inspect core',
        signature: `delegate-failure:${data.delegate_id || id}:${data.error || data.exhaust_reason || ''}`,
        cooldownMs: 30000,
      });
    }
    // Begin the fade; the node lingers until the store drops it (delegate_remove).
    const node = canvasWorld.querySelector(`[data-loop-id="${id}"]`);
    if (node) node.classList.add('loop-node--fading');
  });

  store.on('delegate_remove', ({ id }) => animateDelegateRemoval(id));

  store.start();
}

// onStreamDisconnected mirrors the former EventSource.onerror: surface a single
// degraded notification. The stream auto-reconnects; the snapshot on reconnect
// restores full state.
function onStreamDisconnected() {
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
}

// onStreamConnected mirrors the former EventSource.onopen.
function onStreamConnected() {
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
}

// animateDelegateRemoval plays the exit animation and clears physics/selection
// for a delegate node the store has dropped from the canonical set.
function animateDelegateRemoval(id) {
  const node = canvasWorld.querySelector(`[data-loop-id="${id}"]`);
  if (node) {
    node.classList.add('loop-node--exiting');
    node.addEventListener('animationend', () => {
      node.remove();
      physics.nodes.delete(id);
    }, { once: true });
  } else {
    physics.nodes.delete(id);
  }
  if (state.selected === id) state.selected = null;
}

let connState = 'connecting';

function setConnState(s) {
  connState = s;
  connBadge.textContent = s;
  connBadge.className = 'conn-badge conn-badge--' + s;
}

// ---------------------------------------------------------------------------
// Data Fetching
// ---------------------------------------------------------------------------

async function fetchLogs(loopId) {
  if (!loopId) return;
  // Ephemeral delegate nodes aren't real loops — no logs endpoint.
  const loop = state.loops.get(loopId);
  if (loop && loop._delegate) {
    renderLogs([]);
    return;
  }
  const level = $('#log-level').value;
  let path = '/loops/' + encodeURIComponent(loopId) + '/logs?limit=100';
  if (level) path += '&level=' + encodeURIComponent(level);

  try {
    const data = await api.get(path);
    renderLogs(Array.isArray(data) ? data : []);
  } catch (err) {
    console.warn('Failed to fetch logs:', err);
  }
}

let systemStartTime = null; // derived from system uptime for local ticking

async function fetchSystemStatus() {
  try {
    const previous = state.system;
    // Reassemble the core's view from the native /v1 resources it draws on.
    // Each fragment degrades independently: a cold registry or unconfigured
    // router just leaves that sub-object empty, and every reader null-guards.
    const [sys, models, router, capabilities] = await Promise.all([
      api.tryGet('/system'),
      api.tryGet('/models/registry'),
      api.tryGet('/telemetry/router'),
      api.tryGet('/telemetry/capabilities'),
    ]);
    if (!sys) {
      state.system = null;
      return;
    }
    const next = {
      status: sys.status,
      health: sys.health || {},
      version: sys.version || {},
      uptime_seconds: sys.uptime_seconds,
      models: models || {},
      router: (router && router.stats) || {},
      capabilities: capabilities || null,
    };
    state.system = next;
    // Derive start time so we can tick uptime locally.
    if (next.uptime_seconds != null) {
      systemStartTime = Date.now() - next.uptime_seconds * 1000;
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
  // The registry-core loop is collapsed into the __system__ pseudo-node, so it
  // is not a "live loop" from the operator's view. Count only loops that get a
  // visible node, so the empty state still surfaces the genuine
  // running-but-zero-loops condition instead of being hidden by the lone core.
  const visibleLoops = hasSystem ? loops.filter((l) => !isRegistryCoreLoop(l)) : loops;
  emptyState.hidden = visibleLoops.length > 0;

  // Canvas center — used as gravity anchor and for new-node spawn.
  const rect = refreshCanvasViewport() || getLayoutViewportRect();
  const cx = rect.cx;
  const cy = rect.cy;

  // Sync physics state with current loops (add new, remove stale).
  syncPhysicsNodes(cx, cy);

  // Detect new nodes for enter animation.
  const newIds = new Set();
  for (const loop of loops) {
    if (!state.knownLoopIds.has(loop.id)) newIds.add(loop.id);
  }

  // Create/update DOM nodes (no position-setting — physics handles that).
  for (const loop of loops) {
    // Same registry-core suppression as syncPhysicsNodes: the loop
    // exists in state, but its visual slot is the __system__ node.
    if (hasSystem && isRegistryCoreLoop(loop)) continue;
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

  // Boot-order race cleanup: if /v1/system arrived after a first
  // paint that already rendered the registry-core loop, drop the
  // stale SVG group and knownLoopIds entry immediately (no exit
  // animation — the node was a visual error, not a graceful exit).
  if (hasSystem) {
    for (const loop of loops) {
      if (!isRegistryCoreLoop(loop)) continue;
      const stale = canvasNodeLayer.querySelector(`[data-loop-id="${loop.id}"]`);
      if (stale) stale.remove();
      state.knownLoopIds.delete(loop.id);
    }
  }

  // Remove nodes for loops that no longer exist (with exit animation).
  const existingGroups = canvasNodeLayer.querySelectorAll('.loop-node');
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
    const existing = canvasNodeLayer.querySelector('.system-node');
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
  // When the registry's core loop is present, its loop node is hidden
  // (see isRegistryCoreLoop) and its visual slot is the __system__
  // node. Any loop that named the registry core as its parent gets
  // re-rooted to __system__ for edge-drawing purposes; the registry
  // core itself is excluded from both top-level and children sets.
  const registryCore = hasSystem ? loops.find(isRegistryCoreLoop) : null;
  const registryCoreID = registryCore ? registryCore.id : null;
  const adoptedBySystem = (loop) => loop.parent_id === registryCoreID;

  // Top-level loops: genuine orphans plus loops adopted from the
  // hidden registry core. The registry core itself does not appear.
  const topLevel = loops.filter(l => {
    if (isRegistryCoreLoop(l)) return false;
    if (!l.parent_id) return true;
    return adoptedBySystem(l);
  });
  const activeIds = new Set(topLevel.map(l => l.id));

  // Child loops: anything with a parent_id that is NOT the registry
  // core. Their edges go to the named parent normally.
  const children = loops.filter(l => {
    if (isRegistryCoreLoop(l)) return false;
    if (!l.parent_id) return false;
    return !adoptedBySystem(l);
  });
  const childKeys = new Set(children.map(l => l.id));

  // Build a set of all valid link targets.
  const allValidTargets = new Set([...activeIds, ...childKeys]);

  // Remove stale link lines. Three cases require cleanup, not just one:
  //
  //   1. system→child line whose target no longer exists OR system is gone
  //   2. parent→child line whose target loop is gone
  //   3. parent→child line whose *parent* has gone away (this catches the
  //      registry-core-adoption case: a child that previously listed the
  //      registry core as its parent now belongs to __system__, but the
  //      old child-of-core line wasn't being pruned because the child
  //      itself still existed)
  const existing = canvasEdgeLayer.querySelectorAll('.link-line');
  for (const el of existing) {
    const target = el.dataset.targetLoop;
    const parentLoop = el.dataset.parentLoop || '';
    const isSystemLink = !parentLoop;
    if (isSystemLink) {
      if (!hasSystem || !activeIds.has(target)) {
        el.remove();
      }
      continue;
    }
    if (!allValidTargets.has(target)) {
      el.remove();
      continue;
    }
    // Re-rooting check: if the declared parent is no longer a valid
    // parent for the current target (target moved, was adopted, or
    // the parent is the now-hidden registry core), drop the line.
    const targetLoop = state.loops.get(target);
    if (!targetLoop || targetLoop.parent_id !== parentLoop || parentLoop === registryCoreID) {
      el.remove();
    }
  }

  // System → top-level lines.
  if (hasSystem) {
    for (const loop of topLevel) {
      let line = canvasEdgeLayer.querySelector(`.link-line[data-target-loop="${loop.id}"]:not([data-parent-loop])`);

      if (!line) {
        line = createSVG('line', {
          class: 'link-line',
          'data-target-loop': loop.id,
        });
        canvasEdgeLayer.appendChild(line);
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
    let line = canvasEdgeLayer.querySelector(selector);

    if (!line) {
      line = createSVG('line', {
        class: 'link-line link-line--child',
        'data-target-loop': child.id,
        'data-parent-loop': child.parent_id,
      });
      canvasEdgeLayer.appendChild(line);
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
  const lines = canvasEdgeLayer.querySelectorAll(selector);
  for (const line of lines) {
    const cur = line.getAttribute('class') || '';
    line.setAttribute('class', cur.replace(' link-line--flash', '') + ' link-line--flash');
    setTimeout(() => {
      // Strip only the flash modifier from the CURRENT class, so a render that
      // ran during the flash (e.g. the line entering error/degraded) isn't
      // clobbered by a stale captured base class.
      line.setAttribute('class', (line.getAttribute('class') || '').replace(' link-line--flash', ''));
    }, 300);
  }
}

// setNodeData writes a data-* attribute only when it changed, keeping the
// per-frame projection cheap (no redundant attribute churn).
function setNodeData(dataset, key, value) {
  if (dataset[key] !== value) dataset[key] = value;
}

// projectLoopCharacteristics is the single seam between loop data and node
// styling: it projects a loop's semantic characteristics onto the outer <g>
// as data-* attributes so CSS — and future themes/skins/layers — drive fill,
// ring, sigil visibility, animation, etc. off the cascade rather than off
// imperative branches here. Geometry (r/cx/cy) stays imperative in renderNode
// because SVG cannot read CSS custom properties for layout. We project only
// facts already on the wire; owner/sigil await dedicated spec fields.
function projectLoopCharacteristics(loop, capacity, visualState, group) {
  const d = group.dataset;
  setNodeData(d, 'operation', getLoopOperationType(loop));
  setNodeData(d, 'category', normalizeVisualCategory(getLoopCategory(loop)));
  setNodeData(d, 'state', visualState);
  setNodeData(d, 'contextTier', capacity.key);
  // Backing kind: 'llm' runs a model (sized by it), 'go' is a pure-Go loop
  // (lightweight), 'container' is a structural grouping.
  setNodeData(d, 'backing',
    capacity.basis === 'go' ? 'go'
      : capacity.basis === 'container' ? 'container'
      : 'llm');
  setNodeData(d, 'contextWindow', String(capacity.contextWindow || 0));
  setNodeData(d, 'relation', loop.parent_id ? 'child' : 'root');
  const tz = loop.config && loop.config.Metadata && loop.config.Metadata.trust_zone;
  if (tz && TRUST_ZONES.has(tz)) {
    setNodeData(d, 'trustZone', tz);
  } else if (d.trustZone !== undefined) {
    delete d.trustZone;
  }
}

function renderNode(loop) {
  const category = normalizeVisualCategory(getLoopCategory(loop));
  const capacity = getLoopVisualCapacity(loop);
  const nodeR = capacity.radius;
  const ringR = nodeR + 12;
  let group = canvasNodeLayer.querySelector(`[data-loop-id="${loop.id}"]`);

  if (!group) {
    group = createSVG('g', {
      class: 'loop-node',
      'data-loop-id': loop.id,
    });
    group.addEventListener('click', () => {
      if (Date.now() < suppressNextNodeClickUntil) return;
      selectLoop(loop.id);
    });
    group.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      showContextMenu(e.clientX, e.clientY, buildLoopContextMenu(loop));
    });
    group.addEventListener('pointerdown', (e) => {
      scheduleNodeLongPress(e, {
        select: () => focusLoop(loop.id),
        show: (x, y) => showContextMenu(x, y, buildLoopContextMenu(loop)),
      });
    });
    group.addEventListener('pointermove', updateNodeLongPress);
    group.addEventListener('pointerup', clearNodeLongPress);
    group.addEventListener('pointercancel', clearNodeLongPress);
    group.addEventListener('pointerleave', clearNodeLongPress);

    // Inner group for enter/exit scale animation (children drawn at origin).
    const inner = createSVG('g', { class: 'node-inner' });

    // Native SVG tooltip — instant, no delay.
    const title = createSVG('title', {});
    title.textContent = buildLoopNodeTitle(loop, capacity);
    inner.appendChild(title);

    // Trust zone underglow — diffused coloured circle behind the node.
    const trustZone = loop.config && loop.config.Metadata && loop.config.Metadata.trust_zone;
    if (trustZone && TRUST_ZONES.has(trustZone)) {
      const glow = createSVG('circle', {
        class: 'trust-glow trust-glow--' + trustZone,
        r: nodeR + 3,
        filter: 'url(#trust-blur)',
      });
      inner.appendChild(glow);
    }

    const selectionRing = createSVG('circle', {
      class: 'selection-ring',
      r: nodeR + 18,
      fill: 'none',
    });

    const occlusionDisk = createSVG('circle', {
      class: 'node-occlusion',
      r: nodeR + 13,
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

    // Context-fill ring — an inner arc showing live window utilization
    // (input_tokens / context_window). Capacity is the node's size; this is the
    // moment-to-moment usage. Containers / non-LLM nodes have none.
    const fillR = nodeR * 0.72;
    const fillCirc = 2 * Math.PI * fillR;
    const fillFrac = getLoopContextFill(loop);
    const fillRing = createSVG('circle', {
      class: 'fill-ring',
      r: fillR,
      'stroke-dasharray': fillCirc,
      'stroke-dashoffset': fillCirc * (1 - fillFrac),
    });
    // Color by pressure: green (roomy) -> orange -> red (nearly full).
    fillRing.style.stroke = fillFrac >= 0.8 ? 'var(--red)'
      : (fillFrac >= 0.5 ? 'var(--orange)' : 'var(--green)');
    if (capacity.basis !== 'context' || fillFrac <= 0) {
      fillRing.style.display = 'none';
    }

    // Main shape — always a circle.
    const shapeEl = createNodeShape(category, nodeR);

    // Category icon centered inside the node.
    const icon = createSVG('text', {
      class: 'node-icon',
      'text-anchor': 'middle',
      'dominant-baseline': 'central',
      'font-size': Math.round(nodeR * 0.5),
    });
    icon.textContent = loopSigil(loop);

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
    inner.appendChild(occlusionDisk);
    inner.appendChild(ring);
    inner.appendChild(sleepRing);
    inner.appendChild(shapeEl);
    inner.appendChild(fillRing);
    inner.appendChild(icon);
    inner.appendChild(supDot);
    inner.appendChild(rimBadge);
    inner.appendChild(label);
    group.appendChild(inner);
    canvasNodeLayer.appendChild(group);

    // Mark as known — enter animation is triggered by renderNodes().
    state.knownLoopIds.add(loop.id);
  }

  // Update trust zone underglow colour if it changed or appeared.
  const trustZone = loop.config && loop.config.Metadata && loop.config.Metadata.trust_zone;
  const glowEl = group.querySelector('.trust-glow');
  if (trustZone && TRUST_ZONES.has(trustZone)) {
    if (glowEl) {
      glowEl.setAttribute('class', 'trust-glow trust-glow--' + trustZone);
      glowEl.setAttribute('r', nodeR + 3);
    } else {
      // Trust zone appeared after initial render — insert glow.
      const inner = group.querySelector('.node-inner');
      const glow = createSVG('circle', {
        class: 'trust-glow trust-glow--' + trustZone,
        r: nodeR + 3,
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
    group.querySelector('.node-occlusion').setAttribute('r', nodeR + 13);
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

  const shapeEl = group.querySelector('.node-shape');
  const iconEl = group.querySelector('.node-icon');
  const visualState = getLoopVisualState(loop);
  // Project semantic characteristics onto the node (data-* attrs) — the single
  // seam CSS/themes drive visuals from. Owns data-category and data-state.
  projectLoopCharacteristics(loop, capacity, visualState, group);
  shapeEl.setAttribute('class', 'node-shape node-shape--category-' + category + ' node-shape--activity-' + visualState);
  iconEl.textContent = loopSigil(loop);
  iconEl.setAttribute('class', 'node-icon node-icon--' + category);

  const ring = group.querySelector('.node-ring');
  ring.setAttribute('class', 'node-ring node-ring--' + visualState);

  // Keep the context-fill ring current. Utilization (arc length + pressure
  // color) changes as iterations arrive, and the radius changes when the node
  // resizes (e.g. a model change), so recompute every render instead of
  // freezing the build-time values.
  const fillRing = group.querySelector('.fill-ring');
  if (fillRing) {
    const fillFrac = getLoopContextFill(loop);
    if (capacity.basis !== 'context' || fillFrac <= 0) {
      fillRing.style.display = 'none';
    } else {
      const fillR = nodeR * 0.72;
      const fillCirc = 2 * Math.PI * fillR;
      fillRing.style.display = '';
      fillRing.setAttribute('r', fillR);
      fillRing.setAttribute('stroke-dasharray', fillCirc);
      fillRing.setAttribute('stroke-dashoffset', fillCirc * (1 - fillFrac));
      fillRing.style.stroke = fillFrac >= 0.8 ? 'var(--red)'
        : (fillFrac >= 0.5 ? 'var(--orange)' : 'var(--green)');
    }
  }

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
  // Containers never sleep; their ring is hidden in CSS — skip the per-tick
  // offset math entirely.
  if (group.dataset.operation === 'container') return;
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
  let group = canvasNodeLayer.querySelector('.system-node');

  if (!group) {
    group = createSVG('g', { class: 'system-node' });
    group.addEventListener('click', () => {
      if (Date.now() < suppressNextNodeClickUntil) return;
      selectSystem();
    });
    group.addEventListener('contextmenu', (e) => {
      e.preventDefault();
      showContextMenu(e.clientX, e.clientY, buildSystemContextMenu(sys));
    });
    group.addEventListener('pointerdown', (e) => {
      scheduleNodeLongPress(e, {
        select: () => focusSystem(),
        show: (x, y) => showContextMenu(x, y, buildSystemContextMenu(sys)),
      });
    });
    group.addEventListener('pointermove', updateNodeLongPress);
    group.addEventListener('pointerup', clearNodeLongPress);
    group.addEventListener('pointercancel', clearNodeLongPress);
    group.addEventListener('pointerleave', clearNodeLongPress);

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

    canvasNodeLayer.appendChild(group);
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
  const uptimeEl = detailEntity.querySelector('[data-live-system-uptime]');
  if (!uptimeEl) return;
  if (systemStartTime === null) {
    uptimeEl.textContent = state.system ? (formatUptime(state.system.uptime_seconds) || '-') : '-';
    return;
  }
  const ms = Date.now() - systemStartTime;
  uptimeEl.textContent = formatUptimeLong(ms);
}

// ---------------------------------------------------------------------------
// Rendering — Detail Panel
// ---------------------------------------------------------------------------

function makeSchemaCardModeIcon(mode) {
  const bars = mode === 'title'
    ? [{ y: 10, h: 4 }]
    : mode === 'widget'
      ? [{ y: 6, h: 4 }, { y: 14, h: 4 }]
      : [{ y: 4, h: 3.5 }, { y: 10.25, h: 3.5 }, { y: 16.5, h: 3.5 }];
  const rects = bars
    .map(({ y, h }) => `<rect x="4" y="${y}" width="16" height="${h}" rx="1.8" ry="1.8"></rect>`)
    .join('');
  return `
    <svg viewBox="0 0 24 24" aria-hidden="true" class="schema-card__control-icon">
      ${rects}
    </svg>
  `;
}

function makeSchemaCompactFacts(items, className = 'schema-card__facts') {
  const values = (items || []).filter(Boolean);
  if (values.length === 0) return null;
  const wrap = document.createElement('div');
  wrap.className = className;
  for (const item of values) {
    const fact = document.createElement('span');
    fact.className = 'schema-card__fact';
    if (typeof item === 'string' || typeof item === 'number') {
      fact.textContent = String(item);
    } else {
      const label = item.label ? String(item.label).trim() : '';
      const value = item.value === null || item.value === undefined ? '' : String(item.value).trim();
      fact.textContent = label && value ? `${label}: ${value}` : (value || label);
    }
    if (!fact.textContent) continue;
    wrap.appendChild(fact);
  }
  return wrap.childNodes.length ? wrap : null;
}

function makeSchemaWidgetGrid(facts) {
  const items = (facts || []).filter((item) => item && item.value !== null && item.value !== undefined && item.value !== '');
  if (items.length === 0) return null;
  const grid = document.createElement('div');
  grid.className = 'schema-widget-grid';
  for (const item of items) {
    const cell = document.createElement('div');
    cell.className = 'schema-widget-metric';
    const value = document.createElement('div');
    value.className = 'schema-widget-metric__value';
    value.textContent = String(item.value);
    cell.appendChild(value);
    if (item.label) {
      const label = document.createElement('div');
      label.className = 'schema-widget-metric__label';
      label.textContent = String(item.label);
      cell.appendChild(label);
    }
    grid.appendChild(cell);
  }
  return grid;
}

function makeSchemaCard(title, meta, opts = {}) {
  const card = document.createElement('section');
  card.className = 'detail-card schema-card';
  const isResizable = opts.resizable !== false;
  if (isResizable) {
    card.classList.add('schema-card--resizable');
    card.dataset.entityKind = opts.entityKind || '';
    card.dataset.cardKey = opts.key || title.toLowerCase().replace(/[^a-z0-9]+/g, '-');
  }

  const header = document.createElement('div');
  header.className = 'schema-card__header';

  const heading = document.createElement('div');
  heading.className = 'schema-card__heading';

  const titleEl = document.createElement('h3');
  titleEl.className = 'schema-card__title';
  titleEl.textContent = title;
  heading.appendChild(titleEl);

  if (meta) {
    const metaEl = document.createElement('span');
    metaEl.className = 'schema-card__meta';
    metaEl.textContent = meta;
    heading.appendChild(metaEl);
  }

  header.appendChild(heading);

  if (isResizable) {
    const controls = document.createElement('div');
    controls.className = 'schema-card__controls';
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'schema-card__control';
    btn.textContent = 'Full';
    btn.title = 'Switch card view';
    btn.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      const current = card.dataset.layoutMode || 'full';
      applySchemaCardPreset(card, nextSchemaCardMode(current));
    });
    controls.appendChild(btn);

    header.appendChild(controls);
  }

  card.appendChild(header);

  const titleShell = document.createElement('div');
  titleShell.className = 'schema-card__title-shell';
  const titleSummary = (opts.titleSummary || '').trim();
  if (titleSummary) {
    const summaryEl = document.createElement('p');
    summaryEl.className = 'schema-card__summary';
    summaryEl.textContent = titleSummary;
    titleShell.appendChild(summaryEl);
  }
  const titleFacts = makeSchemaCompactFacts(opts.titleFacts, 'schema-card__facts schema-card__facts--title');
  if (titleFacts) titleShell.appendChild(titleFacts);
  card.appendChild(titleShell);

  const widgetShell = document.createElement('div');
  widgetShell.className = 'schema-card__widget-shell';
  const widgetSummary = (opts.widgetSummary || opts.titleSummary || '').trim();
  if (widgetSummary) {
    const summaryEl = document.createElement('p');
    summaryEl.className = 'schema-card__summary schema-card__summary--widget';
    summaryEl.textContent = widgetSummary;
    widgetShell.appendChild(summaryEl);
  }
  const widgetGrid = makeSchemaWidgetGrid(opts.widgetFacts);
  if (widgetGrid) widgetShell.appendChild(widgetGrid);
  const widgetFacts = makeSchemaCompactFacts(opts.widgetNotes, 'schema-card__facts schema-card__facts--widget');
  if (widgetFacts) widgetShell.appendChild(widgetFacts);
  card.appendChild(widgetShell);

  const bodyShell = document.createElement('div');
  bodyShell.className = 'schema-card__body-shell';

  const body = document.createElement('div');
  body.className = 'schema-card__body';
  bodyShell.appendChild(body);

  if (isResizable) {
    const fade = document.createElement('div');
    fade.className = 'schema-card__fade';
    bodyShell.appendChild(fade);
  }

  card.appendChild(bodyShell);

  return { card, body, header, titleShell, widgetShell };
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
  if (opts.valueAttrs) {
    for (const [name, attrValue] of Object.entries(opts.valueAttrs)) {
      if (attrValue === null || attrValue === undefined) continue;
      valueEl.setAttribute(name, String(attrValue));
    }
  }
  row.appendChild(valueEl);
  body.appendChild(row);
  return { row, valueEl };
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

function makeInspectorUtility(label, content) {
  const wrap = document.createElement('div');
  wrap.className = 'inspector-utility';

  const labelEl = document.createElement('span');
  labelEl.className = 'inspector-utility__label';
  labelEl.textContent = label;
  wrap.appendChild(labelEl);

  if (typeof content === 'string' || typeof content === 'number' || typeof content === 'boolean') {
    const valueEl = document.createElement('span');
    valueEl.className = 'inspector-utility__value';
    valueEl.textContent = String(content);
    wrap.appendChild(valueEl);
  } else if (content) {
    wrap.appendChild(content);
  }

  return wrap;
}

function copyLoopEntityJSON(loop) {
  const entity = buildLoopEntity(loop);
  const conversationID = getLoopPrimaryConversationID(entity);
  const conversation = conversationID ? (state.conversationDetails.get(conversationID) || null) : null;
  const history = state.iterationHistory.get(loop.id) || [];
  const payload = {
    exported_at: new Date().toISOString(),
    entity,
    loop,
    conversation,
    iteration_history: history,
  };
  return navigator.clipboard.writeText(JSON.stringify(payload, null, 2));
}

function copySystemEntityJSON(sys) {
  const entity = buildSystemEntity(sys || state.system || {});
  const payload = {
    exported_at: new Date().toISOString(),
    entity,
    system: sys || state.system || null,
    loops: Array.from(state.loops.values()),
  };
  return navigator.clipboard.writeText(JSON.stringify(payload, null, 2));
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
  details.dataset.detailKey = 'conversation:' + summary.id;

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

function getConversationSummaryDetail(conversationID) {
  if (!conversationID) return null;
  ensureConversationSummary(conversationID);
  return state.conversationDetails.get(conversationID) || buildPendingConversationSummary(conversationID);
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
  const unhealthyCount = Math.max(0, entity.serviceCount - entity.readyCount);

  const hero = document.createElement('section');
  hero.className = 'detail-card schema-card schema-card--hero';
  hero.innerHTML = `
    <div class="schema-hero">
      <div class="schema-hero__copy">
        <div class="schema-kind">${entity.kind}</div>
        <h2 class="detail-name">${escapeHTML(entity.title)}</h2>
        <div class="schema-subtitle">
          <strong>${formatNumber(entity.liveLoopCount)} live loops</strong>
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

  const identity = makeSchemaCard('Identity', 'Build lineage, process identity, and runtime provenance', {
    entityKind: entity.kind,
    key: 'identity',
    titleSummary: `${formatSchemaToken(entity.state)} · ${entity.version || 'dev build'} · ${entity.uptime || 'uptime pending'}`,
    titleFacts: [entity.commit ? shortID(entity.commit) : '', entity.arch || '', entity.goVersion || ''].filter(Boolean),
    widgetFacts: [
      { label: 'Status', value: formatSchemaToken(entity.state) },
      { label: 'Version', value: entity.version || 'dev' },
      { label: 'Uptime', value: entity.uptime || 'pending' },
      entity.commit ? { label: 'Commit', value: shortID(entity.commit) } : null,
    ],
  });
  appendSchemaRow(identity.body, 'anchor kind', entity.kind);
  appendSchemaRow(identity.body, 'status', formatSchemaToken(entity.state));
  appendSchemaRow(identity.body, 'uptime', entity.uptime, {
    valueAttrs: { 'data-live-system-uptime': 'true' },
  });
  appendSchemaRow(identity.body, 'version', entity.version);
  if (entity.commit) {
    appendSchemaRow(identity.body, 'commit', makeSchemaIDList([entity.commit], { maxVisible: 1 }));
  }
  appendSchemaRow(identity.body, 'go', entity.goVersion);
  appendSchemaRow(identity.body, 'arch', entity.arch);
  detailEntity.appendChild(identity.card);

  const topology = makeSchemaCard('Topology', 'Live graph shape and routing footprint', {
    entityKind: entity.kind,
    key: 'topology',
    titleSummary: `${formatNumber(entity.liveLoopCount)} live loops across ${formatNumber(entity.serviceCount)} services.`,
    titleFacts: [entity.routingMode, entity.defaultModel || '', `${formatNumber(entity.rootLoopCount)} root`].filter(Boolean),
    widgetFacts: [
      { label: 'Live', value: formatNumber(entity.liveLoopCount) },
      { label: 'Root', value: formatNumber(entity.rootLoopCount) },
      { label: 'Child', value: formatNumber(entity.childLoopCount) },
      { label: 'Requests', value: formatNumber(entity.totalRequests) },
    ],
  });
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

  const services = makeSchemaCard('Services', 'Service health, strain, and recovery state', {
    entityKind: entity.kind,
    key: 'services',
    titleSummary: unhealthyCount > 0
      ? `${formatNumber(entity.readyCount)} ready · ${formatNumber(unhealthyCount)} degraded or down.`
      : `${formatNumber(entity.readyCount)} of ${formatNumber(entity.serviceCount)} services ready.`,
    titleFacts: [entity.serviceCount ? `${formatNumber(entity.serviceCount)} total` : '', unhealthyCount ? `${formatNumber(unhealthyCount)} impacted` : 'all healthy'].filter(Boolean),
    widgetFacts: [
      { label: 'Ready', value: formatNumber(entity.readyCount) },
      { label: 'Total', value: formatNumber(entity.serviceCount) },
      { label: 'Impacted', value: formatNumber(unhealthyCount) },
      { label: 'State', value: formatSchemaToken(entity.state) },
    ],
  });
  const servicesEl = document.createElement('div');
  servicesEl.className = 'system-services';
  renderSystemServices(servicesEl, entity.health);
  services.body.appendChild(servicesEl);
  detailEntity.appendChild(services.card);

  const registries = makeSchemaCard('Registries', 'Focused windows for runtime catalogs and operator inventories', {
    entityKind: entity.kind,
    key: 'registries',
    titleSummary: 'Open dedicated windows for toolbox, capability, and model inventory inspection instead of carrying the full registries in the core pane.',
    titleFacts: [entity.defaultModel ? `default ${entity.defaultModel}` : '', `gen ${formatNumber(entity.registryGeneration)}`].filter(Boolean),
    widgetFacts: [
      { label: 'Capabilities', value: formatNumber(entity.capabilityCount) },
      { label: 'Tools', value: formatNumber(entity.toolboxToolCount) },
      { label: 'Resources', value: formatNumber(entity.resourceCount) },
      { label: 'Deployments', value: formatNumber(entity.deploymentCount) },
    ],
  });
  const registriesMeta = document.createElement('span');
  registriesMeta.className = 'schema-card__meta';
  const registriesHeading = registries.header.querySelector('.schema-card__heading');
  if (registriesHeading) registriesHeading.appendChild(registriesMeta);

  const registriesSummary = document.createElement('div');
  registriesSummary.className = 'system-summary-grid';
  registries.body.appendChild(registriesSummary);

  const registriesWrap = document.createElement('div');
  registriesWrap.className = 'schema-subsection';
  registriesWrap.innerHTML = '<h4 class="schema-subsection__title">Core Registries</h4>';
  const registriesList = document.createElement('div');
  registriesList.className = 'system-list';
  registriesWrap.appendChild(registriesList);
  registries.body.appendChild(registriesWrap);

  renderSystemRegistries(
    registriesSummary,
    registriesList,
    registriesMeta,
    sys,
    {},
  );
  detailEntity.appendChild(registries.card);

  updateSystemUptime();
}

// ---------------------------------------------------------------------------
// Tier 0 — the glance header.
// An always-on, zero-click read that answers what the NODE itself can't already
// encode: an error STREAK (not just an "error" hue), what the loop is doing
// right NOW (model phase vs tool phase, ticking), context pressure, and next
// wake. Replaces the hero / identity / relationship cards. Prefers the
// loop.view (LoopView) projection and falls back to existing fields when the
// server hasn't shipped `view` yet, so it degrades cleanly across the deploy.

function glanceMetric(label, fill) {
  const m = document.createElement('div');
  m.className = 'glance-metric';
  const l = document.createElement('div');
  l.className = 'glance-metric__label';
  l.textContent = label;
  m.appendChild(l);
  const v = document.createElement('div');
  v.className = 'glance-metric__value';
  fill(v);
  m.appendChild(v);
  return m;
}

// makeSparkline draws a word-sized trend line from a value series (oldest→newest).
function makeSparkline(values, w, h) {
  const max = Math.max(...values);
  const min = Math.min(...values);
  const range = (max - min) || 1;
  const n = values.length;
  const pts = values.map((val, i) => {
    const x = n > 1 ? (i / (n - 1)) * (w - 2) + 1 : w / 2;
    const y = h - 1 - ((val - min) / range) * (h - 2);
    return x.toFixed(1) + ',' + y.toFixed(1);
  }).join(' ');
  const svg = createSVG('svg', { class: 'glance-spark', width: w, height: h, viewBox: '0 0 ' + w + ' ' + h });
  svg.appendChild(createSVG('polyline', { points: pts, fill: 'none' }));
  return svg;
}

// makeFillBar renders a filled-to-ceiling bar, hued by pressure past comfortable.
function makeFillBar(pct) {
  const clamped = Math.max(0, Math.min(100, pct));
  const bar = document.createElement('div');
  bar.className = 'glance-fill';
  const fill = document.createElement('div');
  fill.className = 'glance-fill__fill';
  fill.style.width = clamped + '%';
  fill.style.background = pct >= 85 ? 'var(--red)' : pct >= 60 ? 'var(--orange)' : 'var(--text-secondary)';
  bar.appendChild(fill);
  return bar;
}

// parseDurationSecs reads a signed-second LoopView string ("5940s", "+5953s",
// "-12s") into seconds (sign preserved); null when absent/unparseable.
function parseDurationSecs(str) {
  if (!str) return null;
  const m = String(str).match(/^([+-]?)(\d+)s$/);
  if (!m) return null;
  return (m[1] === '-' ? -1 : 1) * parseInt(m[2], 10);
}

// renderSleepScrubber draws the loop's sleep cycle as a non-interactive
// timeline: a track from 0 to the latest allowed wake (SleepMax), a hatched
// locked zone before SleepMin, a marker at the chosen wake, and a playhead at
// elapsed crawling toward it — red and past the marker when overdue. Chosen +
// elapsed prefer the LoopView projection and fall back to the client's observed
// sleep timer; bounds come from the loop config (nanoseconds). Returns null
// when there is no timer sleep to show (event-driven, or no wake data yet).
function renderSleepScrubber(loop) {
  if (loop.event_driven) return null;
  const view = loop.view || {};
  const cfg = loop.config || {};
  const ns2s = (ns) => (typeof ns === 'number' && ns > 0) ? ns / 1e9 : null;
  const minS = parseDurationSecs(view.sleep_min) || ns2s(cfg.SleepMin);
  const maxS = parseDurationSecs(view.sleep_max) || ns2s(cfg.SleepMax);

  let chosenS = parseDurationSecs(view.current_sleep_duration);
  const toWakeView = parseDurationSecs(view.next_wake_delta);
  let toWakeS = toWakeView;
  let elapsedS = (chosenS != null && toWakeView != null) ? (chosenS - toWakeView) : null;
  if (chosenS == null) {
    const timer = state.sleepTimers.get(loop.id);
    if (timer && timer.durationMs > 0) {
      chosenS = timer.durationMs / 1000;
      elapsedS = (Date.now() - timer.startedAt) / 1000;
      toWakeS = chosenS - elapsedS;
    }
  }
  if (chosenS == null || elapsedS == null || chosenS <= 0) return null;

  // Axis = the chosen sleep (0 → chosen = the wake at the right edge). Anchoring
  // to the choice rather than SleepMax keeps a short choice from squishing
  // against a far-larger ceiling; the ceiling rides in the label instead. The
  // locked zone + min tick draw only when there is a real "can't wake before
  // SleepMin yet, but chose to sleep past it" gap — otherwise (fixed-interval
  // pollers where min ≈ chosen) the whole track would read as locked.
  const pct = (s) => Math.max(0, Math.min(100, (s / chosenS) * 100));
  const overdue = elapsedS > chosenS + 1;
  const showMin = minS != null && minS > 0 && minS < chosenS * 0.98;
  const showMaxLabel = maxS != null && maxS > chosenS * 1.1;

  const wrap = document.createElement('div');
  wrap.className = 'sleep-scrubber' + (overdue ? ' sleep-scrubber--overdue' : '');

  const label = document.createElement('div');
  label.className = 'sleep-scrubber__label';
  const chose = document.createElement('span');
  chose.textContent = 'chose ' + formatDuration(Math.round(chosenS) * 1000)
    + (showMaxLabel ? '  ·  max ' + formatDuration(Math.round(maxS) * 1000) : '');
  label.appendChild(chose);
  const eta = document.createElement('span');
  eta.className = 'sleep-scrubber__eta';
  eta.textContent = overdue
    ? ('overdue ' + formatDuration(Math.round(elapsedS - chosenS) * 1000))
    : ('wakes in ' + formatDuration(Math.max(0, Math.round(toWakeS)) * 1000));
  label.appendChild(eta);
  wrap.appendChild(label);

  const track = document.createElement('div');
  track.className = 'sleep-scrubber__track';
  if (showMin) {
    const locked = document.createElement('div');
    locked.className = 'sleep-scrubber__locked';
    locked.style.width = pct(minS) + '%';
    track.appendChild(locked);
  }
  const fill = document.createElement('div');
  fill.className = 'sleep-scrubber__fill';
  fill.style.width = pct(elapsedS) + '%';
  track.appendChild(fill);
  const head = document.createElement('div');
  head.className = 'sleep-scrubber__head';
  head.style.left = pct(elapsedS) + '%';
  track.appendChild(head);
  if (showMin) {
    const minTick = document.createElement('div');
    minTick.className = 'sleep-scrubber__min';
    minTick.style.left = pct(minS) + '%';
    track.appendChild(minTick);
  }
  wrap.appendChild(track);

  return wrap;
}

function renderLoopGlanceHeader(loop) {
  const view = loop.view || {};
  const header = document.createElement('section');
  header.className = 'glance-header';
  header.dataset.state = getLoopVisualState(loop);

  // Identity: status dot (hue agrees with the node) + name + mono meta line.
  const idLine = document.createElement('div');
  idLine.className = 'glance-id';
  const dot = document.createElement('span');
  dot.className = 'glance-dot';
  idLine.appendChild(dot);
  const name = document.createElement('span');
  name.className = 'glance-name';
  name.textContent = (loop.name || loop.id || '').split('/').pop() || loop.id || 'loop';
  idLine.appendChild(name);
  const meta = document.createElement('span');
  meta.className = 'glance-meta';
  const model = loop._liveModel || loop._lastModel || (getLoopLatestSnapshot(loop) || {}).model || '';
  meta.textContent = [shortID(loop.id), describeLoopExecutionMode(loop), model ? shortModelName(model) : null]
    .filter(Boolean).join('  ·  ');
  idLine.appendChild(meta);
  header.appendChild(idLine);

  // Error STREAK — draws nothing at zero (empty is the datum).
  const errs = loop.consecutive_errors || 0;
  if (errs > 0) {
    const alert = document.createElement('div');
    alert.className = 'glance-alert';
    const count = document.createElement('span');
    count.className = 'glance-alert__count';
    count.textContent = 'errors ' + errs;
    alert.appendChild(count);
    if (loop.last_error) {
      const msg = document.createElement('span');
      msg.className = 'glance-alert__msg';
      msg.textContent = truncate(String(loop.last_error).split('\n')[0], 76);
      msg.title = loop.last_error;
      alert.appendChild(msg);
    }
    header.appendChild(alert);
  }

  // Live phase badge — only while processing; the model-vs-tool read, erased
  // when idle. Elapsed ticks from the turn start (per-phase timestamps are a
  // later slice — this is the whole-turn interim).
  if (loop.state === 'processing') {
    const running = (loop._liveTools || []).find((t) => !t.status || t.status === 'running');
    let label = '';
    if (running && running.tool) label = 'waiting on ' + running.tool;
    else if (loop._liveModel) label = 'thinking on ' + shortModelName(loop._liveModel);
    if (label) {
      const phase = document.createElement('div');
      phase.className = 'glance-phase' + (running ? ' glance-phase--tool' : '');
      const lbl = document.createElement('span');
      lbl.className = 'glance-phase__label';
      lbl.textContent = label;
      phase.appendChild(lbl);
      if (loop._iterStartTs) {
        const el = document.createElement('span');
        el.className = 'glance-phase__elapsed';
        el.textContent = formatDuration(Date.now() - loop._iterStartTs);
        phase.appendChild(el);
      }
      header.appendChild(phase);
    }
  }

  // Metrics: iterations + cadence · context fill · next wake.
  const metrics = document.createElement('div');
  metrics.className = 'glance-metrics';

  // Iteration count is the honest Tier-0 progress signal; per-turn duration
  // variation (where a sparkline earns its ink) lives in the Tier-1 small
  // multiples, a fixed frame where a real slow turn spikes against uniform
  // neighbors instead of an auto-scaled line amplifying steady-state jitter.
  const iters = loop.iterations || 0;
  metrics.appendChild(glanceMetric('iterations', (v) => {
    v.textContent = formatNumber(iters);
  }));

  const cw = loop.context_window || view.context_window || 0;
  const used = loop.last_input_tokens || (loop._llmContext && loop._llmContext.est_tokens) || 0;
  const pct = (view.context_fill_pct != null) ? view.context_fill_pct
    : (cw > 0 && used > 0 ? Math.round((used * 100) / cw) : null);
  if (pct != null) {
    metrics.appendChild(glanceMetric('context', (v) => {
      v.appendChild(makeFillBar(pct));
      const lbl = document.createElement('span');
      lbl.className = 'glance-fill-label';
      lbl.textContent = cw > 0 ? (formatTokens(used) + ' / ' + formatTokens(cw)) : (pct + '%');
      v.appendChild(lbl);
    }));
  }

  header.appendChild(metrics);

  // The sleep scrubber subsumes a plain "wakes in X" cell — a full-width
  // timeline of the sleep cycle. Shown only for timer loops with wake data.
  const scrubber = renderSleepScrubber(loop);
  if (scrubber) header.appendChild(scrubber);

  return header;
}

// renderIterationStrip — the Tier 1 small multiples. The last ~10 completed
// turns as identical rows on a SHARED scale, so a genuinely slow or token-heavy
// turn spikes against uniform neighbors (the fixed frame the header sparkline
// lacked). No per-tool chips — tool itemization is a Tier 2 / catalog concern.
function renderIterationStrip(loop) {
  const history = state.iterationHistory.get(loop.id) || [];
  if (history.length === 0) return null;
  const rows = history.slice(0, 10); // newest-first
  const maxElapsed = Math.max(1, ...rows.map((s) => s.elapsed_ms || 0));
  const maxTokens = Math.max(1, ...rows.map((s) => (s.input_tokens || 0) + (s.output_tokens || 0)));

  const section = document.createElement('section');
  section.className = 'iter-strip';
  const head = document.createElement('div');
  head.className = 'iter-strip__head';
  head.textContent = 'recent turns';
  section.appendChild(head);

  for (const s of rows) {
    const row = document.createElement('div');
    row.className = 'iter-row';

    const num = document.createElement('span');
    num.className = 'iter-num';
    num.textContent = '#' + (s.number || '');
    row.appendChild(num);

    const model = document.createElement('span');
    model.className = 'iter-model';
    model.textContent = s.model ? shortModelName(s.model) : '';
    row.appendChild(model);

    const tokBar = document.createElement('div');
    tokBar.className = 'iter-bar iter-bar--tokens';
    const inTok = s.input_tokens || 0;
    const outTok = s.output_tokens || 0;
    if (inTok + outTok > 0) {
      const inEl = document.createElement('span');
      inEl.className = 'iter-tok-in';
      inEl.style.width = ((inTok / maxTokens) * 100) + '%';
      tokBar.appendChild(inEl);
      const outEl = document.createElement('span');
      outEl.className = 'iter-tok-out';
      outEl.style.width = ((outTok / maxTokens) * 100) + '%';
      tokBar.appendChild(outEl);
    }
    row.appendChild(tokBar);

    const elBar = document.createElement('div');
    elBar.className = 'iter-bar iter-bar--elapsed';
    const elFill = document.createElement('span');
    elFill.style.width = (((s.elapsed_ms || 0) / maxElapsed) * 100) + '%';
    elBar.appendChild(elFill);
    row.appendChild(elBar);

    const dur = document.createElement('span');
    dur.className = 'iter-dur';
    dur.textContent = s.elapsed_ms ? formatDuration(s.elapsed_ms) : '';
    row.appendChild(dur);

    section.appendChild(row);
  }
  return section;
}

// formatToolPayload renders a tool's args/result for the live feed — a JSON
// string for objects, the value as-is for strings.
function formatToolPayload(v) {
  if (v == null) return '';
  if (typeof v === 'string') return v;
  try { return JSON.stringify(v); } catch (e) { return String(v); }
}

// renderLiveToolFeed is the in-situ window into a running loop: a live transcript
// of the turn's tool calls — name, status, args in, result out — streaming as
// they fire and return. Sits directly under the glance header so an operator can
// watch a loop's focus and direction while it works. Sourced from loop._liveTools
// (accumulated by the SSE reducer); shown only while there's a turn in flight.
function renderLiveToolFeed(loop) {
  const tools = loop._liveTools || [];
  if (loop.state !== 'processing' && tools.length === 0) return null;

  const section = document.createElement('section');
  section.className = 'live-feed';

  const head = document.createElement('div');
  head.className = 'live-feed__head';
  const dot = document.createElement('span');
  dot.className = 'live-feed__dot';
  head.appendChild(dot);
  const label = document.createElement('span');
  label.className = 'live-feed__label';
  label.textContent = 'live · this turn';
  head.appendChild(label);
  const count = document.createElement('span');
  count.className = 'live-feed__count';
  const running = tools.filter((t) => t.status === 'running').length;
  if (tools.length) {
    count.textContent = formatNumber(tools.length) + (tools.length === 1 ? ' tool' : ' tools')
      + (running ? ' · ' + running + ' running' : '');
  }
  head.appendChild(count);
  section.appendChild(head);

  if (tools.length === 0) {
    const idle = document.createElement('div');
    idle.className = 'live-feed__idle';
    idle.textContent = loop._liveModel
      ? 'thinking on ' + shortModelName(loop._liveModel) + ' — no tool in flight'
      : 'working — no tool in flight';
    section.appendChild(idle);
    return section;
  }

  for (const t of tools) {
    const card = document.createElement('div');
    card.className = 'live-tool live-tool--' + (t.error ? 'error' : (t.status || 'done'));
    const th = document.createElement('div');
    th.className = 'live-tool__head';
    const name = document.createElement('span');
    name.className = 'live-tool__name';
    name.textContent = t.tool || '?';
    th.appendChild(name);
    const status = document.createElement('span');
    status.className = 'live-tool__status';
    status.textContent = t.error ? 'error' : (t.status || 'done');
    th.appendChild(status);
    card.appendChild(th);
    const args = formatToolPayload(t.args);
    if (args) {
      const pre = document.createElement('pre');
      pre.className = 'live-tool__args';
      pre.textContent = args.slice(0, 240);
      card.appendChild(pre);
    }
    const out = t.error ? formatToolPayload(t.error) : formatToolPayload(t.result);
    if (out) {
      const pre = document.createElement('pre');
      pre.className = 'live-tool__result' + (t.error ? ' live-tool__result--error' : '');
      pre.textContent = out.slice(0, 240);
      card.appendChild(pre);
    }
    section.appendChild(card);
  }
  return section;
}

// renderActiveCapabilities is the SINGLE shared tooling block — used for every
// loop (conversation-backed or not) so the section reads identically instead of
// the old split where conversation loops showed chips and others showed comma
// lists. Minimized read = aggregate counts; brief = the loop's ACTIVE capability
// tags as a full-width chip list, one box each ("active" tracks the tag_activate
// verb the model-facing tooling uses). The available count lives in the summary,
// never as its own box. (Full view → tool-reference catalog links once that
// surface lands.)
function renderActiveCapabilities(entity) {
  const active = entity.currentLoadedCapabilities || [];
  const toolCount = (entity.currentEffectiveTools || []).length;
  const availCount = (entity.availableCapabilities || []).length;
  if (active.length === 0 && toolCount === 0 && availCount === 0) return null;

  const section = document.createElement('section');
  section.className = 'tooling-section';

  const head = document.createElement('div');
  head.className = 'tooling-section__head';
  const title = document.createElement('span');
  title.className = 'tooling-section__title';
  title.textContent = 'tooling';
  head.appendChild(title);
  const counts = document.createElement('span');
  counts.className = 'tooling-section__counts';
  counts.textContent = [
    active.length ? formatNumber(active.length) + ' active' : null,
    toolCount ? formatNumber(toolCount) + ' tools' : null,
    availCount ? formatNumber(availCount) + ' available' : null,
  ].filter(Boolean).join('  ·  ');
  head.appendChild(counts);
  section.appendChild(head);

  if (active.length > 0) {
    const chips = makeSchemaChipList(active.map((e) => e.tag), 'tag-chip tag-chip--active');
    chips.classList.add('tooling-section__chips');
    section.appendChild(chips);
  }
  return section;
}

const LOOP_DEF_TTL_MS = 60000; // specs change rarely; refetch at most once a minute

// ensureLoopDef lazily loads a loop's stored definition (its spec: prompt,
// supervisor config, declared outputs) from /v1/loop-definitions/{name} and
// caches it on state.loopDefs. The inspector re-renders ~1/s, so this guards
// against refetching: it only fires when there's no entry or the cached entry
// is past its TTL, and the resolve handler re-renders the still-selected loop so
// the spec appears without waiting for the next periodic tick.
function ensureLoopDef(name) {
  if (!name) return;
  const cached = state.loopDefs.get(name);
  const now = Date.now();
  if (cached && (cached.status === 'loading' || (now - cached.at) < LOOP_DEF_TTL_MS)) return;
  state.loopDefs.set(name, { status: 'loading', def: cached ? cached.def : null, at: now });
  api.tryGet('/loop-definitions/' + encodeURIComponent(name))
    .then((def) => {
      state.loopDefs.set(name, { status: def ? 'ready' : 'absent', def: def || null, at: Date.now() });
    })
    .catch(() => {
      state.loopDefs.set(name, { status: 'error', def: cached ? cached.def : null, at: Date.now() });
    })
    .finally(() => {
      const sel = state.selected && state.loops.get(state.selected);
      if (sel && sel.name === name) {
        try { renderDetail(); } catch (e) { console.error('spec renderDetail:', e); }
      }
    });
}

// renderLoopSpecSection surfaces the loop's stored DEFINITION — what the loop
// IS, against the runtime sections above that show what it's DOING. Three
// things an operator asks of a spec: the prompt it runs each wake, the
// supervisor-review messaging (when the loop opts into frontier review turns),
// and the documents it's declared to maintain. Ephemeral loops (no stored
// definition) and containers (structural only) say so plainly.
function renderLoopSpecSection(loop) {
  const name = loop && loop.name;
  if (!name || name === 'core') return null; // core has no registry definition
  ensureLoopDef(name);
  const entry = state.loopDefs.get(name);

  const section = document.createElement('section');
  section.className = 'loop-spec';
  const head = document.createElement('div');
  head.className = 'loop-spec__head';
  const title = document.createElement('span');
  title.className = 'loop-spec__title';
  title.textContent = 'spec';
  head.appendChild(title);
  const meta = document.createElement('span');
  meta.className = 'loop-spec__meta';
  head.appendChild(meta);
  section.appendChild(head);

  if (!entry || entry.status === 'loading') {
    meta.textContent = 'reading definition…';
    return section;
  }
  if (entry.status === 'error') {
    meta.textContent = 'definition unavailable';
    return section;
  }
  if (entry.status === 'absent') {
    meta.textContent = 'ephemeral';
    appendSpecEmpty(section, 'This loop runs from a runtime spec, not the definition registry — no persisted prompt, supervisor config, or declared outputs to show.');
    return section;
  }

  const spec = (entry.def && entry.def.spec) || {};
  meta.textContent = [
    spec.operation || '',
    spec.parent_name ? 'child of ' + spec.parent_name : null,
    (spec.tags && spec.tags.length) ? spec.tags.join(', ') : null,
  ].filter(Boolean).join('  ·  ');

  const hasTask = (spec.task || '').trim();
  const hasOutputs = (spec.outputs || []).length > 0;
  if (spec.operation === 'container' && !hasTask && !hasOutputs) {
    appendSpecEmpty(section, 'Container node — organizes its children and cascades subscriptions. No prompt or outputs of its own.');
    return section;
  }

  // Prompt — the per-iteration task, plus method instructions when present.
  if (hasTask) section.appendChild(makeSpecTextField(name, 'prompt', spec.task));
  const instr = spec.profile && spec.profile.instructions;
  if ((instr || '').trim()) section.appendChild(makeSpecTextField(name, 'instructions', instr));

  // Supervisor review — the frontier-review messaging, only when the loop opts in.
  if (spec.supervisor) {
    const sup = spec.supervisor_profile || {};
    const pct = typeof spec.supervisor_prob === 'number' ? Math.round(spec.supervisor_prob * 100) : null;
    const supMeta = [
      pct != null ? '~' + pct + '% of wakes' : null,
      sup.quality_floor ? 'quality floor ' + sup.quality_floor : null,
    ].filter(Boolean).join(' · ');
    if ((sup.instructions || '').trim()) {
      section.appendChild(makeSpecTextField(name, 'supervisor review', sup.instructions, supMeta));
    } else {
      appendSpecEmpty(section, 'Supervisor review on (' + (supMeta || 'periodic') + ') using baseline frontier overrides — no custom review prompt.');
    }
  }

  // Outputs — documents the loop maintains through scoped runtime tools.
  if (hasOutputs) section.appendChild(makeSpecOutputs(spec.outputs));
  return section;
}

function appendSpecEmpty(section, text) {
  const note = document.createElement('div');
  note.className = 'loop-spec__empty';
  note.textContent = text;
  section.appendChild(note);
}

// makeSpecTextField renders one long spec text (prompt / instructions /
// supervisor messaging) as a compact, copyable field. Collapsed by default to a
// one-line whisper + char count; expand reads it inline, copy lifts the full
// body to the clipboard for pasting into an editor (the owner's stated way of
// working with prompt bodies). Expand state lives in state.specExpanded so it
// survives the inspector's ~1/s full re-render.
function makeSpecTextField(name, label, text, extraMeta) {
  const body = String(text);
  const key = name + '::' + label;
  const expanded = state.specExpanded.has(key);

  const field = document.createElement('div');
  field.className = 'spec-field';

  const fhead = document.createElement('div');
  fhead.className = 'spec-field__head';
  const lab = document.createElement('span');
  lab.className = 'spec-field__label';
  lab.textContent = label;
  fhead.appendChild(lab);
  const bytes = document.createElement('span');
  bytes.className = 'spec-field__bytes';
  bytes.textContent = [extraMeta, formatNumber(body.length) + ' chars'].filter(Boolean).join(' · ');
  fhead.appendChild(bytes);
  const spacer = document.createElement('span');
  spacer.className = 'spec-field__spacer';
  fhead.appendChild(spacer);

  const expandBtn = document.createElement('button');
  expandBtn.type = 'button';
  expandBtn.className = 'spec-field__btn';
  expandBtn.textContent = expanded ? 'collapse' : 'expand';
  expandBtn.addEventListener('click', () => {
    if (state.specExpanded.has(key)) state.specExpanded.delete(key);
    else state.specExpanded.add(key);
    try { renderDetail(); } catch (e) { console.error('spec expand renderDetail:', e); }
  });
  fhead.appendChild(expandBtn);

  const copyBtn = document.createElement('button');
  copyBtn.type = 'button';
  copyBtn.className = 'spec-field__btn';
  copyBtn.textContent = 'copy';
  copyBtn.addEventListener('click', () => {
    navigator.clipboard.writeText(body).then(() => {
      copyBtn.textContent = 'copied';
      copyBtn.classList.add('is-copied');
      setTimeout(() => { copyBtn.textContent = 'copy'; copyBtn.classList.remove('is-copied'); }, 1200);
    }).catch(() => {});
  });
  fhead.appendChild(copyBtn);
  field.appendChild(fhead);

  if (expanded) {
    const pre = document.createElement('pre');
    pre.className = 'spec-field__body';
    pre.textContent = body;
    field.appendChild(pre);
  } else {
    const whisper = body.split('\n').map((s) => s.trim()).filter(Boolean)[0] || '';
    const preview = document.createElement('div');
    preview.className = 'spec-field__preview';
    preview.textContent = truncate(whisper, 150);
    field.appendChild(preview);
  }
  return field;
}

// makeSpecOutputs lists the documents the loop is declared to maintain. Each row
// shows the document root + path (click to copy the ref), the write mode, the
// scoped runtime tool the loop writes through (replace_output_<name> for replace
// mode), and the declared purpose — connecting the spec to the live tool feed.
function makeSpecOutputs(outputs) {
  const wrap = document.createElement('div');
  wrap.className = 'spec-outputs';
  const lab = document.createElement('div');
  lab.className = 'spec-field__label spec-outputs__label';
  lab.textContent = outputs.length === 1 ? 'output document' : formatNumber(outputs.length) + ' output documents';
  wrap.appendChild(lab);

  for (const out of outputs) {
    const row = document.createElement('div');
    row.className = 'spec-output';

    const refLine = document.createElement('div');
    refLine.className = 'spec-output__refline';
    const ref = String(out.ref || '');
    const ci = ref.indexOf(':');
    const root = ci > 0 ? ref.slice(0, ci) : '';
    const path = ci > 0 ? ref.slice(ci + 1) : ref;
    if (root) {
      const rootTag = document.createElement('span');
      rootTag.className = 'spec-output__root';
      rootTag.textContent = root;
      refLine.appendChild(rootTag);
    }
    const pathEl = document.createElement('button');
    pathEl.type = 'button';
    pathEl.className = 'spec-output__path';
    pathEl.textContent = path;
    pathEl.title = 'copy ' + ref;
    pathEl.addEventListener('click', () => {
      navigator.clipboard.writeText(ref).then(() => {
        pathEl.textContent = 'copied ref';
        pathEl.classList.add('is-copied');
        setTimeout(() => { pathEl.textContent = path; pathEl.classList.remove('is-copied'); }, 1200);
      }).catch(() => {});
    });
    refLine.appendChild(pathEl);
    if (out.mode) {
      const mode = document.createElement('span');
      mode.className = 'spec-output__mode';
      mode.textContent = out.mode;
      refLine.appendChild(mode);
    }
    row.appendChild(refLine);

    if (out.mode === 'replace' && out.name) {
      const tool = document.createElement('div');
      tool.className = 'spec-output__tool';
      tool.textContent = 'replace_output_' + out.name;
      row.appendChild(tool);
    }
    if (out.purpose) {
      const purpose = document.createElement('div');
      purpose.className = 'spec-output__purpose';
      purpose.textContent = out.purpose;
      row.appendChild(purpose);
    }
    wrap.appendChild(row);
  }
  return wrap;
}

function renderLoopEntityDetail(loop) {
  const entity = buildLoopEntity(loop);
  detailEntity.innerHTML = '';
  const primaryConvID = getLoopPrimaryConversationID(entity);
  const currentConversation = getConversationSummaryDetail(primaryConvID);
  const currentConversationIDs = new Set((entity.currentConvID ? [entity.currentConvID] : []).filter(Boolean));
  const recentHistoryIDs = entity.recentConvIDs.filter((id) => id !== primaryConvID);
  const historyCount = recentHistoryIDs.length + (primaryConvID ? 1 : 0);
  const parentLabel = entity.parentID ? shortID(entity.parentID) : 'core';
  const lastWakeDate = parseTimestamp(entity.lastWakeAt);
  const lastWakeAgo = lastWakeDate ? timeAgo(lastWakeDate) : '';
  const latestModelLabel = entity.latestModel || 'model pending';
  const contextLabel = entity.contextWindow ? `${formatNumber(entity.contextWindow)} ctx` : '';
  const missionSummary = entity.hints.mission ? truncate(entity.hints.mission, 92) : '';
  const conversationBacked = isConversationBackedLoop(entity);
  const currentEffectiveTools = entity.currentEffectiveTools || [];
  const currentLoadedCapabilities = entity.currentLoadedCapabilities || [];
  const latestToolsUsed = Object.keys(entity.latestToolsUsed || {}).filter(Boolean).sort();
  const liveToolNames = Array.from(new Set(entity.activeLiveTools.map((entry) => entry.tool).filter(Boolean))).sort();

  detailEntity.appendChild(renderLoopGlanceHeader(loop));

  // Live tool feed — the in-situ window into a running loop, directly under the
  // header so it's the first thing you read while watching a loop work.
  const liveFeed = renderLiveToolFeed(loop);
  if (liveFeed) detailEntity.appendChild(liveFeed);

  const iterStrip = renderIterationStrip(loop);
  if (iterStrip) detailEntity.appendChild(iterStrip);

  if (conversationBacked) {
    detailEntity.appendChild(renderLoopCurrentTurnCard(loop, entity, currentConversation));
  }

  // Spec — what the loop IS (prompt, supervisor review, declared outputs), from
  // the definition registry. Sits above tooling/execution: definitional context
  // before runtime stats. Lazy-loads; renders a placeholder until it arrives.
  const specSection = renderLoopSpecSection(loop);
  if (specSection) detailEntity.appendChild(specSection);

  const identity = makeSchemaCard('Identity', 'Role in the graph', {
    entityKind: entity.kind,
    key: 'identity',
    titleSummary: `${entity.executionMode} · ${entity.categoryLabel.toLowerCase()}${entity.subsystem ? ' · ' + entity.subsystem : ''}`,
    titleFacts: [entity.relation, entity.categorySource, entity.subsystem || ''].filter(Boolean),
    widgetFacts: [
      { label: 'Mode', value: entity.executionMode },
      { label: 'Visual', value: entity.categoryLabel },
      { label: 'Relation', value: entity.relation },
      entity.subsystem ? { label: 'Subsystem', value: entity.subsystem } : null,
    ],
  });
  appendSchemaRow(identity.body, 'loop_id', makeIDChip(entity.loopID));
  appendSchemaRow(identity.body, 'entity kind', entity.kind);
  appendSchemaRow(identity.body, 'execution mode', entity.executionMode);
  appendSchemaRow(identity.body, 'visual category', entity.categoryLabel);
  appendSchemaRow(identity.body, 'classification source', entity.categorySource);
  if (entity.subsystem) appendSchemaRow(identity.body, 'subsystem', entity.subsystem);

  const relationships = makeSchemaCard('Relationships', 'Parents, conversations, and request trail', {
    entityKind: entity.kind,
    key: 'relationships',
    titleSummary: currentConversation
      ? `${currentConversation.label}${currentConversation.metaLine ? ' · ' + currentConversation.metaLine : ''}`
      : (entity.parentID ? `Child loop of ${shortID(entity.parentID)}.` : 'Root loop anchored to core.'),
    titleFacts: [
      entity.parentID ? `Parent ${shortID(entity.parentID)}` : 'Anchor core',
      historyCount ? `${formatNumber(historyCount)} threads` : '',
      entity.latestRequestID ? `Req ${shortID(entity.latestRequestID)}` : '',
    ].filter(Boolean),
    widgetFacts: [
      { label: 'Parent', value: parentLabel },
      { label: 'Thread', value: currentConversation ? currentConversation.label : 'none' },
      { label: 'History', value: historyCount ? `${formatNumber(historyCount)} convs` : 'none' },
      entity.latestRequestID ? { label: 'Request', value: shortID(entity.latestRequestID) } : null,
    ],
  });
  if (entity.parentID) {
    appendSchemaRow(relationships.body, 'parent loop', makeIDChip(entity.parentID));
  } else {
    appendSchemaRow(relationships.body, 'root anchor', 'core');
  }
  if (primaryConvID) {
    appendSchemaRow(
      relationships.body,
      'conversation',
      makeConversationSummaryList([primaryConvID], { currentIDs: currentConversationIDs }),
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

  const execution = makeSchemaCard('Execution', 'Live state, model, and token flow', {
    entityKind: entity.kind,
    key: 'execution',
    titleSummary: [formatSchemaToken(entity.stateLabel), latestModelLabel, contextLabel].filter(Boolean).join(' · '),
    titleFacts: [
      entity.iterations ? `${formatNumber(entity.iterations)} iterations` : '',
      lastWakeAgo ? `wake ${lastWakeAgo}` : '',
      entity.consecutiveErrors ? `${formatNumber(entity.consecutiveErrors)} errors` : '',
    ].filter(Boolean),
    widgetFacts: [
      { label: 'State', value: formatSchemaToken(entity.stateLabel) },
      { label: 'Model', value: latestModelLabel },
      contextLabel ? { label: 'Context', value: contextLabel } : null,
      entity.lastInputTokens || entity.lastOutputTokens
        ? { label: 'Last I/O', value: `${formatTokens(entity.lastInputTokens)} · ${formatTokens(entity.lastOutputTokens)}` }
        : null,
    ],
  });
  appendSchemaRow(execution.body, 'state', formatSchemaToken(entity.stateLabel));
  appendSchemaRow(execution.body, 'started', entity.startedAt ? timeAgo(new Date(entity.startedAt)) : '');
  appendSchemaRow(execution.body, 'last wake', lastWakeDate ? timeAgo(lastWakeDate) : '');
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

  const toolingSection = renderActiveCapabilities(entity);

  const profile = makeSchemaCard('Profile', 'Intent, trust, and carried context', {
    entityKind: entity.kind,
    key: 'profile',
    titleSummary: missionSummary || [
      entity.trustZone ? `trust ${entity.trustZone}` : '',
      entity.hints.source ? `source ${entity.hints.source}` : '',
    ].filter(Boolean).join(' · ') || 'Hints, trust, and operating posture.',
    titleFacts: [
      entity.trustZone || '',
      entity.hints.source || '',
      entity.hints.delegation_gating || '',
    ].filter(Boolean),
    widgetFacts: [
      entity.trustZone ? { label: 'Trust', value: entity.trustZone } : null,
      entity.hints.source ? { label: 'Source', value: entity.hints.source } : null,
      entity.hints.delegation_gating ? { label: 'Gating', value: entity.hints.delegation_gating } : null,
    ],
  });
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

  const delegateTask = entity.metadata.delegate_task || '';
  const delegateGuidance = entity.metadata.delegate_guidance || '';
  const delegateProfile = entity.metadata.delegate_profile || '';
  if (delegateTask || delegateGuidance || delegateProfile) {
    if (delegateProfile) appendSchemaRow(profile.body, 'delegate profile', delegateProfile);
    if (delegateTask) appendSchemaRow(profile.body, 'delegate task', delegateTask, { multiline: true });
    if (delegateGuidance) appendSchemaRow(profile.body, 'delegate guidance', delegateGuidance, { multiline: true });
  }

  if (toolingSection) detailEntity.appendChild(toolingSection);
  const activity = makeSchemaCard('Activity', 'Recent rhythm and iteration history', {
    entityKind: entity.kind,
    key: 'activity',
    titleSummary: [
      entity.iterations ? `${formatNumber(entity.iterations)} iterations` : 'No iterations yet',
      entity.attempts ? `${formatNumber(entity.attempts)} attempts` : '',
      lastWakeAgo ? `wake ${lastWakeAgo}` : '',
    ].filter(Boolean).join(' · '),
    titleFacts: [
      entity.latestSnapshot && entity.latestSnapshot.completed_at ? 'recent snapshot' : '',
      entity.lastError ? 'last error present' : '',
    ].filter(Boolean),
    widgetFacts: [
      { label: 'Iterations', value: formatNumber(entity.iterations) },
      { label: 'Attempts', value: formatNumber(entity.attempts) },
      { label: 'Last wake', value: lastWakeAgo || 'pending' },
      { label: 'Errors', value: formatNumber(entity.consecutiveErrors || 0) },
    ],
  });
  const aggregates = document.createElement('div');
  aggregates.className = 'detail-aggregates';
  renderAggregates(loop, aggregates);
  activity.body.appendChild(aggregates);

  if (entity.latestSnapshot) {
    const latestWrap = document.createElement('div');
    latestWrap.className = 'loop-turn-brief';
    const latestSummary = document.createElement('div');
    latestSummary.className = 'loop-turn-brief__summary';
    latestSummary.textContent = entity.lastError
      ? 'Most recent recorded turn ended with an error. Use the request chip below to inspect prompt and tool-call detail.'
      : 'Most recent recorded turn gives the best quick read on how this loop has been behaving recently.';
    latestWrap.appendChild(latestSummary);

    const latestGrid = document.createElement('div');
    latestGrid.className = 'loop-turn-brief__grid';
    const latestFacts = [
      { label: 'Iteration', value: '#' + formatNumber(entity.latestSnapshot.number || entity.iterations || 0) },
      { label: 'Request', value: entity.latestSnapshot.request_id ? shortID(entity.latestSnapshot.request_id) : 'pending' },
      { label: 'Model', value: entity.latestSnapshot.model ? shortModelName(entity.latestSnapshot.model) : latestModelLabel },
      { label: 'Duration', value: entity.latestSnapshot.elapsed_ms ? formatDuration(entity.latestSnapshot.elapsed_ms) : 'pending' },
      { label: 'Input', value: entity.latestSnapshot.input_tokens ? formatTokens(entity.latestSnapshot.input_tokens) : '0' },
      { label: 'Output', value: entity.latestSnapshot.output_tokens ? formatTokens(entity.latestSnapshot.output_tokens) : '0' },
    ];
    for (const item of latestFacts) {
      const cell = document.createElement('div');
      cell.className = 'loop-turn-brief__metric';
      const value = document.createElement('div');
      value.className = 'loop-turn-brief__metric-value';
      value.textContent = item.value;
      cell.appendChild(value);
      const label = document.createElement('div');
      label.className = 'loop-turn-brief__metric-label';
      label.textContent = item.label;
      cell.appendChild(label);
      latestGrid.appendChild(cell);
    }
    latestWrap.appendChild(latestGrid);
    activity.body.appendChild(latestWrap);
  }

  const timeline = document.createElement('div');
  timeline.className = 'iter-timeline';
  renderTimeline(loop, timeline, state.iterationHistory.get(loop.id) || [], loop.id, state.sleepTimers);
  activity.body.appendChild(timeline);

  // Tier 0: the hero / identity / relationship cards are subsumed by the glance
  // header above. The remaining cards are Tier 1/2 fodder, kept until those
  // slices rework them (their builds are pruned then).
  if (conversationBacked) {
    detailEntity.appendChild(execution.card);
    detailEntity.appendChild(profile.card);
    return;
  }

  detailEntity.appendChild(execution.card);
  detailEntity.appendChild(profile.card);
}

function buildLoopContextMenu(loop) {
  const entity = buildLoopEntity(loop);
  const primaryConvID = getLoopPrimaryConversationID(entity);
  const items = [
    { label: 'kind: ' + entity.kind, disabled: true },
    { label: 'visual: ' + entity.categoryLabel + ' · ' + entity.categorySource, disabled: true },
    { label: 'relation: ' + entity.relation + (entity.parentID ? ' · parent ' + shortID(entity.parentID) : ' · anchored to core'), disabled: true },
    primaryConvID ? { label: 'conversation: ' + shortID(primaryConvID), disabled: true } : null,
    entity.trustZone ? { label: 'trust: ' + entity.trustZone, disabled: true } : null,
    { separator: true },
  ].filter(Boolean);
  if (entity.parentID && state.loops.has(entity.parentID)) {
    items.push({ label: 'Select parent loop', action: () => selectLoop(entity.parentID) });
  } else if (!entity.parentID && state.system) {
    items.push({ label: 'Select core anchor', action: () => selectSystem() });
  }
  if (entity.latestRequestID && typeof window.onRequestChipClick === 'function') {
    items.push({ label: 'Open request in pane', action: () => showRequestDetail(entity.latestRequestID) });
  }
  items.push({ separator: true });
  items.push({ label: 'Copy node JSON', action: () => { void copyLoopEntityJSON(loop); } });
  items.push({ label: 'Copy loop ID', action: () => navigator.clipboard.writeText(entity.loopID) });
  if (entity.parentID) {
    items.push({ label: 'Copy parent loop ID', action: () => navigator.clipboard.writeText(entity.parentID) });
  }
  if (primaryConvID) {
    items.push({ label: 'Copy conversation ID', action: () => navigator.clipboard.writeText(primaryConvID) });
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
    { label: 'Inspect core', action: () => selectSystem() },
    { separator: true },
    { label: 'Copy core JSON', action: () => { void copySystemEntityJSON(sys || state.system || {}); } },
  ];
}

function renderDetail(opts = {}) {
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
  }, opts);
}

// renderAggregates, renderTimeline, clearLiveTelemetry are in shared.js.

// makeIDRow, makeIDChip, shortID, shortModelName, buildToolCounts,
// escapeHTML, truncate, formatSchemaToken are in shared.js.

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

// ensureInspectorVisible reveals the inspector pane when a user-initiated
// inspect action (node click, "Inspect ..." context menu, long-press focus, or
// a process-table selection) targets it while it is toggled off — otherwise the
// selection renders into a hidden pane with no visible effect.
function ensureInspectorVisible() {
  if (document.getElementById('detail-panel').hidden) setInspectorVisible(true);
}

function selectLoop(loopId) {
  if (state.selected === loopId) {
    // Deselect.
    state.selected = null;
    showLogHint('Select a loop node to inspect its diagnostic tail');
  } else {
    state.selected = loopId;
    fetchLogs(loopId);
    ensureInspectorVisible();
  }
  if (viewState) viewState.setSelection(state.selected);
  renderAll();
}

function focusLoop(loopId) {
  if (!loopId) return;
  if (state.selected !== loopId) {
    state.selected = loopId;
    fetchLogs(loopId);
    ensureInspectorVisible();
    // Mirror selectLoop so the shared selection (process table) stays in sync
    // on touch long-press, not just mouse click.
    if (viewState) viewState.setSelection(loopId);
    renderAll();
  }
}

function selectSystem() {
  if (state.selected === '__system__') {
    state.selected = null;
    showLogHint('Select a loop node to inspect its diagnostic tail');
  } else {
    state.selected = '__system__';
    showLogHint('Logs in the dashboard are node-scoped. Select a loop to inspect its diagnostic tail.');
    ensureInspectorVisible();
  }
  // The system node isn't a loop; clear any loop selection in the shared state.
  if (viewState) viewState.setSelection(null);
  renderAll();
}

function focusSystem() {
  if (state.selected !== '__system__') {
    state.selected = '__system__';
    showLogHint('Logs in the dashboard are node-scoped. Select a loop to inspect its diagnostic tail.');
    ensureInspectorVisible();
    // The system node isn't a loop; clear any loop selection in the shared
    // state (mirrors selectSystem) so the process table doesn't keep a row lit.
    if (viewState) viewState.setSelection(null);
    renderAll();
  }
}

// ---------------------------------------------------------------------------
// Animation Loop (sleep countdowns + progress rings)
// ---------------------------------------------------------------------------

let _lastDetailTickMs = 0;

function currentDetailTickIntervalMs() {
  if (!state.selected || !state.loops.has(state.selected)) return 1000;
  const loop = state.loops.get(state.selected);
  if (!loop) return 1000;
  if (loop.state === 'processing' || loop.state === 'sleeping' || loop.state === 'waiting') {
    return 250;
  }
  return 1000;
}

// Gource-style auto-fit camera: ease pan/zoom each frame so the whole graph
// stays framed in the canvas, with margin. The layout lives in fixed
// canvas-pixel space, so moving the camera here never disturbs the physics —
// there is no feedback loop. Manual pan/zoom switches this off (see
// initPanZoom); double-clicking the background turns it back on and snaps.
function autoFitCamera(canvasW, canvasH) {
  if (!viewport.autoFit || canvasW <= 0 || canvasH <= 0 || physics.nodes.size === 0) return;
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  for (const [id, nd] of physics.nodes) {
    const ext = getPhysicsNodeExtent(id);
    if (nd.x - ext < minX) minX = nd.x - ext;
    if (nd.x + ext > maxX) maxX = nd.x + ext;
    if (nd.y - ext < minY) minY = nd.y - ext;
    if (nd.y + ext > maxY) maxY = nd.y + ext;
  }
  if (!Number.isFinite(minX)) return;
  const margin = 64;
  const cloudW = Math.max(1, maxX - minX);
  const cloudH = Math.max(1, maxY - minY);
  const bcx = (minX + maxX) / 2;
  const bcy = (minY + maxY) / 2;
  // Fit to the tighter axis, but never magnify a small graph past 1:1.
  const fit = Math.min((canvasW - (margin * 2)) / cloudW, (canvasH - (margin * 2)) / cloudH);
  const targetZoom = Math.max(ZOOM_MIN, Math.min(1, fit));
  const targetPanX = (canvasW / 2) - (bcx * targetZoom);
  const targetPanY = (canvasH / 2) - (bcy * targetZoom);
  const ease = viewport.autoFitSnap ? 1 : 0.12;
  viewport.autoFitSnap = false;
  viewport.zoom += (targetZoom - viewport.zoom) * ease;
  viewport.panX += (targetPanX - viewport.panX) * ease;
  viewport.panY += (targetPanY - viewport.panY) * ease;
  applyViewportTransform();
}

function tick() {
  // Physics simulation — run every frame for smooth organic motion.
  const rect = refreshCanvasViewport() || getLayoutViewportRect();
  if (rect.width > 0 && rect.height > 0) {
    physicsStep(rect.cx, rect.cy, rect.pxWidth, rect.pxHeight);
    updatePinnedAnchorPositions();
    updateNodePositions();
    autoFitCamera(rect.pxWidth, rect.pxHeight);
  }

  // Keep the selected inspector lively without redrawing at full frame rate.
  const nowMs = Date.now();
  if ((nowMs - _lastDetailTickMs) >= currentDetailTickIntervalMs()) {
    _lastDetailTickMs = nowMs;
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

  if (graphRunning) requestAnimationFrame(tick);
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
  btn.setAttribute('aria-pressed', String(visible));
  dashboardPrefs.inspectorVisible = visible;
  saveDashboardPrefs(dashboardPrefs);
  if (visible) requestAnimationFrame(() => syncAllSchemaCardLayouts());
}

function setLogsVisible(visible) {
  const panel = document.getElementById('log-panel');
  const handle = document.getElementById('resize-h');
  const btn = document.getElementById('toggle-logs');
  panel.hidden = !visible;
  handle.hidden = !visible;
  btn.classList.toggle('toggle-btn--active', visible);
  btn.setAttribute('aria-pressed', String(visible));
  dashboardPrefs.logsVisible = visible;
  saveDashboardPrefs(dashboardPrefs);
}

function setLegendVisible(visible) {
  if (!legendPanel || !legendBackdrop || !legendToggleBtn) return;
  const wasVisible = !legendPanel.hidden;
  legendPanel.hidden = !visible;
  legendBackdrop.hidden = !visible;
  legendToggleBtn.classList.toggle('toggle-btn--active', visible);
  legendToggleBtn.setAttribute('aria-pressed', String(visible));
  // Move focus into the modal on open and back to the toggle on close, so
  // keyboard/screen-reader users get dialog focus context. The wasVisible
  // guard keeps the Escape handler (which calls setLegendVisible(false)
  // unconditionally) from stealing focus when the legend was already closed.
  if (visible && !wasVisible) {
    legendCloseBtn?.focus();
  } else if (!visible && wasVisible) {
    legendToggleBtn.focus();
  }
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

if (detailPanel) {
  detailPanel.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    if (isDetailInteractiveTarget(e.target)) {
      bumpDetailInteractionHold(450);
      return;
    }
    detailPointerSelectionActive = true;
    bumpDetailInteractionHold(DETAIL_POINTER_GUARD_MS);
  });

  detailPanel.addEventListener('pointerover', (e) => {
    if (!isDetailInteractiveTarget(e.target)) return;
    detailInteractiveHoverActive = true;
    bumpDetailInteractionHold(120);
  });

  detailPanel.addEventListener('pointerout', (e) => {
    if (!isDetailInteractiveTarget(e.target)) return;
    if (isDetailInteractiveTarget(e.relatedTarget)) return;
    detailInteractiveHoverActive = false;
    bumpDetailInteractionHold(90);
  });

  detailPanel.addEventListener('pointerleave', () => {
    detailInteractiveHoverActive = false;
  });

  detailPanel.addEventListener('copy', () => {
    bumpDetailInteractionHold(DETAIL_COPY_GUARD_MS);
  });
}

document.addEventListener('pointerup', () => {
  if (!detailPointerSelectionActive) return;
  detailPointerSelectionActive = false;
  bumpDetailInteractionHold(DETAIL_SELECTION_RELEASE_MS);
});

document.addEventListener('selectionchange', () => {
  if (detailTextSelectionActive()) {
    bumpDetailInteractionHold(DETAIL_SELECTION_RELEASE_MS);
  }
});

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
      sep.setAttribute('role', 'separator');
      contextMenuItems.appendChild(sep);
      continue;
    }
    const li = document.createElement('li');
    li.textContent = item.label;
    li.setAttribute('role', 'menuitem');
    li.setAttribute('tabindex', '-1');
    if (item.disabled) {
      li.className = 'context-menu-item context-menu-item--disabled';
      li.setAttribute('aria-disabled', 'true');
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
// Keyboard Shortcuts
// ---------------------------------------------------------------------------

document.addEventListener('keydown', (e) => {
  // Skip when typing in form elements.
  const tag = e.target.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

  // Bare-key toggles must not hijack browser/OS chords (Cmd/Ctrl/Alt + L/I).
  // Shift stays allowed because '?' is Shift+/ on US layouts. Escape is left
  // reachable so modifier+Escape still runs the close path.
  const mod = e.metaKey || e.ctrlKey || e.altKey;

  switch (e.key.toLowerCase()) {
    case 'i':
      if (!mod) toggleInspector();
      break;
    case 'l':
      if (!mod) toggleLogs();
      break;
    case '?':
      if (!mod) toggleLegend();
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
      syncAllSchemaCardLayouts();
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

(function initInspectorCardObserver() {
  if (typeof ResizeObserver === 'undefined' || !detailPanel) return;
  const observer = new ResizeObserver(() => {
    if (detailPanel.hidden) return;
    syncAllSchemaCardLayouts();
  });
  observer.observe(detailPanel);
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

const viewport = { panX: 0, panY: 0, zoom: 1, spread: 1, autoFit: true, autoFitSnap: true };
const ZOOM_MIN = 0.25;
// Spread is the layout's relief valve: the wheel scales the gravity rings so the
// graph unfurls into available space (scroll out) or packs tighter (scroll in),
// while the auto-fit camera reframes the result. Multiplicative per wheel tick.
const SPREAD_MIN = 0.55;
const SPREAD_MAX = 3.5;
const SPREAD_STEP = 1.12;

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
    viewport.autoFit = false; // a manual pan takes over from the auto-fit camera
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
    // The wheel drives LAYOUT SPREAD, not the camera: scrolling out grows the
    // gravity rings so crowded families relax into the new room, scrolling in
    // packs them. The auto-fit camera (kept on) reframes the resized cloud, so on
    // screen the nodes shrink and the gaps open — the "more canvas to unfurl
    // into" effect that a pure camera zoom can never produce.
    const factor = e.deltaY > 0 ? SPREAD_STEP : 1 / SPREAD_STEP;
    viewport.spread = Math.max(SPREAD_MIN, Math.min(SPREAD_MAX, viewport.spread * factor));
    viewport.autoFit = true;      // let the camera reframe the new spread
    viewport.autoFitSnap = false; // ease into the new frame rather than jumping
  }, { passive: false });

  // Double-click the background to re-enable the auto-fit camera and snap it to
  // frame the whole graph.
  canvas.addEventListener('dblclick', (e) => {
    if (e.target.closest('.loop-node')) return;
    viewport.spread = 1; // reset to the neutral spread along with re-fitting
    viewport.autoFit = true;
    viewport.autoFitSnap = true;
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

function inspectRequest(requestID) {
  if (!requestID) return;
  void showRequestDetail(requestID);
}

window.onRequestChipClick = inspectRequest;

async function showRequestDetail(requestID) {
  if (!requestID) return;

  // Cancel any in-flight fetch for a previous request.
  if (requestDetailAbort) {
    requestDetailAbort.abort();
  }
  const controller = new AbortController();
  requestDetailAbort = controller;

  try {
    const detail = await api.get('/requests/' + encodeURIComponent(requestID), {
      signal: controller.signal,
    });

    // Verify this is still the active request — a newer click may have
    // replaced the controller while we were awaiting the response.
    if (requestDetailAbort !== controller) return;

    activeRequestID = requestID;
    activeRequestJSON = JSON.stringify(detail, null, 2);

    // Show the request detail panel, hide others. Reveal the inspector aside
    // and its resize handle directly (without persisting) so the request
    // detail is visible even when the operator has the Inspector toggled off —
    // e.g. opening a #request/<id> deep link on load. The saved Inspector
    // preference is restored in closeRequestDetail / handleHashRoute.
    document.getElementById('detail-panel').hidden = false;
    document.getElementById('resize-v').hidden = false;
    detailPlaceholder.hidden = true;
    detailContent.hidden = true;
    requestDetailPanel.hidden = false;

    renderRequestDetail(detail, requestDetailEls);

    // Update URL fragment for deep linking. Use location.hash (which
    // creates a history entry) so the browser Back button closes the panel.
    window.location.hash = 'request/' + requestID;
  } catch (err) {
    if (err.name === 'AbortError') return; // Superseded by a newer request.
    if (requestDetailAbort !== controller) return; // superseded mid-flight
    if (err instanceof api.ApiError) {
      // 404 (no retained detail) or 503 (retention disabled): close the panel.
      if (err.status === 404) console.warn('Request detail not found:', requestID);
      closeRequestDetail();
      return;
    }
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
  // Restore the inspector aside to the operator's saved preference — the
  // request view may have force-revealed it.
  setInspectorVisible(dashboardPrefs.inspectorVisible);
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
renderDetail = function(...args) {
  if (activeRequestID && !requestDetailPanel.hidden) {
    return; // Don't overwrite the request detail panel.
  }
  // Forward opts (force/instantLayout) so schema-card preset clicks still get
  // their forced re-render instead of being silently deferred.
  _origRenderDetail(...args);
};

// Request ID chips resolve through inspectRequest (assigned to
// window.onRequestChipClick above). When retained detail is unavailable the
// native API returns 404/503 and showRequestDetail closes the panel — no
// separate availability probe is needed.

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
    // Restore the inspector aside to the operator's saved preference — the
    // request view may have force-revealed it.
    setInspectorVisible(dashboardPrefs.inspectorVisible);
    renderAll();
  }
}

// ---------------------------------------------------------------------------
// createGraph — explicit entry point
// ---------------------------------------------------------------------------

// createGraph boots the cognition graph: it starts the live data streams and
// the animation loop, and (unless disabled) wires hash-based request deep
// links. The graph is no longer started on import, so a host controls when it
// runs and what it talks to.
//
// opts:
//   client      — /v1 data client (default: the real ./data/client.js). Inject
//                 a fixture client for tests, or a native bridge for an embed.
//   events      — loop-event subscriber (default: ./data/events.js SSE).
//   hashRouting — wire window.hashchange for #request/<id> deep links
//                 (default true; pass false when a host owns the URL).
let graphBooted = false;
let graphHandle = null;
let graphRunning = false;
let viewState = null;

// getStore exposes the shared loop store (created in connect()) so sibling
// console views — the process table, future surfaces — read the same live data
// the graph does, instead of opening a second ingestion of the same stream.
export function getStore() {
  return store;
}

export function createGraph(opts = {}) {
  // Idempotent: a second call (easy in an embed/harness/test) would otherwise
  // start duplicate SSE subscriptions, intervals, and animation loops.
  if (graphBooted) {
    console.warn('createGraph() called more than once; returning the existing instance.');
    return graphHandle;
  }
  graphBooted = true;
  graphRunning = true;
  viewState = opts.viewState || null;

  if (opts.client) api = opts.client;
  if (opts.events) subscribeLoopEvents = opts.events;
  const hashRouting = opts.hashRouting !== false;

  connect();
  fetchVersionInfo();
  fetchSystemStatus();
  if (hashRouting) {
    window.addEventListener('hashchange', handleHashRoute);
    handleHashRoute();
  }
  // Refresh uptime display every second.
  const uptimeTimer = setInterval(updateUptime, 1000);
  // Refresh system status every 10s.
  const systemTimer = setInterval(fetchSystemStatus, 10000);
  requestAnimationFrame(tick);

  // Reflect external selection (e.g. from the process table) into the graph, so
  // selecting a loop in either view highlights it in both. selectLoop() writes
  // back to viewState (deduped), so this can't loop.
  let unsubViewState = null;
  if (viewState) {
    unsubViewState = viewState.subscribe(({ selection }) => {
      if (selection === state.selected) return;
      if (selection) selectLoop(selection);
      else if (state.selected && state.selected !== '__system__') selectLoop(state.selected);
    });
  }

  // destroy() stops the graph's ongoing work — the store's SSE, the intervals,
  // the animation loop, the hashchange listener, and the view-state
  // subscription — so a host can unmount the graph cleanly (e.g. a SwiftUI/
  // WebKit embed). After destroy, createGraph() can boot a fresh instance.
  graphHandle = {
    destroy() {
      graphRunning = false; // the rAF loop stops rescheduling on the next frame
      clearInterval(uptimeTimer);
      clearInterval(systemTimer);
      if (hashRouting) window.removeEventListener('hashchange', handleHashRoute);
      if (unsubViewState) unsubViewState();
      if (store) store.stop();
      graphBooted = false;
      graphHandle = null;
    },
  };
  return graphHandle;
}
