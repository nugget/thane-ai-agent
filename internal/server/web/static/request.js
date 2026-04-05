'use strict';

const $ = (sel) => document.querySelector(sel);

const urlParams = new URLSearchParams(window.location.search);
const requestID = urlParams.get('id') || '';
const REQUEST_DETAIL_PROBE_PATH = '/api/request-detail/_probe';

const els = {
  title: $('#request-window-title'),
  subtitle: $('#request-window-subtitle'),
  ids: $('#request-window-ids'),
  meta: $('#request-window-meta'),
  content: $('#request-window-content'),
  waterfall: $('#request-window-waterfall'),
  empty: $('#request-window-empty'),
  refresh: $('#request-window-refresh'),
  copy: $('#request-window-copy'),
};

let activeRequestJSON = '';
let refreshTimer = null;
let requestDetailAvailable = null;
let copyStatusTimer = null;

function makeMetaBarItem(label, value, opts = {}) {
  const item = document.createElement('span');
  item.className = 'request-meta-bar__item';

  const labelEl = document.createElement('span');
  labelEl.className = 'request-meta-bar__label';
  labelEl.textContent = label;

  const valueEl = document.createElement('span');
  valueEl.className = 'request-meta-bar__value' + (opts.warn ? ' request-meta-bar__value--warn' : '');
  valueEl.textContent = value;

  item.appendChild(labelEl);
  item.appendChild(document.createTextNode(' '));
  item.appendChild(valueEl);
  return item;
}

function renderUnavailableState(message, subtitle = 'Request detail is not available in this runtime.') {
  els.title.textContent = 'Request ' + shortID(requestID);
  els.subtitle.textContent = subtitle;
  els.ids.innerHTML = '';
  if (requestID) {
    els.ids.appendChild(makeIDRow('request_id', requestID));
  }
  els.meta.innerHTML = '';
  const meta = document.createElement('div');
  meta.className = 'request-meta-bar';
  meta.appendChild(makeMetaBarItem('status', message, { warn: true }));
  els.meta.appendChild(meta);
  els.empty.textContent = message;
  setRequestLoaded(false);
}

function setRequestLoaded(loaded) {
  if (loaded) {
    els.empty.textContent = '';
  }
  els.empty.hidden = loaded;
  els.content.hidden = !loaded;
  els.waterfall.hidden = !loaded;
}

function updateWindowTitle() {
  document.title = 'Thane \u00b7 Request ' + (requestID ? shortID(requestID) : 'Unknown');
}

function setCopyButtonState(label, copied) {
  if (!els.copy) return;
  els.copy.textContent = label;
  els.copy.classList.toggle('copy-btn--copied', Boolean(copied));
}

function queueCopyButtonReset(delay = 1200) {
  if (copyStatusTimer) clearTimeout(copyStatusTimer);
  copyStatusTimer = setTimeout(() => {
    copyStatusTimer = null;
    setCopyButtonState('JSON', false);
  }, delay);
}

async function copyText(text) {
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    await navigator.clipboard.writeText(text);
    return;
  }

  const input = document.createElement('textarea');
  input.value = text;
  input.setAttribute('readonly', 'readonly');
  input.style.position = 'fixed';
  input.style.opacity = '0';
  document.body.appendChild(input);
  input.focus();
  input.select();
  const copied = document.execCommand('copy');
  document.body.removeChild(input);
  if (!copied) {
    throw new Error('clipboard copy was rejected');
  }
}

async function fetchRequestDetail() {
  if (!requestID) {
    els.empty.textContent = 'No request ID was provided.';
    setRequestLoaded(false);
    return;
  }

  if (requestDetailAvailable === false) {
    renderUnavailableState(
      'Content retention is disabled for this runtime, so the full prompt, messages, and waterfall are unavailable.',
      'Request anchor and copy helpers remain available even when retained content is off.',
    );
    return;
  }

  try {
    const resp = await fetch('/api/requests/' + encodeURIComponent(requestID));
    if (!resp.ok) {
      if (resp.status === 503) {
        requestDetailAvailable = false;
        renderUnavailableState(
          'Content retention is disabled for this runtime, so this request window cannot load full request content.',
          'Request anchor and copy helpers remain available even when retained content is off.',
        );
      } else if (resp.status === 404) {
        renderUnavailableState(
          'This request detail is unavailable. It may have aged out of the live buffer, or archival storage may not include this turn.',
          'Stored request content is missing for this request ID.',
        );
      } else {
        els.empty.textContent = 'Request detail failed to load (' + resp.status + ').';
        setRequestLoaded(false);
      }
      return;
    }

    const detail = await resp.json();
    activeRequestJSON = JSON.stringify(detail, null, 2);

    const titleBits = ['Request ' + shortID(requestID)];
    if (detail.model) titleBits.push(shortModelName(detail.model));
    if (detail.iteration_count) titleBits.push(formatNumber(detail.iteration_count) + ' iterations');
    els.title.textContent = titleBits.join(' \u00b7 ');

    const subtitleBits = [];
    if (detail.exhausted) subtitleBits.push('exhausted ' + (detail.exhaust_reason || ''));
    if (detail.prompt_hash) subtitleBits.push('prompt ' + shortID(detail.prompt_hash));
    if (detail.input_tokens || detail.output_tokens) {
      subtitleBits.push(formatTokens(detail.input_tokens || 0) + ' in / ' + formatTokens(detail.output_tokens || 0) + ' out');
    }
    els.subtitle.textContent = subtitleBits.filter(Boolean).join(' \u00b7 ') || 'Prompt, content, and tool-call waterfall.';

    renderRequestDetail(detail, {
      ids: els.ids,
      meta: els.meta,
      content: els.content,
      waterfall: els.waterfall,
    });
    setRequestLoaded(true);
  } catch (err) {
    console.warn('Failed to fetch request detail:', err);
    els.empty.textContent = 'Request detail failed to load.';
    setRequestLoaded(false);
  }
}

async function probeContentRetention() {
  try {
    const resp = await fetch(REQUEST_DETAIL_PROBE_PATH);
    requestDetailAvailable = resp.ok && resp.headers.get('X-Request-Detail-Available') === 'true';
  } catch (_) {
    requestDetailAvailable = null;
  }
}

function startRefreshLoop() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = setInterval(() => {
    if (document.hidden) return;
    if (requestDetailAvailable === false) return;
    void fetchRequestDetail();
  }, 3000);
}

els.refresh?.addEventListener('click', () => {
  void fetchRequestDetail();
});

els.copy?.addEventListener('click', () => {
  if (!activeRequestJSON) return;
  void copyText(activeRequestJSON)
    .then(() => {
      setCopyButtonState('Copied', true);
      queueCopyButtonReset();
    })
    .catch((err) => {
      console.warn('Failed to copy request detail JSON:', err);
      setCopyButtonState('Copy failed', false);
      queueCopyButtonReset(1600);
    });
});

window.addEventListener('pagehide', () => {
  if (refreshTimer) clearInterval(refreshTimer);
  if (copyStatusTimer) clearTimeout(copyStatusTimer);
});

updateWindowTitle();
void probeContentRetention().then(fetchRequestDetail);
startRefreshLoop();
