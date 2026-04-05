// Detail popup — standalone window for inspecting a single node.
// Reads ?type=loop&id=xxx or ?type=system from URL params.

'use strict';

const $ = (sel) => document.querySelector(sel);
const logEmpty = $('#log-empty');
const logScroll = $('#log-scroll');
const logBody = $('#log-body');
const logsSection = $('#logs-section');
const requestSection = $('#request-section');
const logControlsRow = $('#log-controls-row');
const connDot = $('#conn-dot');
const pollSlider = $('#poll-rate');
const pollLabel = $('#poll-rate-label');
const forensics = {
  title: $('#forensics-title'),
  subtitle: $('#forensics-subtitle'),
  current: $('#forensics-current'),
  follow: $('#forensics-follow'),
  openRequest: $('#forensics-open-request'),
  copyJSON: $('#forensics-copy-json'),
  ids: $('#forensics-ids'),
  meta: $('#forensics-meta'),
  requestMeta: $('#forensics-request-meta'),
  waterfallMeta: $('#forensics-waterfall-meta'),
  empty: $('#forensics-empty'),
  content: $('#forensics-content'),
  waterfall: $('#forensics-waterfall'),
};
const popupFooter = {
  version: $('#popup-footer-version'),
  uptime: $('#popup-footer-uptime'),
  arch: $('#popup-footer-arch'),
  go: $('#popup-footer-go'),
  loopState: $('#popup-footer-loop-state'),
  loopRuntime: $('#popup-footer-loop-runtime'),
  loopIterations: $('#popup-footer-loop-iterations'),
  loopTokens: $('#popup-footer-loop-tokens'),
  loopContext: $('#popup-footer-loop-context'),
  loopWake: $('#popup-footer-loop-wake'),
};

// ---------------------------------------------------------------------------
// Connection Status (dot indicator instead of status bar)
// ---------------------------------------------------------------------------

function setConnStatus(state, detail) {
  connDot.className = 'conn-dot' + (state === 'ok' ? ' conn-dot--ok' : state === 'err' ? ' conn-dot--err' : '');
  connDot.title = detail || state;
}

function openRequestWindow(requestID) {
  if (!requestID) return;
  const name = 'Request ' + shortID(requestID);
  const w = window.open(
    '/static/request.html?id=' + encodeURIComponent(requestID),
    'request-' + requestID,
    'popup=yes,width=1180,height=860',
  );
  if (w) {
    w.addEventListener('load', () => {
      w.document.title = 'Thane \u00b7 ' + name;
    });
  }
}

// ---------------------------------------------------------------------------
// Dynamic Log Poll Rate
// ---------------------------------------------------------------------------

let logPollInterval = null;
let activeRequestID = '';
let activeRequestJSON = '';
let pinnedRequestID = '';
let followLatestRequest = true;
let serverStartTime = null;
let requestDetailAvailable = null;
let requestDetailProbeInFlight = null;
let requestDetailCooldown = { requestID: '', status: 0, until: 0 };

function startLogPoll() {
  if (logPollInterval) clearInterval(logPollInterval);
  const secs = parseInt(pollSlider.value, 10);
  pollLabel.textContent = secs + 's';
  const fetchFn = nodeType === 'system' ? fetchSystemLogs : fetchLoopLogs;
  logPollInterval = setInterval(fetchFn, secs * 1000);
}

async function fetchVersionInfo() {
  try {
    const resp = await fetch('/v1/version');
    if (!resp.ok) return;
    const info = await resp.json();

    const ver = info.version || 'dev';
    const commit = (info.git_commit || 'unknown').slice(0, 7);
    if (popupFooter.version) popupFooter.version.textContent = ver + ' (' + commit + ')';
    if (popupFooter.arch) popupFooter.arch.textContent = (info.os || '') + '/' + (info.arch || '');
    if (popupFooter.go) popupFooter.go.textContent = info.go_version || '';

    if (info.uptime) {
      const uptimeMs = parseDuration(info.uptime);
      serverStartTime = Date.now() - uptimeMs;
    }

    updatePopupFooter();
  } catch (err) {
    console.warn('Failed to fetch version info:', err);
  }
}

