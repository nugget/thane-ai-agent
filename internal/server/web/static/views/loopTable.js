// views/loopTable.js — the process table.
//
// A flat, sortable view of the running loops (top/htop/Activity-Monitor style)
// — an alternative rendering of the SAME data that backs the node graph, read
// from the shared loop store. Scoped by the shared anchor (subtree); selection
// and anchor sync with every other view via the shared view-state.
//
// loopTableView(getStore, viewState) returns the router's surface interface
// ({ mount, unmount }). getStore is a thunk so the (graph-owned) store is
// resolved at mount time, after boot.

import { subtree, ancestorPath } from '../data/loops.js';

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
  // Guard NaN and the Go zero time (0001-01-01, a large negative epoch) — loops
  // that have never woken serialize last_wake_at as the zero value.
  if (Number.isNaN(t) || t < 0) return '—';
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return s + 's';
  if (s < 3600) return Math.round(s / 60) + 'm';
  if (s < 86400) return Math.round(s / 3600) + 'h';
  return Math.round(s / 86400) + 'd';
}

function shortModel(m) {
  if (!m) return '';
  return String(m).replace(/-\d{8,}$/, '').replace(/^.*\//, '');
}

function loopModel(l) {
  return l._liveModel || l._lastModel || (l.config && l.config.Model) || '';
}

function activeToolNames(l) {
  return (l._liveTools || []).filter((t) => t && t.status === 'running').map((t) => t.tool);
}

// --- columns: value() drives sorting, cell() renders the <td> content ---

const COLUMNS = [
  { key: 'name', label: 'Loop', align: 'left', value: (l) => (l.name || l.id).toLowerCase() },
  { key: 'state', label: 'State', align: 'left', value: (l) => l.state || '' },
  { key: 'iter', label: 'Iter', align: 'right', value: (l) => l.iterations || 0 },
  { key: 'tokens', label: 'Tokens (in→out)', align: 'right', value: (l) => (l.last_output_tokens || 0) + (l.last_input_tokens || 0) },
  { key: 'context', label: 'Context', align: 'right', value: (l) => l.context_window || 0 },
  { key: 'activity', label: 'Activity', align: 'left', value: (l) => activeToolNames(l).length },
  { key: 'wake', label: 'Last wake', align: 'right', value: (l) => Date.parse(l.last_wake_at || '') || 0 },
  { key: 'err', label: 'Err', align: 'right', value: (l) => l.consecutive_errors || 0 },
];

export function loopTableView(getStore, viewState) {
  let store = null;
  let unsubStore = null;
  let unsubView = null;
  let root = null;
  let tbody = null;
  let crumbEl = null;
  const sort = { key: 'state', dir: 1 };

  function setCell(td, col, l) {
    td.className = 'proc-td proc-td--' + col.align;
    switch (col.key) {
      case 'name':
        td.textContent = l.name || l.id;
        td.classList.add('proc-name');
        break;
      case 'state': {
        const b = document.createElement('span');
        b.className = 'proc-badge proc-badge--' + (l.state || 'unknown');
        b.textContent = l.state || 'unknown';
        td.appendChild(b);
        break;
      }
      case 'iter':
        td.textContent = fmtNum(l.iterations || 0);
        break;
      case 'tokens':
        td.textContent = (l.last_input_tokens || l.last_output_tokens)
          ? fmtNum(l.last_input_tokens || 0) + '→' + fmtNum(l.last_output_tokens || 0)
          : '—';
        break;
      case 'context':
        td.textContent = fmtCtx(l.context_window);
        break;
      case 'activity': {
        const tools = activeToolNames(l);
        if (tools.length) {
          td.textContent = tools.join(', ');
          td.classList.add('proc-active');
        } else {
          td.textContent = shortModel(loopModel(l)) || '—';
          td.classList.add('proc-dim');
        }
        break;
      }
      case 'wake':
        td.textContent = relTime(l.last_wake_at);
        break;
      case 'err':
        td.textContent = l.consecutive_errors ? String(l.consecutive_errors) : '';
        if (l.consecutive_errors) td.classList.add('proc-err');
        break;
    }
  }

  function renderCrumb() {
    crumbEl.replaceChildren();
    const root_ = document.createElement('button');
    root_.type = 'button';
    root_.className = 'proc-crumb' + (viewState.anchor ? '' : ' is-current');
    root_.textContent = 'core';
    root_.addEventListener('click', () => viewState.resetAnchor());
    crumbEl.appendChild(root_);

    const path = viewState.anchor ? ancestorPath(store.getLoops(), viewState.anchor) : [];
    for (let i = 0; i < path.length; i++) {
      const sep = document.createElement('span');
      sep.className = 'proc-crumb-sep';
      sep.textContent = '›';
      crumbEl.appendChild(sep);
      const seg = document.createElement('button');
      seg.type = 'button';
      seg.className = 'proc-crumb' + (i === path.length - 1 ? ' is-current' : '');
      seg.textContent = path[i].name || path[i].id;
      const id = path[i].id;
      seg.addEventListener('click', () => viewState.setAnchor(id));
      crumbEl.appendChild(seg);
    }
  }

  function renderRows() {
    const rows = subtree(store.getLoops(), viewState.anchor).slice();
    const col = COLUMNS.find((c) => c.key === sort.key) || COLUMNS[0];
    rows.sort((a, b) => {
      const av = col.value(a), bv = col.value(b);
      if (av < bv) return -sort.dir;
      if (av > bv) return sort.dir;
      return (a.id < b.id ? -1 : 1);
    });

    tbody.replaceChildren();
    for (const l of rows) {
      const tr = document.createElement('tr');
      tr.className = 'proc-row' + (viewState.selection === l.id ? ' is-selected' : '');
      tr.dataset.loopId = l.id;
      for (const c of COLUMNS) {
        const td = document.createElement('td');
        setCell(td, c, l);
        tr.appendChild(td);
      }
      // anchor action cell
      const ad = document.createElement('td');
      ad.className = 'proc-td proc-td--right';
      const ab = document.createElement('button');
      ab.type = 'button';
      ab.className = 'proc-anchor-btn';
      ab.title = 'Anchor here (show this subtree)';
      ab.textContent = '⊙';
      ab.addEventListener('click', (e) => { e.stopPropagation(); viewState.setAnchor(l.id); });
      ad.appendChild(ab);
      tr.appendChild(ad);

      tr.addEventListener('click', () => viewState.setSelection(l.id));
      tbody.appendChild(tr);
    }

    if (!rows.length) {
      const tr = document.createElement('tr');
      const td = document.createElement('td');
      td.className = 'proc-empty';
      td.colSpan = COLUMNS.length + 1;
      td.textContent = 'No loops in scope.';
      tr.appendChild(td);
      tbody.appendChild(tr);
    }
  }

  function rerender() {
    if (!root) return;
    renderCrumb();
    renderRows();
  }

  function buildHeader(thead) {
    const tr = document.createElement('tr');
    for (const c of COLUMNS) {
      const th = document.createElement('th');
      th.className = 'proc-th proc-th--' + c.align + (sort.key === c.key ? ' is-sorted' : '');
      th.textContent = c.label + (sort.key === c.key ? (sort.dir > 0 ? ' ▲' : ' ▼') : '');
      th.addEventListener('click', () => {
        if (sort.key === c.key) sort.dir = -sort.dir;
        else { sort.key = c.key; sort.dir = 1; }
        rerender();
      });
      tr.appendChild(th);
    }
    const th = document.createElement('th');
    th.className = 'proc-th proc-th--right';
    tr.appendChild(th); // anchor column
    thead.appendChild(tr);
  }

  return {
    mount(mountRoot) {
      root = mountRoot;
      store = getStore();

      const surface = document.createElement('div');
      surface.className = 'surface proc-surface';

      const header = document.createElement('div');
      header.className = 'surface-header';
      const h = document.createElement('h2');
      h.textContent = 'Processes';
      header.appendChild(h);
      crumbEl = document.createElement('div');
      crumbEl.className = 'proc-crumbs';
      header.appendChild(crumbEl);
      surface.appendChild(header);

      const table = document.createElement('table');
      table.className = 'proc-table';
      const thead = document.createElement('thead');
      buildHeader(thead);
      table.appendChild(thead);
      tbody = document.createElement('tbody');
      table.appendChild(tbody);
      surface.appendChild(table);

      root.appendChild(surface);

      rerender();
      // Re-render on data changes (live events) and on anchor/selection changes.
      if (store) unsubStore = store.subscribe(rerender);
      unsubView = viewState.subscribe(rerender);
    },

    unmount() {
      if (unsubStore) unsubStore();
      if (unsubView) unsubView();
      unsubStore = unsubView = null;
      root = tbody = crumbEl = null;
    },
  };
}
