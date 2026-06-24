// views/forensics.js — single-loop live forensics.
//
// A deep, live view of ONE loop (the selected one): status + stats, the live
// tool feed (args in → result out, mid-flight), and the iteration timeline. The
// successor to the old detail.html "live forensics" popup, rebuilt on the
// shared store + /v1. It follows viewState.selection, so selecting a loop in
// the graph or table focuses it here, live.
//
// Everything rendered here comes from the shared store (loop status, live
// tools, iteration history) — no extra fetches yet; the request-detail
// waterfall and per-loop logs are a follow-on.

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

  function focused() {
    return store ? store.getLoop(viewState.selection) : null;
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
    body.appendChild(renderHeader(l));
    body.appendChild(renderToolFeed(l));
    body.appendChild(renderTimeline(l));
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
      if (store) unsubStore = store.subscribe(render);
      unsubView = viewState.subscribe(render);
    },

    unmount() {
      if (unsubStore) unsubStore();
      if (unsubView) unsubView();
      unsubStore = unsubView = null;
      root = body = null;
    },
  };
}
