// data/auth.js — the console's sign-in.
//
// The native API is gated when the operator has configured a token. The
// console never stores that token: it posts it once to /v1/auth/login and
// receives an HttpOnly, SameSite=Strict session cookie the browser sends
// on every later fetch and on the SSE stream. ensureSession() runs before
// the console boots and blocks on the sign-in overlay until a session
// exists; client.js calls requireSignIn() when a later request comes back
// 401, which covers a session that expired or a restart that forgot it.

const SESSION_URL = '/v1/auth/session';
const LOGIN_URL = '/v1/auth/login';
const LOGOUT_URL = '/v1/auth/logout';

let overlay = null;
let pending = null;

// ensureSession resolves once the console may talk to the API: either no
// credential is required, or the browser holds a valid session. Otherwise
// it shows the overlay and resolves after a successful sign-in.
export async function ensureSession() {
  let state = null;
  try {
    const resp = await fetch(SESSION_URL, { cache: 'no-store' });
    if (resp.ok) state = await resp.json();
  } catch (e) { /* offline; the connection badge reports it */ }
  if (!state || !state.auth_required || state.authenticated) {
    renderSignOut(state);
    return state;
  }
  await requireSignIn();
  return state;
}

// requireSignIn shows the overlay (once, however many callers ask) and
// resolves when a sign-in succeeds. The page reloads on success so every
// module restarts against an authenticated session rather than each one
// retrying on its own.
export function requireSignIn() {
  if (pending) return pending;
  pending = new Promise((resolve) => {
    overlay = overlay || buildOverlay();
    overlay.hidden = false;
    const form = overlay.querySelector('form');
    const input = overlay.querySelector('input');
    const error = overlay.querySelector('.auth-error');
    input.value = '';
    error.textContent = '';
    input.focus();
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      error.textContent = '';
      const token = input.value.trim();
      if (!token) return;
      form.querySelector('button').disabled = true;
      try {
        const resp = await fetch(LOGIN_URL, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ token }),
        });
        if (resp.ok) {
          resolve();
          location.reload();
          return;
        }
        error.textContent = resp.status === 401 ? 'That token was not accepted.' : 'Sign-in failed (' + resp.status + ').';
      } catch (e) {
        error.textContent = 'Could not reach Thane.';
      } finally {
        form.querySelector('button').disabled = false;
        input.value = '';
        input.focus();
      }
    };
  });
  return pending;
}

// signOut revokes the session and reloads into the sign-in.
export async function signOut() {
  try { await fetch(LOGOUT_URL, { method: 'POST' }); } catch (e) { /* reload anyway */ }
  location.reload();
}

function buildOverlay() {
  const el = document.createElement('section');
  el.id = 'auth-overlay';
  el.className = 'auth-overlay';
  el.setAttribute('role', 'dialog');
  el.setAttribute('aria-modal', 'true');
  el.setAttribute('aria-labelledby', 'auth-title');
  el.innerHTML =
    '<form class="auth-card" autocomplete="off">' +
    '<h2 id="auth-title" class="auth-title">Sign in to Thane</h2>' +
    '<p class="auth-help">Paste an operator token from <code>listen.auth.tokens</code>. ' +
    'The console keeps only a session cookie, never the token.</p>' +
    '<input type="password" name="token" class="auth-input" placeholder="API token" ' +
    'autocapitalize="off" autocorrect="off" spellcheck="false" required>' +
    '<div class="auth-row"><button type="submit" class="auth-button">Sign in</button>' +
    '<span class="auth-error" role="alert" aria-live="assertive"></span></div>' +
    '</form>';
  document.body.appendChild(el);
  return el;
}

function renderSignOut(state) {
  const btn = document.getElementById('auth-signout');
  if (!btn) return;
  if (state && state.auth_required && state.authenticated) {
    btn.hidden = false;
    const who = state.principal && state.principal.name ? state.principal.name : '';
    btn.title = who ? 'Signed in as ' + who + ' — sign out' : 'Sign out';
    btn.onclick = signOut;
  } else {
    btn.hidden = true;
  }
}
