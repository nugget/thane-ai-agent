// theme.js — light/dark/auto presentation + a choosable accent.
//
// Persisted in localStorage, which is per-origin, so each Thane agent's
// dashboard (a distinct host:port) remembers its own look automatically —
// letting operators tell multiple agents apart at a glance. The accent is the
// single knob: its tints derive from --accent in CSS via color-mix(). A small
// inline script in index.html's <head> applies the persisted values before
// first paint to avoid a flash; this module re-applies idempotently and builds
// the header control.

const MODE_KEY = 'thane.theme.mode';
const ACCENT_KEY = 'thane.theme.accent';
const DEFAULT_ACCENT = '#2dd4bf';
const PRESETS = ['#2dd4bf', '#3b82f6', '#8b5cf6', '#ec4899', '#ef4444', '#f59e0b', '#22c55e', '#06b6d4'];

// localStorage can throw (private mode, storage disabled, quota). Treat it as
// best-effort: reads fall back to defaults, writes are swallowed — theming
// still applies in-memory either way, so the console boots and the picker works
// even when persistence is blocked.
function readStored(key) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeStored(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* persistence unavailable; in-memory state already applied */
  }
}

function applyAccent(hex) {
  document.documentElement.style.setProperty('--accent', hex);
}

// applyMode: 'auto' clears the override so prefers-color-scheme wins;
// 'light'/'dark' force a mode via the data-theme attribute.
function applyMode(mode) {
  if (mode === 'light' || mode === 'dark') {
    document.documentElement.dataset.theme = mode;
  } else {
    delete document.documentElement.dataset.theme;
  }
}

function loadMode() {
  const m = readStored(MODE_KEY);
  return m === 'light' || m === 'dark' ? m : 'auto';
}
function loadAccent() {
  return readStored(ACCENT_KEY) || DEFAULT_ACCENT;
}

function initTheme() {
  let mode = loadMode();
  let accent = loadAccent();
  applyMode(mode);
  applyAccent(accent);

  const btn = document.getElementById('toggle-theme');
  const pop = document.getElementById('theme-popover');
  if (!btn || !pop) return;

  const custom = pop.querySelector('#theme-custom');
  const swatchRow = pop.querySelector('#theme-swatches');

  PRESETS.forEach((color) => {
    const sw = document.createElement('button');
    sw.type = 'button';
    sw.className = 'theme-swatch';
    sw.style.background = color;
    sw.dataset.accent = color;
    sw.title = color;
    sw.addEventListener('click', () => setAccent(color));
    swatchRow.appendChild(sw);
  });

  pop.querySelectorAll('[data-mode]').forEach((el) => {
    el.addEventListener('click', () => {
      mode = el.dataset.mode;
      writeStored(MODE_KEY, mode);
      applyMode(mode);
      sync();
    });
  });

  custom.addEventListener('input', () => setAccent(custom.value));

  function setAccent(color) {
    accent = color;
    writeStored(ACCENT_KEY, color);
    applyAccent(color);
    sync();
  }

  function sync() {
    pop.querySelectorAll('[data-mode]').forEach((el) =>
      el.classList.toggle('is-active', el.dataset.mode === mode));
    pop.querySelectorAll('.theme-swatch').forEach((el) =>
      el.classList.toggle('is-active', el.dataset.accent.toLowerCase() === accent.toLowerCase()));
    custom.value = /^#[0-9a-f]{6}$/i.test(accent) ? accent : DEFAULT_ACCENT;
  }
  sync();

  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    pop.hidden = !pop.hidden;
  });
  document.addEventListener('click', (e) => {
    if (!pop.hidden && !pop.contains(e.target) && e.target !== btn) pop.hidden = true;
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') pop.hidden = true;
  });
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initTheme);
} else {
  initTheme();
}