function setFooterItem(el, value, extraClass = '') {
  if (!el) return;
  el.hidden = !value;
  el.textContent = value || '';
  el.className = 'footer-item' + (extraClass ? ' ' + extraClass : '');
}

function updatePopupUptime() {
  if (!popupFooter.uptime || serverStartTime === null) return;
  popupFooter.uptime.textContent = 'up ' + formatUptimeLong(Date.now() - serverStartTime);
}

function formatLoopFooterWake(loop) {
  if (!loop) return '';
  if (loop.state === 'sleeping') {
    const sleepTimer = sleepTimers.get(nodeId);
    if (sleepTimer && sleepTimer.durationMs > 0) {
      const remaining = sleepTimer.durationMs - (Date.now() - sleepTimer.startedAt);
      if (remaining > 0) return 'sleep ' + formatDuration(remaining);
    }
    return 'sleeping';
  }
  if (loop.state === 'waiting') return 'awaiting event';
  const lastWake = parseTimestamp(loop.last_wake_at);
  if (!lastWake) return '';
  return 'wake ' + timeAgo(lastWake);
}

function updatePopupFooter() {
  updatePopupUptime();
  if (nodeType !== 'loop' || !loopData) {
    setFooterItem(popupFooter.loopState, '');
    setFooterItem(popupFooter.loopRuntime, '');
    setFooterItem(popupFooter.loopIterations, '');
    setFooterItem(popupFooter.loopTokens, '');
    setFooterItem(popupFooter.loopContext, '');
    setFooterItem(popupFooter.loopWake, '');
    return;
  }

  const loopStarted = parseTimestamp(loopData.started_at);
  const totalTokens = (loopData.total_input_tokens || 0) + (loopData.total_output_tokens || 0);
  const iterations = loopData._delegate ? (loopData._delegateIterations || 0) : (loopData.iterations || 0);
  const attempts = loopData.attempts || 0;

  setFooterItem(
    popupFooter.loopState,
    formatSchemaToken(loopData.state || 'pending'),
    loopData.state === 'processing' ? 'footer-item--live' : 'footer-item--accent',
  );
  setFooterItem(popupFooter.loopRuntime, loopStarted ? 'loop ' + formatUptimeLong(Date.now() - loopStarted.getTime()) : '');
  setFooterItem(
    popupFooter.loopIterations,
    iterations > 0 || attempts > 0
      ? `${formatNumber(iterations)} iter${attempts !== iterations ? ' · ' + formatNumber(attempts) + ' att' : ''}`
      : '',
  );
  setFooterItem(popupFooter.loopTokens, totalTokens > 0 ? formatTokens(totalTokens) + ' tok' : '');

  let contextText = '';
  if (loopData.context_window && loopData.last_input_tokens) {
    contextText = 'ctx ' + formatTokens(loopData.last_input_tokens) + ' / ' + formatNumber(loopData.context_window);
  } else if (loopData.context_window) {
    contextText = 'ctx ' + formatNumber(loopData.context_window);
  }
  setFooterItem(popupFooter.loopContext, contextText);
  setFooterItem(popupFooter.loopWake, formatLoopFooterWake(loopData));
}

async function probeContentRetention() {
  if (requestDetailProbeInFlight) return requestDetailProbeInFlight;
  requestDetailProbeInFlight = (async () => {
    try {
      const resp = await fetch('/api/requests/_probe');
      requestDetailAvailable = resp.ok && resp.headers.get('X-Request-Detail-Available') === 'true';
    } catch (_) {
      requestDetailAvailable = null;
    } finally {
      requestDetailProbeInFlight = null;
    }
    return requestDetailAvailable;
  })();
  return requestDetailProbeInFlight;
}

