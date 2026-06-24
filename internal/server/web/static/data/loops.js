// data/loops.js — the shared running-loop data layer.
//
// One source of truth for the running-loop set behind every view (the node
// graph, the process table, the forensics window). It will grow to own the
// SSE ingestion + canonical loop map + change subscription; this first piece
// is the view-agnostic *anchor math* every consumer needs.
//
// Anchoring operates on the REAL loop hierarchy (`parent_id`) — "show node X
// and everything downstream of it." That is deliberately distinct from the
// graph's *effective-parent* layout tree (which re-roots orphan loops around
// the core node for display); anchoring is about the actual loop topology, so
// both views and the breadcrumb agree on what a subtree is.
//
// All functions are pure over a `loops` iterable (an array, or a Map's
// `.values()`), so they're trivially testable and reusable.

// asArray normalizes the accepted loops input (array | Map | iterable) to an
// array, so callers can pass a store's Map directly or a plain list.
function asArray(loops) {
  if (Array.isArray(loops)) return loops;
  if (loops instanceof Map) return Array.from(loops.values());
  return loops ? Array.from(loops) : [];
}

// childrenByParent indexes loops by their parent_id → child ids, the adjacency
// the subtree walk needs. Loops without a parent_id are roots (absent here).
export function childrenByParent(loops) {
  const byParent = new Map();
  for (const loop of asArray(loops)) {
    const pid = loop && loop.parent_id;
    if (!pid) continue;
    if (!byParent.has(pid)) byParent.set(pid, []);
    byParent.get(pid).push(loop.id);
  }
  return byParent;
}

// subtree returns the anchor loop plus every loop downstream of it (transitive
// descendants via parent_id), in stable pre-order. A null/empty anchor means
// "no scoping" → all loops. Cycle-safe. Children of an anchor that isn't itself
// a present loop are still included (defensive against partial snapshots).
export function subtree(loops, anchorId) {
  const list = asArray(loops);
  if (!anchorId) return list;

  const byParent = childrenByParent(list);
  const byId = new Map(list.map((l) => [l.id, l]));
  const out = [];
  const seen = new Set();
  const stack = [anchorId];

  while (stack.length > 0) {
    const id = stack.pop();
    if (seen.has(id)) continue; // guard against cycles
    seen.add(id);
    const loop = byId.get(id);
    if (loop) out.push(loop);
    const kids = byParent.get(id);
    if (kids) {
      // Push in reverse so siblings emerge in their original order.
      for (let i = kids.length - 1; i >= 0; i--) stack.push(kids[i]);
    }
  }
  return out;
}

// ancestorPath returns the chain from the root down to the anchor
// (root-first), for a breadcrumb like `supervisor > signal > signal/aimee`
// where each segment re-anchors. Walks parent_id upward; cycle-safe. Returns
// [] when the anchor isn't a present loop.
export function ancestorPath(loops, anchorId) {
  const byId = new Map(asArray(loops).map((l) => [l.id, l]));
  const path = [];
  const seen = new Set();
  let id = anchorId;

  while (id && byId.has(id) && !seen.has(id)) {
    seen.add(id);
    const loop = byId.get(id);
    path.push(loop);
    id = loop.parent_id || null;
  }
  return path.reverse();
}

// ---------------------------------------------------------------------------
// Loop store — the live, canonical running-loop set.
// ---------------------------------------------------------------------------
//
// createLoopStore ingests the /v1/loops/events stream into ONE canonical map
// and exposes it to every view via subscribe(). It owns only DATA — the loop
// map, per-loop iteration history, sleep timers, and a recent-event log. View
// concerns (rendering, notifications, animations, selection) stay in the views,
// which react to the store's change + lifecycle signals.
//
// Injected dependencies (for embeddability + tests):
//   events — the loop-event subscriber (data/events.js `subscribe`), called as
//            events({onSnapshot, onLoop, onDelegate, onState}) -> unsubscribe.
//   client — the /v1 client (data/client.js), used for the /loops re-fetch.
//
// The reducer (applyLoopEventToLoop) and helpers (clearLiveTelemetry,
// parseTimestamp, MAX_ITERATION_HISTORY) currently live in shared.js as globals
// — a fully standalone embed will bundle those. Signals via on(name, fn):
//   'conn_state'         — 'connecting' | 'connected' | 'disconnected'
//   'loop_error'         — { loopId, loop, message }
//   'iteration_complete' — { loopId }
//   'delegate_complete'  — { id, entry, data }
//   'delegate_remove'    — { id }
// and subscribe(fn) fires on ANY data change (the re-render hook).

