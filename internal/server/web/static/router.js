// router.js — hash router for the console shell.
//
// The node graph is the home view and keeps its own DOM (the <main> region and
// the log panel); the router shows or hides that and swaps surface views
// (Models, Loops, Usage, Schedule) into #view-root. Request-detail deep links
// (#request/<id>) stay owned by the graph (app.js), so the router treats any
// hash that isn't a registered surface as "graph mode". All navigation is
// client-side via location.hash, which the Go server never sees — so deep
// links survive reload (the server always serves the same index.html) and the
// browser Back/Forward buttons step between surfaces.

const surfaces = new Map(); // name -> view: { mount(root, params), unmount?(), update?(params) }
let viewRoot = null;
let navEl = null;
let graphEls = [];
let current = null; // { name, view }

// registerSurface adds a routable surface keyed by its first hash segment
// (e.g. "models" for #/models).
export function registerSurface(name, view) {
  surfaces.set(name, view);
}

// link builds a hash for a route — link('models') or
// link('loop-definitions', 'cota_observer'). One source of truth for hash
// shapes so cross-links don't drift from the route table.
export function link(name, param) {
  return '#/' + name + (param != null && param !== '' ? '/' + encodeURIComponent(param) : '');
}

function parseHash() {
  const seg = location.hash.replace(/^#\/?/, '').split('/');
  return { head: seg[0] || '', rest: seg.slice(1).map((s) => decodeURIComponent(s)) };
}

function setGraphVisible(visible) {
  for (const el of graphEls) el.style.display = visible ? '' : 'none';
}

function showGraph() {
  if (current) {
    if (current.view.unmount) current.view.unmount();
    current = null;
  }
  if (viewRoot) viewRoot.hidden = true;
  setGraphVisible(true);
}

function showSurface(name, params) {
  setGraphVisible(false);
  viewRoot.hidden = false;
  if (current && current.name === name) {
    if (current.view.update) current.view.update(params);
    return;
  }
  if (current && current.view.unmount) current.view.unmount();
  viewRoot.replaceChildren();
  const view = surfaces.get(name);
  current = { name, view };
  view.mount(viewRoot, params);
  // Move focus to the freshly mounted surface so keyboard/screen-reader users
  // land on the new content instead of the now-hidden graph.
  viewRoot.focus({ preventScroll: true });
}

function route() {
  const { head, rest } = parseHash();
  if (surfaces.has(head)) {
    showSurface(head, rest);
  } else {
    showGraph();
  }
  const active = surfaces.has(head) ? head : 'graph';
  if (navEl) {
    navEl.querySelectorAll('[data-route]').forEach((a) => {
      const on = a.dataset.route === active;
      a.classList.toggle('is-active', on);
      if (on) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
  }
}

// initRouter wires the hashchange listener and resolves the current hash. Call
// it after the shell DOM and the graph exist.
export function initRouter() {
  viewRoot = document.getElementById('view-root');
  navEl = document.querySelector('.nav');
  graphEls = ['main.main', '.log-panel', '.resize-handle--h']
    .map((s) => document.querySelector(s))
    .filter(Boolean);
  window.addEventListener('hashchange', route);
  route();
}