function renderRequestDetailUnavailable(message, meta = '') {
  activeRequestJSON = '';
  if (forensics.ids) forensics.ids.innerHTML = '';
  if (forensics.meta) forensics.meta.innerHTML = '';
  if (forensics.requestMeta) {
    forensics.requestMeta.textContent = meta;
    forensics.requestMeta.classList.remove('forensics-card__meta--loading');
  }
  if (forensics.waterfallMeta) forensics.waterfallMeta.textContent = '';
  if (forensics.empty) forensics.empty.textContent = message;
  setForensicsLoaded(false);
  updateForensicsControls();
}

function shouldCooldownRequestDetail(requestID, force = false) {
  if (force) return false;
  return requestDetailCooldown.requestID === requestID && Date.now() < requestDetailCooldown.until;
}

function rememberRequestDetailFailure(requestID, status) {
  const now = Date.now();
  let until = now + 15000;
  if (status === 404) until = now + 30000;
  if (status === 503) until = now + 300000;
  requestDetailCooldown = { requestID, status, until };
}

function clearRequestDetailCooldown(requestID) {
  if (requestDetailCooldown.requestID === requestID) {
    requestDetailCooldown = { requestID: '', status: 0, until: 0 };
  }
}

pollSlider.addEventListener('input', () => {
  pollLabel.textContent = parseInt(pollSlider.value, 10) + 's';
  startLogPoll();
});

// Parse URL params.
const urlParams = new URLSearchParams(window.location.search);
const nodeType = urlParams.get('type') || 'loop';
const nodeId = urlParams.get('id') || '';
const nodeName = urlParams.get('name') || '';

// Set title immediately from URL param, refined later from SSE data.
document.title = 'Thane \u00b7 ' + (nodeName || (nodeType === 'system' ? 'Core' : nodeId.slice(0, 8)));

// Utility functions (formatTokens, formatDuration, formatTime, etc.),
// card builders, and log rendering are in shared.js.

// clearLiveTelemetry, renderAggregates, renderTimeline,
// applyLoopEventToLoop, MAX_ITERATION_HISTORY are in shared.js.

// renderLogRows and buildLogDetail are in shared.js.
function renderLogs(entries) {
  renderLogRows(entries, { logEmpty, logScroll, logBody });
}

// ---------------------------------------------------------------------------
// System Mode
// ---------------------------------------------------------------------------

let systemStartTime = null;

function initSystem() {
  showSystemLogsView();
  $('#system-detail').hidden = false;

  void fetchVersionInfo();
  fetchSystemStatus();
  fetchSystemLogs();
  setInterval(fetchSystemStatus, 10000);
  startLogPoll();
  setInterval(() => {
    updateSystemUptime();
    updatePopupFooter();
  }, 1000);
}

async function fetchSystemStatus() {
  try {
    const resp = await fetch('/api/system');
    if (!resp.ok) return;
    const sys = await resp.json();

    if (sys.uptime) {
      const uptimeMs = parseDuration(sys.uptime);
      systemStartTime = Date.now() - uptimeMs;
    }

    renderSystemInspector(sys, {
      badge: $('#system-status'),
      overview: $('#system-overview'),
      services: $('#system-services'),
      registryMeta: $('#system-registry-meta'),
      registrySummary: $('#system-registry-summary'),
      registryResources: $('#system-registry-resources'),
      registryDeployments: $('#system-registry-deployments'),
    });
    updateSystemUptime();
    updatePopupFooter();

    setConnStatus('ok', 'Connected \u2014 last updated ' + formatTime(new Date()));
  } catch (err) {
    setConnStatus('err', 'Error: ' + err.message);
  }
}

function updateSystemUptime() {
  if (systemStartTime === null) return;
  const ms = Date.now() - systemStartTime;
  const el = $('#system-uptime');
  if (el) el.textContent = formatUptimeLong(ms);
  if (serverStartTime === null) {
    serverStartTime = systemStartTime;
  }
  updatePopupUptime();
}