const MAX_EVENTS = 50;
const DELEGATE_LINGER_MS = 15000;

export function createLoopStore({ client, events } = {}) {
  const loops = new Map();
  const iterationHistory = new Map();
  const sleepTimers = new Map();
  const eventLog = [];
  let connState = 'connecting';
  let unsubscribe = null;

  const changeListeners = new Set();
  const lifecycle = new Map();

  function emitChange() {
    for (const fn of changeListeners) fn();
  }
  function emit(name, payload) {
    const set = lifecycle.get(name);
    if (set) for (const fn of set) fn(payload);
  }
  function setConnState(s) {
    if (connState === s) return;
    connState = s;
    emit('conn_state', s);
  }
  function pushEvent(evt) {
    eventLog.unshift(evt);
    if (eventLog.length > MAX_EVENTS) eventLog.length = MAX_EVENTS;
  }
  function recordSnapshot(loopId, snap) {
    let arr = iterationHistory.get(loopId);
    if (!arr) {
      arr = [];
      iterationHistory.set(loopId, arr);
    }
    arr.unshift(snap);
    if (arr.length > MAX_ITERATION_HISTORY) arr.length = MAX_ITERATION_HISTORY;
  }

  // ---- ingestion (pure data; emits signals, never touches the DOM) ----

  function ingestSnapshot(statuses) {
    loops.clear();
    iterationHistory.clear();
    for (const s of statuses) {
      if (s.recent_iterations && s.recent_iterations.length > 0) {
        iterationHistory.set(s.id, s.recent_iterations.slice());
      }
      // Seed live telemetry for loops already processing so views show it
      // immediately on connect.
      if (s.state === 'processing') {
        const lastWake = parseTimestamp(s.last_wake_at);
        s._iterStartTs = lastWake ? lastWake.getTime() : Date.now();
        s._liveTools = [];
        s._liveModel = '';
        s._llmContext = s.llm_context || null;
        if (s._llmContext && s._llmContext.model) s._liveModel = s._llmContext.model;
      }
      loops.set(s.id, s);
    }
    setConnState('connected');
    emitChange();
  }

  function ingestLoopEvent(evt) {
    const loopId = evt.data && evt.data.loop_id;
    const loopName = evt.data && evt.data.loop_name;
    pushEvent(evt);

    // loop_started needs a full re-fetch; bootstrap a minimal entry so events
    // arriving before it returns aren't dropped.
    if (evt.kind === 'loop_started') {
      if (loopId && !loops.has(loopId)) {
        loops.set(loopId, {
          id: loopId, name: loopName || loopId, state: 'processing',
          parent_id: evt.data.parent_id || null, iterations: 0, _iterStartTs: Date.now(),
        });
      }
      void refetch();
      emitChange();
      return;
    }
    if (!loopId) {
      emitChange();
      return;
    }
    if (!loops.has(loopId)) {
      loops.set(loopId, {
        id: loopId, name: loopName || loopId, state: 'processing',
        iterations: 0, _iterStartTs: Date.now(),
      });
    }

    const loop = loops.get(loopId);
    const history = iterationHistory.get(loopId) || [];
    const result = applyLoopEventToLoop(evt, { loop, loopId, sleepTimers, history });

    if (result && result.snapshot) {
      recordSnapshot(loopId, result.snapshot);
      emit('iteration_complete', { loopId });
    }
    if (evt.kind === 'loop_error') {
      const message = (evt.data && evt.data.error) || loop.last_error || 'Loop iteration failed.';
      emit('loop_error', { loopId, loop, message });
    }
    emitChange();
  }

  function ingestDelegateEvent(evt) {
    const did = evt.data && evt.data.delegate_id;
    if (!did) return;
    const syntheticId = 'delegate-' + did;

    if (evt.kind === 'spawn') {
      // Synthetic loop entry so existing view infra (physics, connectors,
      // rows) works unchanged.
      loops.set(syntheticId, {
        id: syntheticId, name: evt.data.name || syntheticId, state: 'processing',
        parent_id: evt.data.parent_loop_id || null,
        config: { Metadata: { category: 'delegate' } },
        _delegate: true, _delegateId: did, _delegateTask: evt.data.task || '',
        _delegateProfile: evt.data.profile || '', _delegateGuidance: evt.data.guidance || '',
        _delegateTags: evt.data.tags || [], _iterStartTs: Date.now(),
      });
      emitChange();
      return;
    }
    if (evt.kind === 'complete') {
      sleepTimers.delete(syntheticId);
      const entry = loops.get(syntheticId);
      if (entry) {
        entry.state = evt.data.exhausted ? 'error' : 'completed';
        entry._delegateExhausted = !!evt.data.exhausted;
        entry._delegateExhaustReason = evt.data.exhaust_reason || '';
        entry._delegateDurationMs = evt.data.duration_ms || 0;
        entry._delegateIterations = evt.data.iterations || 0;
      }
      emit('delegate_complete', { id: syntheticId, entry, data: evt.data });
      // Linger, then drop from the canonical set; views animate the removal in
      // response to 'delegate_remove'. (Removal is data-driven, not coupled to
      // any one view's selection — each view keeps its own selection in sync.)
      setTimeout(() => {
        sleepTimers.delete(syntheticId);
        loops.delete(syntheticId);
        emit('delegate_remove', { id: syntheticId });
        emitChange();
      }, DELEGATE_LINGER_MS);
      emitChange();
    }
  }

  // refetch re-syncs from /v1/loops, preserving transient client-only telemetry
  // that in-flight SSE events may have set before the fetch returned.
  async function refetch() {
    if (!client) return;
    try {
      const statuses = await client.get('/loops');
      const serverIds = new Set();
      const transient = [
        '_iterStartTs', '_liveTools', '_liveModel', '_llmContext',
        '_supervisor', '_currentConvID', '_currentRequestID', '_lastModel', '_lastSupervisor',
        '_delegate', '_delegateId', '_delegateTask', '_delegateProfile',
        '_delegateGuidance', '_delegateTags', '_delegateIterations',
        '_delegateDurationMs', '_delegateExhausted', '_delegateExhaustReason',
      ];
      for (const s of statuses) {
        serverIds.add(s.id);
        const existing = loops.get(s.id);
        if (existing) {
          for (const key of transient) {
            if (existing[key] !== undefined && s[key] === undefined) s[key] = existing[key];
          }
        }
        loops.set(s.id, s);
      }
      // Drop loops the server no longer reports, but keep client-only delegates.
      for (const id of loops.keys()) {
        if (!serverIds.has(id) && !loops.get(id)?._delegate) loops.delete(id);
      }
      emitChange();
    } catch (err) {
      console.warn('Failed to fetch loops:', err);
    }
  }

  return {
    // shared structures (view layers may reference these directly during the
    // graph migration; new consumers should prefer the accessors below)
    loops,
    iterationHistory,
    sleepTimers,
    events: eventLog,

    getLoops: () => loops,
    getLoop: (id) => loops.get(id),
    subtree: (anchorId) => subtree(loops, anchorId),
    ancestorPath: (anchorId) => ancestorPath(loops, anchorId),
    get connState() {
      return connState;
    },
    refetch,

    subscribe(fn) {
      changeListeners.add(fn);
      return () => changeListeners.delete(fn);
    },
    on(name, fn) {
      if (!lifecycle.has(name)) lifecycle.set(name, new Set());
      lifecycle.get(name).add(fn);
      return () => lifecycle.get(name).delete(fn);
    },

    start() {
      if (unsubscribe) unsubscribe();
      setConnState('connecting');
      if (!events) return;
      unsubscribe = events({
        onSnapshot: ingestSnapshot,
        onLoop: ingestLoopEvent,
        onDelegate: ingestDelegateEvent,
        onState: (status) => {
          if (status === 'connected') setConnState('connected');
          else if (status === 'disconnected') setConnState('disconnected');
          else setConnState('connecting');
        },
      });
    },
    stop() {
      if (unsubscribe) {
        unsubscribe();
        unsubscribe = null;
      }
    },

    // Direct-drive ingestion — for tests and non-SSE hosts.
    ingestSnapshot,
    ingestLoopEvent,
    ingestDelegateEvent,
  };
}
