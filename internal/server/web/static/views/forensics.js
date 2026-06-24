// views/forensics.js — single-loop live forensics.
//
// A deep, live view of ONE loop (the selected one): status + stats, the live
// tool feed (args in → result out, mid-flight), and the iteration timeline. The
// successor to the old detail.html "live forensics" popup, rebuilt on the
// shared store + /v1. It follows viewState.selection, so selecting a loop in
// the graph or table focuses it here, live.
//
// The live state comes from the shared store (loop status, live tools, iteration
// history); the recent-logs tail is fetched from /v1/loops/{id}/logs. The
// request-detail waterfall is a follow-on.

import { logs as fetchLogTail, tryGet as apiTryGet } from '../data/client.js';

// --- self-contained formatters (no shared.js dependency, for embeddability) ---

function fmtNum(n) {
  n = Number(n) || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k';
  return String(n);
}

function fmtCtx(n) {
  n = Number(n) || 0;
  if (n <= 0) return '—';
  return n >= 1000 ? Math.round(n / 1000) + 'k' : String(n);
}

function relTime(iso) {
  if (!iso) return '—';
  const t = Date.parse(iso);
  if (Number.isNaN(t) || t < 0) return '—';
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.round(s / 60) + 'm ago';
  if (s < 86400) return Math.round(s / 3600) + 'h ago';
  return Math.round(s / 86400) + 'd ago';
}

function shortModel(m) {
  if (!m) return '';
  return String(m).replace(/-\d{8,}$/, '').replace(/^.*\//, '');
}

function loopModel(l) {
  return l._liveModel || l._lastModel || (l.config && l.config.Model) || '';
}

// el is a tiny element helper: el('div', {class:'x'}, [children|text]).
function el(tag, attrs, kids) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') node.className = v;
      else if (k === 'text') node.textContent = v;
      else node.setAttribute(k, v);
    }
  }
  if (kids != null) {
    for (const c of [].concat(kids)) {
      if (c == null) continue;
      node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    }
  }
  return node;
}

function stat(label, value, opts = {}) {
  const v = el('div', { class: 'fx-stat-val' + (opts.emphasis ? ' fx-emph' : '') + (opts.danger ? ' fx-danger' : '') });
  v.textContent = value;
  return el('div', { class: 'fx-stat' }, [el('div', { class: 'fx-stat-label', text: label }), v]);
}