async function fetchSystemLogs() {
  const level = $('#log-level').value;
  let url = '/api/system/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);
  try {
    const resp = await fetch(url);
    if (!resp.ok) return;
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch system logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Loop Mode
// ---------------------------------------------------------------------------

let loopData = null;
const sleepTimers = new Map();
let iterationHistory = [];
let loopEventSource = null;
let loopEventSourceClosing = false;

function showLoopForensicsView() {
  if (requestSection) requestSection.hidden = false;
  if (logsSection) logsSection.hidden = true;
  if (logControlsRow) logControlsRow.hidden = true;
  if (forensics.openRequest) forensics.openRequest.disabled = true;
}

function showSystemLogsView() {
  if (requestSection) requestSection.hidden = true;
  if (logsSection) logsSection.hidden = false;
  if (logControlsRow) logControlsRow.hidden = false;
}

function getLatestLoopSnapshot() {
  if (iterationHistory.length > 0) return iterationHistory[0];
  if (loopData && Array.isArray(loopData.recent_iterations) && loopData.recent_iterations.length > 0) {
    return loopData.recent_iterations[0];
  }
  return null;
}

function getLatestLoopRequestID() {
  const latest = getLatestLoopSnapshot();
  return (latest && latest.request_id) || '';
}

function setForensicsLoaded(loaded) {
  if (!forensics.empty || !forensics.content || !forensics.waterfall) return;
  forensics.empty.hidden = loaded;
  forensics.content.hidden = !loaded;
  forensics.waterfall.hidden = !loaded;
}

function updateForensicsControls() {
  if (!forensics.follow) return;
  if (followLatestRequest) {
    forensics.follow.textContent = 'Following latest';
    forensics.follow.classList.add('toggle-btn--active');
    forensics.follow.title = 'Following the latest retained request for this loop';
  } else {
    forensics.follow.textContent = 'Resume live follow';
    forensics.follow.classList.remove('toggle-btn--active');
    forensics.follow.title = 'Jump back to the latest retained request';
  }
  if (forensics.openRequest) {
    forensics.openRequest.disabled = !activeRequestID;
  }
}

function renderForensicsCurrent(loop) {
  if (!forensics.current) return;
  const latestRequestID = getLatestLoopRequestID();
  const latestSnap = getLatestLoopSnapshot();
  const latestModel = loop._liveModel || loop._lastModel || latestSnap?.model || '';
  const currentConvID = loop._currentConvID || latestSnap?.conv_id || '';
  const targetRequestID = followLatestRequest ? latestRequestID : (pinnedRequestID || latestRequestID);
  const chips = [
    { text: followLatestRequest ? 'live follow' : 'pinned', focus: true },
    { text: formatSchemaToken(loop.state || 'pending') },
    latestModel ? { text: shortModelName(latestModel) } : null,
    currentConvID ? { text: 'thread ' + shortID(currentConvID) } : null,
    targetRequestID ? { text: 'req ' + shortID(targetRequestID), request: true, full: targetRequestID } : null,
  ].filter(Boolean);

  forensics.current.innerHTML = '';
  for (const item of chips) {
    const el = document.createElement(item.request ? 'button' : 'span');
    el.className = 'forensics-chip'
      + (item.request ? ' forensics-chip--request' : '')
      + (item.focus ? ' forensics-chip--focus' : '');
    el.textContent = item.text;
    if (item.request) {
      el.type = 'button';
      el.title = item.full || '';
      el.addEventListener('click', () => {
        if (!item.full) return;
        followLatestRequest = false;
        pinnedRequestID = item.full;
        syncLoopRequestDetail(true);
      });
    }
    forensics.current.appendChild(el);
  }
}

function setForensicsRequestLoading(requestID) {
  activeRequestID = requestID || '';
  activeRequestJSON = '';
  if (forensics.ids) {
    forensics.ids.innerHTML = '';
    if (requestID) {
      forensics.ids.appendChild(makeIDRow('request_id', requestID));
    }
  }
  if (forensics.meta) {
    forensics.meta.innerHTML = '';
  }
  if (forensics.requestMeta) {
    const metaBits = [];
    if (!followLatestRequest) metaBits.push('Pinned');
    if (requestID) metaBits.push('Loading req ' + shortID(requestID));
    forensics.requestMeta.textContent = metaBits.join(' · ');
    forensics.requestMeta.classList.toggle('forensics-card__meta--loading', !!requestID);
  }
  if (forensics.waterfallMeta) {
    forensics.waterfallMeta.textContent = requestID ? 'Waiting for request detail' : '';
  }
  if (forensics.empty) {
    forensics.empty.textContent = requestID
      ? 'Loading retained request ' + shortID(requestID) + '…'
      : 'Waiting for the first retained request in this loop.';
  }
  setForensicsLoaded(false);
  if (requestSection) {
    requestSection.scrollTo({ top: 0, behavior: 'smooth' });
  }
}

async function fetchRequestDetailIntoForensics(requestID) {
  if (!requestID) {
    activeRequestID = '';
    activeRequestJSON = '';
    clearRequestDetailCooldown(requestID);
    if (forensics.ids) forensics.ids.innerHTML = '';
    if (forensics.meta) forensics.meta.innerHTML = '';
    if (forensics.requestMeta) {
      forensics.requestMeta.textContent = '';
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    if (forensics.waterfallMeta) forensics.waterfallMeta.textContent = '';
    if (forensics.empty) {
      forensics.empty.textContent = loopData && loopData.state === 'processing'
        ? 'The loop is active, but the current request is not retained yet. Live tool and iteration telemetry stays in the sidebar until request detail becomes available.'
        : 'Waiting for the first retained request in this loop.';
    }
    setForensicsLoaded(false);
    updateForensicsControls();
    return;
  }

  if (requestDetailAvailable === false) {
    activeRequestID = requestID;
    renderRequestDetailUnavailable(
      'Request detail is unavailable because content retention is disabled for this runtime. Live loop telemetry in the sidebar still reflects the turn as it runs.',
      'Content retention unavailable',
    );
    return;
  }

  setForensicsRequestLoading(requestID);

  try {
    const resp = await fetch('/api/requests/' + encodeURIComponent(requestID));
    if (!resp.ok) {
      activeRequestID = requestID;
      activeRequestJSON = '';
      if (resp.status === 503) {
        requestDetailAvailable = false;
      }
      rememberRequestDetailFailure(requestID, resp.status);
      if (forensics.ids) forensics.ids.innerHTML = '';
      if (forensics.meta) forensics.meta.innerHTML = '';
      if (forensics.requestMeta) {
        forensics.requestMeta.textContent = resp.status === 404
          ? 'Request detail unavailable'
          : resp.status === 503
            ? 'Content retention unavailable'
            : 'Request detail failed (' + resp.status + ')';
        forensics.requestMeta.classList.remove('forensics-card__meta--loading');
      }
      if (forensics.waterfallMeta) forensics.waterfallMeta.textContent = '';
      if (forensics.empty) {
        forensics.empty.textContent = resp.status === 404
          ? 'This retained request is no longer available. It may have been evicted, or request retention may not include this turn.'
          : resp.status === 503
            ? 'Request detail is unavailable because content retention is disabled for this runtime.'
            : 'Request detail failed to load.';
      }
      setForensicsLoaded(false);
      updateForensicsControls();
      return;
    }

    const detail = await resp.json();
    activeRequestID = requestID;
    activeRequestJSON = JSON.stringify(detail, null, 2);
    clearRequestDetailCooldown(requestID);

    if (forensics.requestMeta) {
      const metaBits = [];
      if (!followLatestRequest) metaBits.push('Pinned');
      metaBits.push('req ' + shortID(requestID));
      if (detail.model) metaBits.push(shortModelName(detail.model));
      if (detail.iteration_count) metaBits.push(formatNumber(detail.iteration_count) + ' iterations');
      forensics.requestMeta.textContent = metaBits.join(' · ');
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    if (forensics.waterfallMeta) {
      const waterfallBits = [];
      if (detail.tools_used && Object.keys(detail.tools_used).length > 0) {
        waterfallBits.push(Object.entries(detail.tools_used).map(([name, count]) => `${name}×${count}`).join(' · '));
      }
      if (detail.exhausted) waterfallBits.push('exhausted');
      forensics.waterfallMeta.textContent = waterfallBits.join(' · ');
    }

    renderRequestDetail(detail, {
      ids: forensics.ids,
      meta: forensics.meta,
      content: forensics.content,
      waterfall: forensics.waterfall,
    });
    setForensicsLoaded(true);
    updateForensicsControls();
  } catch (err) {
    console.warn('Failed to fetch request detail:', err);
    rememberRequestDetailFailure(requestID, 0);
    if (forensics.ids) forensics.ids.innerHTML = '';
    if (forensics.meta) forensics.meta.innerHTML = '';
    if (forensics.requestMeta) {
      forensics.requestMeta.textContent = 'Request detail failed';
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    if (forensics.empty) forensics.empty.textContent = 'Request detail failed to load.';
    setForensicsLoaded(false);
    updateForensicsControls();
  }
}

function syncLoopRequestDetail(force = false) {
  if (!loopData) return;
  const latestRequestID = getLatestLoopRequestID();
  if (followLatestRequest) {
    pinnedRequestID = '';
  }
  const targetRequestID = followLatestRequest ? latestRequestID : (pinnedRequestID || latestRequestID);

  if (forensics.title) {
    forensics.title.textContent = (loopData.name || nodeId.slice(0, 8)) + ' forensics';
  }
  if (forensics.subtitle) {
    const subtitleBits = [
      followLatestRequest ? 'Following the latest retained request for this loop.' : 'Pinned to a specific retained request for comparison.',
      loopData.state === 'processing' ? 'Live tool and iteration telemetry stays in the sidebar while the turn runs.' : '',
    ].filter(Boolean);
    forensics.subtitle.textContent = subtitleBits.join(' ');
  }

  renderForensicsCurrent(loopData);
  updateForensicsControls();

  if (!targetRequestID) {
    if (force || activeRequestID !== '' || (forensics.empty && forensics.empty.hidden)) {
      void fetchRequestDetailIntoForensics('');
    }
    return;
  }

  if (requestDetailAvailable === false) {
    void fetchRequestDetailIntoForensics(targetRequestID);
    return;
  }

  if (shouldCooldownRequestDetail(targetRequestID, force)) return;
  if (!force && activeRequestID === targetRequestID && forensics.empty?.hidden) return;
  void fetchRequestDetailIntoForensics(targetRequestID);
}

function disconnectLoopSSE() {
  loopEventSourceClosing = true;
  if (loopEventSource) {
    loopEventSource.close();
    loopEventSource = null;
  }
}

function initLoop() {
  if (!nodeId) {
    setConnStatus('err', 'Error: no loop ID specified');
    return;
  }

  showLoopForensicsView();
  $('#loop-detail').hidden = false;
  void fetchVersionInfo();
  void probeContentRetention().then(() => syncLoopRequestDetail(true));
  window.onRequestChipClick = (requestID) => {
    if (!requestID) return;
    followLatestRequest = false;
    pinnedRequestID = requestID;
    syncLoopRequestDetail(true);
  };

  connectSSE();
  setInterval(tickLoop, 1000);
}

function connectSSE() {
  disconnectLoopSSE();
  loopEventSourceClosing = false;
  setConnStatus('connecting', 'Connecting...');
  const es = new EventSource('/api/loops/events');
  loopEventSource = es;

  es.addEventListener('snapshot', (e) => {
    const statuses = JSON.parse(e.data);
    const match = statuses.find(s => s.id === nodeId);
    if (match) {
      // Seed iteration history from server-side ring buffer.
      if (match.recent_iterations && match.recent_iterations.length > 0) {
        iterationHistory = match.recent_iterations.slice();
      } else {
        iterationHistory = [];
      }
      // Seed live telemetry for a loop already in processing state.
      if (match.state === 'processing') {
        match._iterStartTs = match.last_wake_at ? new Date(match.last_wake_at).getTime() : Date.now();
        match._liveTools = [];
        match._liveModel = '';
        // Restore LLM context from snapshot so late-connecting clients
        // see enrichment data immediately.
        match._llmContext = match.llm_context || null;
        if (match._llmContext && match._llmContext.model) {
          match._liveModel = match._llmContext.model;
        }
      }
      loopData = match;
      document.title = 'Thane \u00b7 ' + (match.name || nodeId.slice(0, 8)) + ' forensics';
      renderLoopDetail();
    }
    setConnStatus('ok', 'Connected \u2014 ' + formatTime(new Date()));
  });

  es.addEventListener('loop', (e) => {
    const evt = JSON.parse(e.data);
    if (evt.data && evt.data.loop_id === nodeId) {
      applyLoopEvent(evt);
      renderLoopDetail();
    }
  });

  es.addEventListener('error', () => {
    if (loopEventSourceClosing) return;
    setConnStatus('err', 'Disconnected \u2014 reconnecting...');
  });

  es.addEventListener('open', () => {
    if (loopEventSourceClosing) return;
    setConnStatus('ok', 'Connected');
  });
}

window.addEventListener('pagehide', disconnectLoopSSE);
window.addEventListener('beforeunload', disconnectLoopSSE);

function applyLoopEvent(evt) {
  if (!loopData) return;

  const result = applyLoopEventToLoop(evt, {
    loop: loopData,
    loopId: nodeId,
    sleepTimers: sleepTimers,
    history: iterationHistory,
  });

  if (result && result.snapshot) {
    iterationHistory.unshift(result.snapshot);
    if (iterationHistory.length > MAX_ITERATION_HISTORY) {
      iterationHistory.length = MAX_ITERATION_HISTORY;
    }
  }

  // Capability tools change active_tags — refetch to update chips.
  if (result && result.capabilityChanged) {
    refreshActiveTags();
  }
}

// refreshActiveTags fetches current loop status and updates
// the active_tags field so capability chips reflect changes
// from activate_capability / deactivate_capability tool calls.
async function refreshActiveTags() {
  try {
    const resp = await fetch('/api/loops');
    if (!resp.ok) return;
    const statuses = await resp.json();
    const match = statuses.find(s => s.id === nodeId);
    if (match && loopData) {
      loopData.active_tags = match.active_tags || null;
      renderLoopDetail();
    }
  } catch (_) { /* best-effort */ }
}

function renderLoopDetail() {
  if (!loopData) return;

  document.title = 'Thane \u00b7 ' + (loopData.name || nodeId.slice(0, 8)) + ' forensics';
  $('#detail-name').textContent = loopData.name || loopData.id;

  const badge = $('#detail-state');
  const isSup = loopData._supervisor && loopData.state === 'processing';
  badge.textContent = isSup ? 'supervisor' : (loopData.state || 'unknown');
  badge.className = 'state-badge state-badge--' + (isSup ? 'supervisor' : (loopData.state || 'pending'));

  // IDs.
  const idsContainer = $('#detail-ids');
  idsContainer.innerHTML = '';
  if (loopData.id) idsContainer.appendChild(makeIDRow('loop_id', loopData.id));
  if (loopData.parent_id) idsContainer.appendChild(makeIDRow('parent_id', loopData.parent_id));
  if (loopData._currentConvID) idsContainer.appendChild(makeIDRow('conv_id', loopData._currentConvID));

  // Aggregate stats bar.
  renderAggregates(loopData, $('#detail-aggregates'));

  // Iteration timeline.
  renderTimeline(loopData, $('#detail-timeline'), iterationHistory, nodeId, sleepTimers);
  syncLoopRequestDetail();
  updatePopupFooter();

  // Capabilities: show configured tags (muted if inactive) and
  // dynamically activated tags (dashed border if not in config).
  const configTags = (loopData.config && loopData.config.Tags) || [];
  const activeTags = new Set(loopData.active_tags || []);
  const allTags = new Set([...configTags, ...activeTags]);
  const tagsSection = $('#detail-tags');
  const tagsList = $('#detail-tags-list');
  if (allTags.size > 0) {
    tagsSection.hidden = false;
    tagsList.innerHTML = '';
    for (const tag of [...allTags].sort()) {
      const chip = document.createElement('span');
      const inConfig = configTags.includes(tag);
      const isActive = activeTags.has(tag);
      chip.className = 'tag-chip'
        + (isActive && inConfig ? ' tag-chip--active' : '')
        + (!isActive && inConfig ? ' tag-chip--muted' : '')
        + (isActive && !inConfig ? ' tag-chip--dynamic' : '');
      chip.textContent = tag;
      tagsList.appendChild(chip);
    }
  } else {
    tagsSection.hidden = true;
  }
}

// buildLiveCard, buildPastCard, buildConnector, renderAggregates,
// renderTimeline, clearLiveTelemetry, applyLoopEventToLoop,
// shortModelName, buildToolCounts, escapeHTML, truncate, makeIDRow,
// makeIDChip, MAX_ITERATION_HISTORY are all in shared.js.

function tickLoop() {
  if (loopData) {
    renderLoopDetail();
  } else {
    updatePopupFooter();
  }
}

async function fetchLoopLogs() {
  const level = $('#log-level').value;
  let url = '/api/loops/' + encodeURIComponent(nodeId) + '/logs?limit=100';
  if (level) url += '&level=' + encodeURIComponent(level);
  try {
    const resp = await fetch(url);
    if (!resp.ok) return;
    const data = await resp.json();
    renderLogs(data.entries || []);
  } catch (err) {
    console.warn('Failed to fetch loop logs:', err);
  }
}

// ---------------------------------------------------------------------------
// Inspector Sidebar Resize
// ---------------------------------------------------------------------------

(function initResize() {
  const handle = document.getElementById('popup-resize');
  const inspector = document.getElementById('inspector');
  const mainEl = document.querySelector('.popup-main');
  let dragging = false;

  handle.addEventListener('mousedown', (e) => {
    e.preventDefault();
    dragging = true;
    handle.classList.add('popup-resize--active');
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  });

  document.addEventListener('mousemove', (e) => {
    if (!dragging) return;
    e.preventDefault();
    const mainRect = mainEl.getBoundingClientRect();
    const isLeft = document.body.classList.contains('inspector-left');
    let newWidth;
    if (isLeft) {
      // Inspector is on the left: width = mouse X relative to main left.
      newWidth = e.clientX - mainRect.left;
    } else {
      // Inspector is on the right: width = main right edge minus mouse X.
      newWidth = mainRect.right - e.clientX;
    }
    const clamped = Math.max(160, Math.min(newWidth, mainRect.width - 200));
    inspector.style.width = clamped + 'px';
  });

  document.addEventListener('mouseup', () => {
    if (!dragging) return;
    dragging = false;
    handle.classList.remove('popup-resize--active');
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  });
})();

// ---------------------------------------------------------------------------
// Swap Inspector Side
// ---------------------------------------------------------------------------

$('#toggle-side').addEventListener('click', () => {
  document.body.classList.toggle('inspector-left');
});

// ---------------------------------------------------------------------------
// Event Bindings
// ---------------------------------------------------------------------------

$('#log-level').addEventListener('change', () => {
  if (nodeType === 'system') fetchSystemLogs();
  else fetchLoopLogs();
});

forensics.follow?.addEventListener('click', () => {
  if (followLatestRequest) return;
  followLatestRequest = true;
  pinnedRequestID = '';
  syncLoopRequestDetail(true);
});

forensics.openRequest?.addEventListener('click', () => {
  if (!activeRequestID) return;
  openRequestWindow(activeRequestID);
});

forensics.copyJSON?.addEventListener('click', () => {
  if (!activeRequestJSON) return;
  navigator.clipboard.writeText(activeRequestJSON).then(() => {
    forensics.copyJSON.textContent = 'Copied';
    forensics.copyJSON.classList.add('copy-btn--copied');
    setTimeout(() => {
      forensics.copyJSON.textContent = 'JSON';
      forensics.copyJSON.classList.remove('copy-btn--copied');
    }, 1200);
  });
});

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

if (nodeType === 'system') {
  initSystem();
} else {
  initLoop();
}
