// Applies the persisted theme before first paint to avoid a flash of the
// default look. Mirrors theme.js; kept tiny and dependency-free.
//
// This is a separate file rather than an inline block in index.html so
// the console's content policy can name 'self' and nothing else. A
// blocking <script src> in <head> runs before first paint just as an
// inline block does, so the anti-flash property is unchanged; the cost
// is one request against an embedded file. The alternative — a nonce —
// would mean index.html could no longer be served as a static asset,
// since each response would need a fresh value templated into it.
(function () {
  try {
    var root = document.documentElement;
    var m = localStorage.getItem('thane.theme.mode');
    if (m === 'light' || m === 'dark') root.dataset.theme = m;
    var a = localStorage.getItem('thane.theme.accent');
    if (a && /^#[0-9a-f]{6}$/i.test(a)) {
      root.style.setProperty('--accent', a);
    }
  } catch (e) { /* storage blocked — fall back to defaults */ }
})();