export function forensicsView(getStore, viewState) {
  let store = null;
  let root = null;
  let body = null;
  let unsubStore = null;
  let unsubView = null;
  let unsubIter = null;
  let logsEntries = [];
  let logsFor = null;
  let reqDetail = null;
  let reqFor = null;

  function focused() {
    return store ? store.getLoop(viewState.selection) : null;
  }

  // loadLogs fetches the focused loop's /v1 log tail (async) and re-renders.
  // logsFor is set synchronously so render() won't re-trigger the fetch.
  async function loadLogs(id) {
    logsFor = id;
    logsEntries = [];
    if (!id) return;
    const entries = await fetchLogTail('/loops/' + encodeURIComponent(id) + '/logs?limit=50');
    if (logsFor !== id) return; // focus changed mid-fetch
    logsEntries = Array.isArray(entries) ? entries : [];
    render();
  }

  // loadRequest fetches the focused loop's most recent request detail (the
  // tool-call waterfall) from /v1/requests/{id}. Same async-guard pattern.
  async function loadRequest(id) {
    reqFor = id;
    reqDetail = null;
    const l = store && store.getLoop(id);
    const history = (store && store.iterationHistory.get(id)) || [];
    const reqId = (l && l._currentRequestID) || (history[0] && history[0].request_id) || '';
    if (!reqId) return;
    const detail = await apiTryGet('/requests/' + encodeURIComponent(reqId));
    if (reqFor !== id) return; // focus changed mid-fetch
    reqDetail = detail || null;
    render();
  }

  function renderHeader(l) {
    const tokens = l.last_input_tokens || l.last_output_tokens
      ? fmtNum(l.last_input_tokens || 0) + '→' + fmtNum(l.last_output_tokens || 0)
      : '—';
    const grid = el('div', { class: 'fx-stats' }, [
      stat('State', l.state || 'unknown', { emphasis: l.state === 'processing', danger: l.state === 'error' }),
      stat('Model', shortModel(loopModel(l)) || '—'),
      stat('Iterations', fmtNum(l.iterations || 0)),
      stat('Last tokens', tokens),
      stat('Context', fmtCtx(l.context_window)),
      stat('Errors', String(l.consecutive_errors || 0), { danger: !!l.consecutive_errors }),
      stat('Last wake', relTime(l.last_wake_at)),
      stat('Parent', l.parent_id || 'core'),
    ]);
    return el('div', { class: 'fx-header' }, [
      el('div', { class: 'fx-title' }, [
        el('span', { class: 'fx-name', text: l.name || l.id }),
        el('span', { class: 'proc-badge proc-badge--' + (l.state || 'unknown'), text: l.state || 'unknown' }),
      ]),
      grid,
    ]);
  }

  function renderToolFeed(l) {
    const tools = l._liveTools || [];
    const section = el('section', { class: 'fx-section' }, [
      el('h3', { class: 'fx-section-title', text: 'Live tools — this turn' }),
    ]);
    if (!tools.length) {
      section.appendChild(el('div', { class: 'fx-idle', text: l.state === 'processing' ? 'Working — no tool in flight right now.' : 'Idle — no tools in flight.' }));
      return section;
    }
    for (const t of tools) {
      const head = el('div', { class: 'fx-tool-head' }, [
        el('span', { class: 'fx-tool-name', text: t.tool || '?' }),
        el('span', { class: 'fx-tool-status fx-tool-status--' + (t.error ? 'error' : t.status || 'done'), text: t.error ? 'error' : (t.status || 'done') }),
      ]);
      const card = el('div', { class: 'fx-tool' }, [head]);
      if (t.args) card.appendChild(el('pre', { class: 'fx-tool-args', text: String(t.args).slice(0, 600) }));
      if (t.error) card.appendChild(el('pre', { class: 'fx-tool-result fx-danger', text: String(t.error).slice(0, 600) }));
      else if (t.result) card.appendChild(el('pre', { class: 'fx-tool-result', text: String(t.result).slice(0, 600) }));
      section.appendChild(card);
    }
    return section;
  }

  function renderTimeline(l) {
    const hist = (store.iterationHistory.get(l.id) || []);
    const section = el('section', { class: 'fx-section' }, [
      el('h3', { class: 'fx-section-title', text: 'Iterations' }),
    ]);
    if (!hist.length) {
      section.appendChild(el('div', { class: 'fx-idle', text: 'No iterations recorded yet.' }));
      return section;
    }
    const list = el('div', { class: 'fx-timeline' });
    for (const s of hist.slice(0, 20)) {
      const tools = s.tools_used && typeof s.tools_used === 'object' ? Object.entries(s.tools_used) : [];
      const chips = el('div', { class: 'fx-iter-tools' }, tools.map(([name, cnt]) =>
        el('span', { class: 'fx-chip', text: cnt > 1 ? name + '×' + cnt : name })));
      list.appendChild(el('div', { class: 'fx-iter' }, [
        el('span', { class: 'fx-iter-num', text: '#' + (s.number || '—') }),
        el('span', { class: 'fx-iter-model', text: shortModel(s.model) || '—' }),
        el('span', { class: 'fx-iter-tokens', text: fmtNum(s.input_tokens || 0) + '→' + fmtNum(s.output_tokens || 0) }),
        chips,
      ]));
    }
    section.appendChild(list);
    return section;
  }

  function renderLogs() {
    const section = el('section', { class: 'fx-section' }, [
      el('h3', { class: 'fx-section-title', text: 'Recent logs' }),
    ]);
    if (!logsEntries.length) {
      section.appendChild(el('div', { class: 'fx-idle', text: 'No recent log entries.' }));
      return section;
    }
    const list = el('div', { class: 'fx-logs' });
    for (const e of logsEntries) {
      list.appendChild(el('div', { class: 'fx-log fx-log--' + (e.Level || 'info').toLowerCase() }, [
        el('span', { class: 'fx-log-time', text: relTime(e.Timestamp) }),
        el('span', { class: 'fx-log-level', text: (e.Level || '').toUpperCase() }),
        el('span', { class: 'fx-log-msg', text: e.Tool ? '[' + e.Tool + '] ' + (e.Msg || '') : (e.Msg || '') }),
      ]));
    }
    section.appendChild(list);
    return section;
  }

  function renderWaterfall() {
    const section = el('section', { class: 'fx-section' }, [
      el('h3', { class: 'fx-section-title', text: 'Last request — tool calls' }),
    ]);
    const calls = reqDetail && Array.isArray(reqDetail.tool_calls) ? reqDetail.tool_calls : [];
    if (!calls.length) {
      section.appendChild(el('div', { class: 'fx-idle', text: 'No completed request detail yet.' }));
      return section;
    }
    for (const tc of calls) {
      const card = el('div', { class: 'fx-tool' }, [
        el('div', { class: 'fx-tool-head' }, [
          el('span', { class: 'fx-tool-name', text: tc.tool_name || '?' }),
          el('span', { class: 'fx-tool-status fx-tool-status--done', text: 'done' }),
        ]),
      ]);
      if (tc.arguments) card.appendChild(el('pre', { class: 'fx-tool-args', text: String(tc.arguments).slice(0, 800) }));
      if (tc.result) card.appendChild(el('pre', { class: 'fx-tool-result', text: String(tc.result).slice(0, 800) }));
      section.appendChild(card);
    }
    return section;
  }

  function render() {
    if (!body) return;
    body.replaceChildren();
    const l = focused();
    if (!l) {
      body.appendChild(el('div', { class: 'surface-empty' }, [
        el('p', { text: 'Select a loop — in the graph or the process table — to watch it live here.' }),
      ]));
      return;
    }
    if (l.id !== logsFor) loadLogs(l.id); // focus changed → (re)load the log tail
    if (l.id !== reqFor) loadRequest(l.id); // and the latest request's waterfall
    body.appendChild(renderHeader(l));
    body.appendChild(renderToolFeed(l));
    body.appendChild(renderWaterfall());
    body.appendChild(renderTimeline(l));
    body.appendChild(renderLogs());
  }

  return {
    mount(mountRoot) {
      root = mountRoot;
      store = getStore();

      const surface = el('div', { class: 'surface fx-surface' }, [
        el('div', { class: 'surface-header' }, [el('h2', { text: 'Forensics' })]),
      ]);
      body = el('div', { class: 'fx-body' });
      surface.appendChild(body);
      root.appendChild(surface);

      render();
      if (store) {
        unsubStore = store.subscribe(render);
        unsubIter = store.on('iteration_complete', ({ loopId }) => {
          if (loopId === viewState.selection) {
            loadLogs(loopId);
            loadRequest(loopId);
          }
        });
      }
      unsubView = viewState.subscribe(render);
    },

    unmount() {
      if (unsubStore) unsubStore();
      if (unsubIter) unsubIter();
      if (unsubView) unsubView();
      unsubStore = unsubIter = unsubView = null;
      root = body = null;
    },
  };
}
