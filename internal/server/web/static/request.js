'use strict';

const $ = (sel) => document.querySelector(sel);

const urlParams = new URLSearchParams(window.location.search);
const requestID = urlParams.get('id') || '';

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

function setRequestLoaded(loaded) {
  els.empty.hidden = loaded;
  els.content.hidden = !loaded;
  els.waterfall.hidden = !loaded;
}

function updateWindowTitle() {
  document.title = 'Thane \u00b7 Request ' + (requestID ? shortID(requestID) : 'Unknown');
}

async function fetchRequestDetail() {
  if (!requestID) {
    els.empty.textContent = 'No request ID was provided.';
    setRequestLoaded(false);
    return;
  }

  try {
    const resp = await fetch('/api/requests/' + encodeURIComponent(requestID));
    if (!resp.ok) {
      if (resp.status === 404) {
        els.empty.textContent = 'Request detail is unavailable. Content retention may be disabled or the request may have been evicted.';
      } else {
        els.empty.textContent = 'Request detail failed to load (' + resp.status + ').';
      }
      setRequestLoaded(false);
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

function startRefreshLoop() {
  if (refreshTimer) clearInterval(refreshTimer);
  refreshTimer = setInterval(() => {
    if (document.hidden) return;
    void fetchRequestDetail();
  }, 3000);
}

els.refresh?.addEventListener('click', () => {
  void fetchRequestDetail();
});

els.copy?.addEventListener('click', () => {
  if (!activeRequestJSON) return;
  navigator.clipboard.writeText(activeRequestJSON).then(() => {
    els.copy.textContent = 'Copied';
    els.copy.classList.add('copy-btn--copied');
    setTimeout(() => {
      els.copy.textContent = 'JSON';
      els.copy.classList.remove('copy-btn--copied');
    }, 1200);
  });
});

window.addEventListener('pagehide', () => {
  if (refreshTimer) clearInterval(refreshTimer);
});

updateWindowTitle();
void fetchRequestDetail();
startRefreshLoop();
