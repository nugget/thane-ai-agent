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
const inspector = $('#inspector');
const resizeHandle = $('#popup-resize');
const connDot = $('#conn-dot');
const pollSlider = $('#poll-rate');
const pollLabel = $('#poll-rate-label');
const forensics = {
  title: $('#forensics-title'),
  subtitle: $('#forensics-subtitle'),
  state: $('#forensics-state'),
  current: $('#forensics-current'),
  palette: $('#forensics-palette'),
  follow: $('#forensics-follow'),
  openRequest: $('#forensics-open-request'),
  copyJSON: $('#forensics-copy-json'),
  ids: $('#forensics-ids'),
  meta: $('#forensics-meta'),
  requestMeta: $('#forensics-request-meta'),
  scope: $('#forensics-scope'),
  scopeMeta: $('#forensics-scope-meta'),
  notebook: $('#forensics-notebook'),
  notebookMeta: $('#forensics-notebook-meta'),
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

function openRegistryWindow(registry) {
  const key = String(registry || 'toolbox').trim().toLowerCase();
  const name = key === 'models' ? 'Model Registry' : key === 'scheduled' ? 'Scheduled Loops' : 'Toolbox & Capabilities';
  const w = window.open(
    '/static/registry.html?registry=' + encodeURIComponent(key),
    'registry-' + key,
    'popup=yes,width=1280,height=920',
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
let activeRequestDetail = null;
let pinnedRequestID = '';
let followLatestRequest = true;
const traceDisclosureState = new Map();
let serverStartTime = null;
let requestDetailAvailable = null;
let requestDetailProbeInFlight = null;
let requestDetailCooldown = { requestID: '', status: 0, until: 0 };
let recentlyCompletedRequest = { requestID: '', until: 0 };

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
      const resp = await fetch('/api/request-detail/_probe');
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
  activeRequestDetail = null;
  if (forensics.ids) forensics.ids.innerHTML = '';
  if (forensics.meta) forensics.meta.innerHTML = '';
  if (forensics.requestMeta) {
    forensics.requestMeta.textContent = meta;
    forensics.requestMeta.classList.remove('forensics-card__meta--loading');
  }
  if (forensics.empty) forensics.empty.textContent = message;
  setForensicsLoaded(false);
  if (loopData) renderTraceNotebook(loopData);
  updateForensicsControls();
}

function shouldCooldownRequestDetail(requestID, force = false) {
  if (force) return false;
  return requestDetailCooldown.requestID === requestID && Date.now() < requestDetailCooldown.until;
}

function rememberRequestDetailFailure(requestID, status) {
  const now = Date.now();
  let until = now + 15000;
  if (status === 404) until = now + 2000;
  if (status === 503) until = now + 300000;
  requestDetailCooldown = { requestID, status, until };
}

function clearRequestDetailCooldown(requestID) {
  if (requestDetailCooldown.requestID === requestID) {
    requestDetailCooldown = { requestID: '', status: 0, until: 0 };
  }
}

function noteRecentlyCompletedRequest(requestID) {
  if (!requestID) return;
  recentlyCompletedRequest = {
    requestID,
    until: Date.now() + 5000,
  };
}

function shouldRefreshRecentlyCompletedRequest(requestID) {
  return !!requestID
    && recentlyCompletedRequest.requestID === requestID
    && Date.now() < recentlyCompletedRequest.until;
}

function clearRecentlyCompletedRequest(requestID) {
  if (!requestID || recentlyCompletedRequest.requestID !== requestID) return;
  recentlyCompletedRequest = { requestID: '', until: 0 };
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
      registriesMeta: $('#system-registries-meta'),
      registriesSummary: $('#system-registries-summary'),
      registriesList: $('#system-registries-list'),
      registryActions: {
        toolbox: () => openRegistryWindow('toolbox'),
        models: () => openRegistryWindow('models'),
      },
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
let allLoopStatuses = [];
const loopStatusByID = new Map();
const childLoopIDsByParentID = new Map();
const sleepTimers = new Map();
let iterationHistory = [];
let loopEventSource = null;
let loopEventSourceClosing = false;

function showLoopForensicsView() {
  if (requestSection) requestSection.hidden = false;
  if (logsSection) logsSection.hidden = true;
  if (logControlsRow) logControlsRow.hidden = true;
  if (inspector) inspector.hidden = true;
  if (resizeHandle) resizeHandle.hidden = true;
  document.body.classList.remove('inspector-left');
  if (forensics.openRequest) forensics.openRequest.disabled = true;
}

function showSystemLogsView() {
  if (requestSection) requestSection.hidden = true;
  if (logsSection) logsSection.hidden = false;
  if (logControlsRow) logControlsRow.hidden = false;
  if (inspector) inspector.hidden = false;
  if (resizeHandle) resizeHandle.hidden = false;
}

function getLatestLoopSnapshot() {
  if (iterationHistory.length > 0) return iterationHistory[0];
  if (loopData && Array.isArray(loopData.recent_iterations) && loopData.recent_iterations.length > 0) {
    return loopData.recent_iterations[0];
  }
  return null;
}

function getLatestLoopRequestID() {
  if (loopData && loopData.state === 'processing' && loopData._currentRequestID) {
    return loopData._currentRequestID;
  }
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
  renderForensicsPalette();
}

function stringifyForensicsJSON(value) {
  if (!value) return '';
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return '';
  }
}

function fallbackCopyText(text) {
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    ta.style.top = '0';
    document.body.appendChild(ta);
    ta.select();
    try {
      if (document.execCommand('copy')) {
        resolve();
      } else {
        reject(new Error('copy command failed'));
      }
    } catch (err) {
      reject(err);
    } finally {
      document.body.removeChild(ta);
    }
  });
}

function writeClipboardText(value) {
  const text = String(value || '');
  if (!text) return Promise.reject(new Error('empty clipboard value'));
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text).catch(() => fallbackCopyText(text));
  }
  return fallbackCopyText(text);
}

function copyPaletteValue(btn, value, label) {
  if (!btn || !value) return;
  writeClipboardText(value).then(() => {
    btn.textContent = 'copied';
    btn.classList.add('forensics-palette__button--copied');
    setTimeout(() => {
      btn.textContent = label;
      btn.classList.remove('forensics-palette__button--copied');
    }, 1200);
  }).catch(() => {
    btn.textContent = 'copy failed';
    setTimeout(() => {
      btn.textContent = label;
    }, 1200);
  });
}

function makePaletteButton(label, opts = {}) {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'forensics-palette__button'
    + (opts.active ? ' forensics-palette__button--active' : '')
    + (opts.id ? ' forensics-palette__button--id' : '');
  btn.textContent = label;
  btn.title = opts.title || '';
  btn.disabled = !!opts.disabled;
  if (opts.onClick) {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      opts.onClick(btn);
    });
  }
  return btn;
}

function makeCopyPaletteButton(label, value, title, opts = {}) {
  const titleValue = opts.includeValueInTitle === false ? '' : value;
  return makePaletteButton(label, {
    ...opts,
    disabled: !value || opts.disabled,
    title: value && titleValue ? (title + '\n' + titleValue) : (value ? title : (opts.emptyTitle || title)),
    onClick: (btn) => copyPaletteValue(btn, value, label),
  });
}

function renderForensicsPalette() {
  if (!forensics.palette) return;
  const latestSnap = getLatestLoopSnapshot();
  const latestModel = loopData ? (loopData._liveModel || loopData._lastModel || latestSnap?.model || '') : '';
  const currentConvID = loopData ? (loopData._currentConvID || latestSnap?.conv_id || '') : '';
  const loopJSON = stringifyForensicsJSON(loopData);

  forensics.palette.innerHTML = '';
  forensics.palette.appendChild(makePaletteButton(
    followLatestRequest ? 'live follow' : 'pinned request',
    {
      active: followLatestRequest,
      title: followLatestRequest
        ? 'Automatically follows the newest retained request detail for this loop. Click a request_id chip to pin a specific turn.'
        : 'Pinned to a specific request detail. Click to resume following the newest retained request.',
      onClick: () => {
        if (followLatestRequest) return;
        followLatestRequest = true;
        pinnedRequestID = '';
        syncLoopRequestDetail(true);
      },
    },
  ));

  if (loopData && loopData.id) {
    forensics.palette.appendChild(makeCopyPaletteButton('loop_id', loopData.id, 'Copy loop_id', { id: true }));
  }
  if (loopData && loopData.parent_id) {
    forensics.palette.appendChild(makeCopyPaletteButton('parent_id', loopData.parent_id, 'Copy parent_id', { id: true }));
  }
  if (currentConvID) {
    forensics.palette.appendChild(makeCopyPaletteButton('conv_id', currentConvID, 'Copy conv_id', { id: true }));
  }
  if (latestModel) {
    forensics.palette.appendChild(makeCopyPaletteButton('model', latestModel, 'Copy model name'));
  }
  forensics.palette.appendChild(makeCopyPaletteButton('loop JSON', loopJSON, 'Copy current loop snapshot JSON', {
    emptyTitle: 'Loop snapshot is not loaded yet.',
    includeValueInTitle: false,
  }));
}

function stateClassToken(state) {
  return String(state || 'pending').toLowerCase().replace(/[^a-z0-9_-]+/g, '-');
}

function renderForensicsState(loop) {
  if (!forensics.state) return;
  const state = formatSchemaToken(loop && loop.state ? loop.state : 'pending');
  const stateClass = stateClassToken(loop && loop.state ? loop.state : 'pending');
  forensics.state.textContent = state;
  forensics.state.className = 'forensics-state-badge state-badge state-badge--' + stateClass;
  forensics.state.title = 'Current loop state: ' + state;
}

function renderForensicsCurrent(loop) {
  renderForensicsState(loop);
  if (!forensics.current) return;
  const latestRequestID = getLatestLoopRequestID();
  const latestSnap = getLatestLoopSnapshot();
  const latestModel = loop._liveModel || loop._lastModel || latestSnap?.model || '';
  const currentConvID = loop._currentConvID || latestSnap?.conv_id || '';
  const targetRequestID = followLatestRequest ? latestRequestID : (pinnedRequestID || latestRequestID);
  const nextTurn = loop ? describeNextTurn(loop) : null;
  const chips = [
    { text: followLatestRequest ? 'live follow' : 'pinned', focus: true },
    nextTurn ? { text: nextTurn.title, full: nextTurn.meta } : null,
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
    if (item.full) el.title = item.full;
    if (item.request) {
      el.type = 'button';
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

function getLoopConfig(loop) {
  return (loop && loop.config && typeof loop.config === 'object') ? loop.config : {};
}

function getLoopMetadata(loop) {
  const cfg = getLoopConfig(loop);
  return (cfg.Metadata && typeof cfg.Metadata === 'object') ? cfg.Metadata : {};
}

function getConfigValue(cfg, name, fallback = '') {
  if (!cfg || typeof cfg !== 'object') return fallback;
  if (cfg[name] !== undefined && cfg[name] !== null && cfg[name] !== '') return cfg[name];
  const lower = name.slice(0, 1).toLowerCase() + name.slice(1);
  if (cfg[lower] !== undefined && cfg[lower] !== null && cfg[lower] !== '') return cfg[lower];
  return fallback;
}

function parseMetadataList(raw) {
  if (!raw) return [];
  if (Array.isArray(raw)) return Array.from(new Set(raw.filter(Boolean).map((v) => String(v).trim()).filter(Boolean)));
  const text = String(raw).trim();
  if (!text) return [];
  if (text.startsWith('[')) {
    try {
      const parsed = JSON.parse(text);
      if (Array.isArray(parsed)) return parseMetadataList(parsed);
    } catch (_) {
      // Fall through to delimiter parsing.
    }
  }
  return Array.from(new Set(text.split(/[\n,;]/).map((v) => v.trim()).filter(Boolean)));
}

function isExactEntityPattern(value) {
  return /^[a-z0-9_]+\.[a-z0-9_]+$/i.test(String(value || ''));
}

function formatConfigDuration(raw) {
  if (raw === null || raw === undefined || raw === '') return '';
  if (typeof raw === 'number' && Number.isFinite(raw) && raw > 0) {
    return formatDuration(raw / 1000000);
  }
  if (typeof raw === 'string') {
    const ms = parseDuration(raw);
    return ms > 0 ? formatDuration(ms) : raw;
  }
  return '';
}

function hasToolingDetails(tooling) {
  return !!tooling && (
    tooling.configuredTags.length > 0 ||
    tooling.loadedTags.length > 0 ||
    tooling.loadedCapabilities.length > 0 ||
    tooling.effectiveTools.length > 0 ||
    tooling.excludedTools.length > 0
  );
}

function getLoopCurrentTooling(loop) {
  const cfg = getLoopConfig(loop);
  const liveTooling = normalizeTooling(loop && loop._llmContext && loop._llmContext.tooling, {
    configuredTags: (loop && loop.tooling && loop.tooling.configured_tags) || [],
    loadedTags: loop && loop._llmContext && loop._llmContext.active_tags,
    effectiveTools: loop && loop._llmContext && loop._llmContext.effective_tools,
    excludedTools: (loop && loop.tooling && loop.tooling.excluded_tools) || [],
  });
  const baseTooling = normalizeTooling(loop && loop.tooling, {
    configuredTags: getConfigValue(cfg, 'Tags', []),
    loadedTags: (loop && loop.active_tags) || [],
    excludedTools: getConfigValue(cfg, 'ExcludeTools', []),
  });
  const latest = getLatestLoopSnapshot();
  const latestTooling = normalizeTooling(latest && latest.tooling, {
    configuredTags: baseTooling.configuredTags,
    loadedTags: latest && latest.active_tags,
    effectiveTools: latest && latest.effective_tools,
    excludedTools: baseTooling.excludedTools,
  });

  if (hasToolingDetails(liveTooling)) return liveTooling;
  if (hasToolingDetails(latestTooling)) return latestTooling;
  return baseTooling;
}

function makeScopeSection(title) {
  const section = document.createElement('section');
  section.className = 'forensics-scope__section';
  const heading = document.createElement('div');
  heading.className = 'forensics-scope__title';
  heading.textContent = title;
  section.appendChild(heading);
  return section;
}

function makeScopeChip(text, className = '') {
  if (!text) return null;
  const chip = document.createElement('span');
  chip.className = 'forensics-scope__chip' + (className ? ' ' + className : '');
  chip.textContent = text;
  return chip;
}

function appendScopeChips(section, values, className = '') {
  const valid = (values || []).filter(Boolean);
  if (valid.length === 0) return false;
  const row = document.createElement('div');
  row.className = 'forensics-scope__chips';
  for (const value of valid) {
    const chip = makeScopeChip(value, className);
    if (chip) row.appendChild(chip);
  }
  section.appendChild(row);
  return true;
}

function appendCapabilityChips(section, entries) {
  const valid = (entries || []).filter((entry) => entry && entry.tag);
  if (valid.length === 0) return false;
  const row = document.createElement('div');
  row.className = 'forensics-scope__chips';
  for (const entry of valid) {
    const chip = makeScopeChip(entry.tag, 'forensics-scope__chip--active');
    if (!chip) continue;
    const desc = describeCapabilityEntry(entry);
    if (desc) chip.title = desc;
    row.appendChild(chip);
  }
  section.appendChild(row);
  return true;
}

function renderLoopScope(loop) {
  if (!forensics.scope) return;
  const cfg = getLoopConfig(loop);
  const meta = getLoopMetadata(loop);
  const tooling = getLoopCurrentTooling(loop);
  const operation = getConfigValue(cfg, 'Operation', '') || (loop.event_driven ? 'service' : '');
  const completion = getConfigValue(cfg, 'Completion', '');
  const outputs = Array.isArray(getConfigValue(cfg, 'Outputs', [])) ? getConfigValue(cfg, 'Outputs', []) : [];
  const sleepMin = formatConfigDuration(getConfigValue(cfg, 'SleepMin', ''));
  const sleepMax = formatConfigDuration(getConfigValue(cfg, 'SleepMax', ''));
  const cadence = loop.event_driven
    ? 'event driven'
    : (sleepMin && sleepMax ? (sleepMin === sleepMax ? sleepMin : sleepMin + ' - ' + sleepMax) : 'timed');

  forensics.scope.innerHTML = '';
  if (forensics.scopeMeta) {
    const bits = [
      meta.subsystem || '',
      meta.category || '',
      operation ? formatSchemaToken(operation) : '',
    ].filter(Boolean);
    forensics.scopeMeta.textContent = bits.join(' · ');
  }

  const startedAt = parseTimestamp(loop.started_at);
  const lastWake = parseTimestamp(loop.last_wake_at);
  const totalTokens = (loop.total_input_tokens || 0) + (loop.total_output_tokens || 0);
  const facts = makeIterationFacts([
    { label: 'Operation', value: operation ? formatSchemaToken(operation) : '' },
    { label: 'Completion', value: completion ? formatSchemaToken(completion) : '' },
    { label: 'Cadence', value: cadence },
    { label: 'Started', value: startedAt ? timeAgo(startedAt) : '' },
    { label: 'Last wake', value: lastWake ? timeAgo(lastWake) : '' },
    { label: 'Attempts', value: loop.attempts ? formatNumber(loop.attempts) : '0' },
    { label: 'Total tokens', value: totalTokens > 0 ? formatTokens(totalTokens) : '' },
  ]);
  if (facts) forensics.scope.appendChild(facts);

  const capabilitySection = makeScopeSection('Capabilities');
  const renderedCapabilities = appendCapabilityChips(capabilitySection, tooling.loadedCapabilities);
  const renderedConfigured = appendScopeChips(capabilitySection, tooling.configuredTags, '');
  const renderedTools = tooling.effectiveTools.length > 0
    ? appendScopeChips(capabilitySection, [formatNumber(tooling.effectiveTools.length) + ' tools in scope'])
    : false;
  if (renderedCapabilities || renderedConfigured || renderedTools) {
    forensics.scope.appendChild(capabilitySection);
  }

  const entityGlobs = parseMetadataList(meta.entity_globs || meta.subscribed_entities || meta.entities);
  if (entityGlobs.length > 0 || meta.subscription_event || meta.rate_limit_per_minute) {
    const entitySection = makeScopeSection('Home Assistant Event Filter');
    const exactEntities = entityGlobs.filter(isExactEntityPattern);
    const globPatterns = entityGlobs.filter((value) => !isExactEntityPattern(value));
    appendScopeChips(entitySection, exactEntities, 'forensics-scope__chip--entity');
    appendScopeChips(entitySection, globPatterns.length > 0 ? globPatterns : (entityGlobs.length === 0 ? ['all entities'] : []), '');
    const policyBits = [];
    if (meta.subscription_event) policyBits.push('event ' + meta.subscription_event);
    if (meta.rate_limit_per_minute && meta.rate_limit_per_minute !== '0') {
      policyBits.push(meta.rate_limit_per_minute + '/min per entity');
    } else if (meta.rate_limit_per_minute === '0') {
      policyBits.push('no rate limit');
    }
    appendScopeChips(entitySection, policyBits, '');
    forensics.scope.appendChild(entitySection);
  }

  if (outputs.length > 0) {
    const outputSection = makeScopeSection('Declared Outputs');
    appendScopeChips(outputSection, outputs.map((out) => out.name || out.Name || out.ref || out.Ref).filter(Boolean));
    forensics.scope.appendChild(outputSection);
  }
}

function getActiveRequestDetail() {
  if (activeRequestDetail && activeRequestDetail.request_id === activeRequestID) {
    return activeRequestDetail;
  }
  if (!activeRequestJSON) return null;
  try {
    const detail = JSON.parse(activeRequestJSON);
    return detail && detail.request_id === activeRequestID ? detail : null;
  } catch (_) {
    return null;
  }
}

function formatTraceJSON(value) {
  if (value === null || value === undefined || value === '') return '';
  if (typeof value === 'string') {
    const text = value.trim();
    if (!text) return '';
    if (text.startsWith('{') || text.startsWith('[')) {
      try {
        return JSON.stringify(JSON.parse(text), null, 2);
      } catch (_) {
        return value;
      }
    }
    return value;
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return String(value);
  }
}

function summarizeTracePayload(value, max = 120) {
  const text = formatTraceJSON(value).replace(/\s+/g, ' ').trim();
  return text ? truncate(text, max) : '';
}

function hashTraceDisclosureText(text) {
  let hash = 5381;
  for (let i = 0; i < text.length; i++) {
    hash = ((hash << 5) + hash) ^ text.charCodeAt(i);
  }
  return (hash >>> 0).toString(36);
}

function traceDisclosureKey(label, text, key = '') {
  return key || (label + ':' + hashTraceDisclosureText(text.slice(0, 2000)));
}

function appendTraceDisclosure(parent, label, value, key = '', opts = {}) {
  if (key && typeof key === 'object') {
    opts = key;
    key = '';
  }
  const text = formatTraceJSON(value);
  if (!text) return false;
  const stateKey = traceDisclosureKey(label, text, key);
  const details = document.createElement('details');
  details.className = 'trace-disclosure';
  details.dataset.traceKey = stateKey;
  details.open = traceDisclosureState.get(stateKey) === true;
  details.addEventListener('toggle', () => {
    traceDisclosureState.set(stateKey, details.open);
  });
  const summary = document.createElement('summary');
  const labelEl = document.createElement('span');
  labelEl.textContent = label;
  summary.appendChild(labelEl);
  if (opts.copy) {
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'trace-disclosure__copy';
    copy.textContent = 'copy';
    copy.title = 'Copy ' + label;
    copy.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      writeClipboardText(text).then(() => {
        copy.textContent = 'copied';
        copy.classList.add('trace-disclosure__copy--copied');
        setTimeout(() => {
          copy.textContent = 'copy';
          copy.classList.remove('trace-disclosure__copy--copied');
        }, 1200);
      }).catch(() => {
        copy.textContent = 'failed';
        setTimeout(() => {
          copy.textContent = 'copy';
        }, 1200);
      });
    });
    summary.appendChild(copy);
  }
  const pre = document.createElement('pre');
  pre.textContent = text;
  details.appendChild(summary);
  details.appendChild(pre);
  parent.appendChild(details);
  return true;
}

function makeTraceChip(text, className = '') {
  return makeTurnChip(text, className);
}

function buildTraceEvent(probe, title, meta, opts = {}) {
  const event = document.createElement('article');
  event.className = 'trace-event' + (opts.kind ? ' trace-event--' + opts.kind : '');
  if (opts.title) event.title = opts.title;

  const header = document.createElement('div');
  header.className = 'trace-event__header';
  const probeEl = document.createElement('span');
  probeEl.className = 'trace-event__probe';
  probeEl.textContent = probe;
  const titleEl = document.createElement('div');
  titleEl.className = 'trace-event__title';
  titleEl.textContent = title;
  header.appendChild(probeEl);
  header.appendChild(titleEl);
  event.appendChild(header);

  if (meta) {
    const metaEl = document.createElement('div');
    metaEl.className = 'trace-event__meta';
    metaEl.textContent = meta;
    event.appendChild(metaEl);
  }

  const chips = (opts.chips || []).filter(Boolean);
  if (chips.length > 0) {
    const row = document.createElement('div');
    row.className = 'trace-event__chips';
    for (const chip of chips) row.appendChild(chip);
    event.appendChild(row);
  }

  if (opts.details && opts.details.length > 0) {
    const body = document.createElement('div');
    body.className = 'trace-event__details';
    for (const detail of opts.details) {
      appendTraceDisclosure(body, detail.label, detail.value, detail.key || '', { copy: detail.copy });
    }
    if (body.children.length > 0) event.appendChild(body);
  }

  const actions = (opts.actions || []).filter(Boolean);
  if (actions.length > 0) {
    const row = document.createElement('div');
    row.className = 'trace-event__actions';
    for (const action of actions) row.appendChild(action);
    event.appendChild(row);
  }

  return event;
}

function makeTraceAction(label, title, onClick) {
  const btn = document.createElement('button');
  btn.className = 'btn btn--sm';
  btn.type = 'button';
  btn.textContent = label;
  if (title) btn.title = title;
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    onClick();
  });
  return btn;
}

function makeTraceCopyAction(label, value, title, emptyTitle = '') {
  const btn = document.createElement('button');
  btn.className = 'btn btn--sm';
  btn.type = 'button';
  btn.textContent = label;
  btn.disabled = !value;
  btn.title = value ? title : (emptyTitle || title);
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    if (!value) return;
    writeClipboardText(value).then(() => {
      btn.textContent = 'copied';
      btn.classList.add('copy-btn--copied');
      setTimeout(() => {
        btn.textContent = label;
        btn.classList.remove('copy-btn--copied');
      }, 1200);
    }).catch(() => {
      btn.textContent = 'copy failed';
      setTimeout(() => {
        btn.textContent = label;
      }, 1200);
    });
  });
  return btn;
}

function formatTraceTime(raw) {
  const date = parseTimestamp(raw);
  return date ? timeAgo(date) : '';
}

function getTraceChildLoops(loop) {
  if (!loop) return [];
  const childIDs = childLoopIDsByParentID.get(loop.id);
  if (!childIDs) return [];
  return Array.from(childIDs)
    .map((id) => loopStatusByID.get(id))
    .filter(Boolean)
    .sort((a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id)));
}

function indexLoopChild(parentID, childID) {
  if (!parentID || !childID) return;
  let children = childLoopIDsByParentID.get(parentID);
  if (!children) {
    children = new Set();
    childLoopIDsByParentID.set(parentID, children);
  }
  children.add(childID);
}

function unindexLoopChild(parentID, childID) {
  if (!parentID || !childID) return;
  const children = childLoopIDsByParentID.get(parentID);
  if (!children) return;
  children.delete(childID);
  if (children.size === 0) childLoopIDsByParentID.delete(parentID);
}

function reindexLoopParent(status, previousParentID) {
  const nextParentID = status && status.parent_id ? status.parent_id : '';
  if (previousParentID === nextParentID) return;
  unindexLoopChild(previousParentID, status.id);
  indexLoopChild(nextParentID, status.id);
}

function replaceLoopStatusSnapshot(statuses) {
  allLoopStatuses = [];
  loopStatusByID.clear();
  childLoopIDsByParentID.clear();
  for (const entry of statuses || []) {
    if (!entry || !entry.id) continue;
    const status = { ...entry };
    allLoopStatuses.push(status);
    loopStatusByID.set(status.id, status);
    indexLoopChild(status.parent_id || '', status.id);
  }
}

function upsertLoopStatusFromEvent(evt) {
  const d = evt.data || {};
  const id = d.loop_id;
  if (!id) return null;
  let status = loopStatusByID.get(id);
  const previousParentID = status && status.parent_id ? status.parent_id : '';
  if (!status) {
    status = {
      id,
      name: d.loop_name || id,
      state: 'pending',
      parent_id: d.parent_id || '',
      iterations: 0,
      attempts: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
    };
    allLoopStatuses.push(status);
    loopStatusByID.set(id, status);
  }
  if (d.loop_name) status.name = d.loop_name;
  if (d.parent_id !== undefined) status.parent_id = d.parent_id || '';
  if (evt.kind === 'loop_started') {
    status.state = status.state === 'pending' ? 'sleeping' : status.state;
    status.started_at = evt.ts;
  } else if (evt.kind === 'loop_state_change') {
    status.state = d.to || status.state;
  } else if (evt.kind === 'loop_iteration_start') {
    status.state = 'processing';
    status.last_wake_at = evt.ts;
    status.attempts = d.attempt || status.attempts || 0;
  } else if (evt.kind === 'loop_iteration_complete') {
    status.iterations = (status.iterations || 0) + 1;
    status.state = status.event_driven ? 'waiting' : status.state;
    status.last_input_tokens = d.input_tokens || 0;
    status.last_output_tokens = d.output_tokens || 0;
    status.total_input_tokens = (status.total_input_tokens || 0) + (d.input_tokens || 0);
    status.total_output_tokens = (status.total_output_tokens || 0) + (d.output_tokens || 0);
    status.last_model = d.model || status.last_model || '';
  } else if (evt.kind === 'loop_sleep_start') {
    status.state = 'sleeping';
  } else if (evt.kind === 'loop_wait_start') {
    status.state = 'waiting';
    status.event_driven = true;
  } else if (evt.kind === 'loop_error') {
    status.state = 'error';
    status.last_error = d.error || status.last_error || '';
  } else if (evt.kind === 'loop_stopped') {
    status.state = 'stopped';
    status.iterations = d.iterations || status.iterations || 0;
    status.attempts = d.attempts || status.attempts || 0;
  }
  reindexLoopParent(status, previousParentID);
  return status;
}

function openLoopForensics(loopID, loopName, target = '_self') {
  if (!loopID) return;
  const url = '/static/detail.html?type=loop&id=' + encodeURIComponent(loopID)
    + '&name=' + encodeURIComponent(loopName || loopID);
  if (target === '_blank') {
    window.open(url, 'loop-' + loopID, 'popup=yes,width=1280,height=920');
    return;
  }
  window.location.href = url;
}

function buildTraceToolEvent(tool) {
  const name = tool.toolName || tool.tool_name || tool.tool || 'tool';
  const isDelegate = isDelegateTool(name);
  const status = tool.error ? 'error' : (tool.status || (tool.result ? 'done' : 'recorded'));
  const callID = tool.tool_call_id || tool.toolCallID || tool.call_id || tool.callID || '';
  const toolKey = callID || tool.liveKey || tool.liveIndex || [name, tool.iterationIndex || tool.iteration_index || 0, status].join(':');
  const startedAt = tool.started_at || tool.startedAt || '';
  const completedAt = tool.completed_at || tool.completedAt || tool.created_at || '';
  const duration = Number.isFinite(tool.duration_ms) ? tool.duration_ms : null;
  const parsed = isDelegate ? parseDelegateArgs(tool.arguments || tool.args) : {};
  const title = isDelegate
    ? name + ': ' + truncate(parsed.task || 'spawned work', 96)
    : name;
  const metaBits = [];
  if (status === 'running') metaBits.push('in flight');
  if (tool.iterationIndex || tool.iteration_index) metaBits.push('iteration #' + (tool.iterationIndex || tool.iteration_index));
  if (startedAt) metaBits.push('started ' + formatTraceTime(startedAt));
  if (duration !== null && duration >= 0) metaBits.push(formatDuration(duration));
  if (!startedAt && completedAt) metaBits.push('recorded ' + formatTraceTime(completedAt));
  const argSummary = summarizeTracePayload(tool.arguments || tool.args, 100);
  if (argSummary && !isDelegate) metaBits.push(argSummary);
  if (tool.error) metaBits.push(tool.error);
  const chips = [
    makeTraceChip(status, status === 'running' ? 'forensics-scope__chip--active' : ''),
    isDelegate ? makeTraceChip(delegateToolLabel(name), 'forensics-scope__chip--active') : null,
    callID ? makeTraceChip('call ' + shortID(callID)) : null,
  ];
  const details = [
    { label: 'Arguments', value: tool.arguments || tool.args, key: 'tool:' + toolKey + ':args', copy: true },
    { label: tool.error ? 'Error' : 'Result', value: tool.error || tool.result, key: 'tool:' + toolKey + ':result', copy: true },
    { label: 'Raw tool record', value: tool, key: 'tool:' + toolKey + ':raw', copy: true },
  ];
  return buildTraceEvent(isDelegate ? 'loop:spawn' : 'tool:' + status, title, metaBits.join(' · '), {
    kind: tool.error ? 'error' : (isDelegate ? 'branch' : 'tool'),
    chips,
    details,
  });
}

function getNotebookRequestDetail(requestID) {
  const detail = getActiveRequestDetail();
  return detail && detail.request_id === requestID ? detail : null;
}

function makeNotebookCell(opts) {
  const cell = document.createElement('article');
  cell.className = 'trace-cell trace-cell--' + (opts.kind || 'past');

  const rail = document.createElement('div');
  rail.className = 'trace-cell__rail';
  const dot = document.createElement('span');
  dot.className = 'trace-cell__dot';
  const ordinal = document.createElement('span');
  ordinal.className = 'trace-cell__ordinal';
  ordinal.textContent = opts.ordinal || '';
  const time = document.createElement('span');
  time.className = 'trace-cell__time';
  time.textContent = opts.time || '';
  rail.appendChild(dot);
  rail.appendChild(ordinal);
  rail.appendChild(time);
  cell.appendChild(rail);

  const body = document.createElement('div');
  body.className = 'trace-cell__body';

  const header = document.createElement('header');
  header.className = 'trace-cell__header';
  if (opts.eyebrow) {
    const eyebrow = document.createElement('div');
    eyebrow.className = 'trace-cell__eyebrow';
    eyebrow.textContent = opts.eyebrow;
    header.appendChild(eyebrow);
  }

  const heading = document.createElement('div');
  heading.className = 'trace-cell__heading';
  const title = document.createElement('h3');
  title.className = 'trace-cell__title';
  title.textContent = opts.title || '';
  if (opts.fullTitle) title.title = opts.fullTitle;
  heading.appendChild(title);
  if (opts.state) {
    const state = document.createElement('span');
    state.className = 'trace-cell__state trace-cell__state--' + (opts.stateKind || opts.kind || 'past');
    state.textContent = opts.state;
    heading.appendChild(state);
  }
  header.appendChild(heading);

  if (opts.meta) {
    const meta = document.createElement('div');
    meta.className = 'trace-cell__meta';
    meta.textContent = opts.meta;
    header.appendChild(meta);
  }

  const chips = (opts.chips || []).filter(Boolean);
  if (chips.length > 0) {
    const row = document.createElement('div');
    row.className = 'trace-cell__chips';
    for (const chip of chips) row.appendChild(chip);
    header.appendChild(row);
  }

  const actions = (opts.actions || []).filter(Boolean);
  if (actions.length > 0) {
    const row = document.createElement('div');
    row.className = 'trace-cell__actions';
    for (const action of actions) row.appendChild(action);
    header.appendChild(row);
  }

  let sectionTarget = body;
  if (opts.collapsible) {
    const foldKey = 'cell:' + (opts.foldKey || opts.title || opts.ordinal || '');
    const fold = document.createElement('details');
    fold.className = 'trace-cell__fold';
    fold.open = traceDisclosureState.has(foldKey)
      ? traceDisclosureState.get(foldKey) === true
      : opts.defaultOpen !== false;
    fold.addEventListener('toggle', () => {
      traceDisclosureState.set(foldKey, fold.open);
    });

    const summary = document.createElement('summary');
    summary.appendChild(header);
    fold.appendChild(summary);

    sectionTarget = document.createElement('div');
    sectionTarget.className = 'trace-cell__fold-body';
    fold.appendChild(sectionTarget);
    body.appendChild(fold);
  } else {
    body.appendChild(header);
  }
  cell.appendChild(body);
  return { cell, body: sectionTarget };
}

function makeNotebookSection(title, content) {
  if (!content) return null;
  const section = document.createElement('section');
  section.className = 'trace-cell__section';
  if (title) {
    const label = document.createElement('div');
    label.className = 'trace-cell__section-title';
    label.textContent = title;
    section.appendChild(label);
  }
  section.appendChild(content);
  return section;
}

function makeNotebookEventList(events) {
  const valid = (events || []).filter(Boolean);
  if (valid.length === 0) return null;
  const list = document.createElement('div');
  list.className = 'trace-cell__events';
  for (const event of valid) list.appendChild(event);
  return list;
}

function appendNotebookSection(body, title, content) {
  const section = makeNotebookSection(title, content);
  if (section) body.appendChild(section);
}

function buildToolSummaryEvents(toolsUsed) {
  if (!toolsUsed) return [];
  return Object.entries(toolsUsed).map(([name, count]) => buildTraceEvent('tool:summary', name, formatNumber(count) + ' call' + (count === 1 ? '' : 's') + ' observed in this turn', {
    kind: isDelegateTool(name) ? 'branch' : 'tool',
    chips: [makeTraceChip('summary')],
  }));
}

function appendNotebookToolSection(body, detail, fallbackTools, liveTools = []) {
  const events = [];
  for (const entry of liveTools || []) {
    events.push(buildTraceToolEvent({
      toolName: entry.tool,
      status: entry.status || 'running',
      arguments: entry.args,
      result: entry.result,
      error: entry.error,
      tool_call_id: entry.tool_call_id || entry.toolCallID,
      liveKey: entry.liveKey,
      liveIndex: entry.liveIndex,
      started_at: entry.started_at,
      completed_at: entry.completed_at,
      duration_ms: entry.duration_ms,
    }));
  }
  if (events.length === 0 && detail && Array.isArray(detail.tool_calls) && detail.tool_calls.length > 0) {
    for (const toolCall of detail.tool_calls) {
      events.push(buildTraceToolEvent(toolCall));
    }
  }
  if (events.length === 0) {
    events.push(...buildToolSummaryEvents(fallbackTools));
  }
  appendNotebookSection(body, 'Tool Calls', makeNotebookEventList(events));
}

function getToolDefinitionSnapshots(detail) {
  if (!detail) return [];
  if (Array.isArray(detail.tool_definitions)) return detail.tool_definitions;
  return [];
}

function countToolDefinitions(snapshots) {
  return (snapshots || []).reduce((total, snap) => total + (snap && Array.isArray(snap.tools) ? snap.tools.length : 0), 0);
}

function appendNotebookToolSurface(body, requestID, detail) {
  const snapshots = getToolDefinitionSnapshots(detail);
  if (snapshots.length === 0) return;

  const surface = document.createElement('div');
  surface.className = 'trace-cell__evidence';
  for (const snap of snapshots) {
    if (!snap) continue;
    const tools = Array.isArray(snap.tools) ? snap.tools : [];
    if (tools.length === 0) continue;
    const iter = Number.isFinite(snap.iteration_index) ? snap.iteration_index : 0;
    const label = 'Iteration ' + iter + ' Registry.List() (' + formatNumber(tools.length) + ' tools)';
    appendTraceDisclosure(surface, label, tools, 'request:' + requestID + ':tooldefs:' + iter, { copy: true });
  }
  appendNotebookSection(body, 'Model Tool Surface', surface.childElementCount > 0 ? surface : null);
}

function buildChildLoopEvent(child) {
  const tokenTotal = (child.total_input_tokens || 0) + (child.total_output_tokens || 0);
  const metaBits = [
    formatSchemaToken(child.state || 'pending'),
    child.iterations ? formatNumber(child.iterations) + ' turns' : '',
    child.last_wake_at ? 'wake ' + formatTraceTime(child.last_wake_at) : '',
    tokenTotal > 0 ? formatTokens(tokenTotal) + ' tokens' : '',
  ].filter(Boolean);
  const actions = [
    makeTraceAction('Inspect', 'Navigate to child loop forensics', () => openLoopForensics(child.id, child.name, '_self')),
    makeTraceAction('Pop out', 'Open child loop forensics in a new window', () => openLoopForensics(child.id, child.name, '_blank')),
  ];
  return buildTraceEvent('loop:child', child.name || shortID(child.id), metaBits.join(' · '), {
    kind: child.state === 'error' ? 'error' : 'branch',
    title: 'loop_id ' + child.id,
    chips: [
      makeTraceChip(formatSchemaToken(child.state || 'pending'), child.state === 'processing' ? 'forensics-scope__chip--active' : ''),
      child.handler_only ? makeTraceChip('handler') : null,
    ],
    actions,
  });
}

function buildDelegateSummaryEvent(dc) {
  const metaBits = [
    dc.profile || dc.mode || '',
    dc.status || '',
    dc.guidance || '',
    dc.error || '',
  ].filter(Boolean);
  return buildTraceEvent('loop:spawn', truncate(dc.task || 'spawned work', 96), metaBits.join(' · '), {
    kind: dc.status === 'error' ? 'error' : 'branch',
    chips: [
      dc.mode ? makeTraceChip(dc.mode, 'forensics-scope__chip--active') : null,
      dc.tags && dc.tags.length > 0 ? makeTraceChip(formatNumber(dc.tags.length) + ' tags') : null,
    ],
  });
}

function appendNotebookSpawnedWork(body, loop, snap, includeLinkedChildren) {
  const events = [];
  if (snap && Array.isArray(snap.delegate_calls)) {
    for (const dc of snap.delegate_calls) events.push(buildDelegateSummaryEvent(dc));
  }
  if (includeLinkedChildren) {
    for (const child of getTraceChildLoops(loop)) events.push(buildChildLoopEvent(child));
  }
  appendNotebookSection(body, 'Spawned Work', makeNotebookEventList(events));
}

function makeNotebookStatus(text) {
  if (!text) return null;
  const status = document.createElement('div');
  status.className = 'trace-cell__status';
  status.textContent = text;
  return status;
}

function appendNotebookEvidence(body, detail) {
  if (!detail) return;
  const evidence = document.createElement('div');
  evidence.className = 'trace-cell__evidence';
  appendTraceDisclosure(evidence, 'Full retained request JSON', detail, 'request:' + detail.request_id + ':detail', { copy: true });
  appendNotebookSection(body, 'Request Detail JSON', evidence.childElementCount > 0 ? evidence : null);
}

function loadNotebookRequestDetail(requestID) {
  if (!requestID) return;
  followLatestRequest = false;
  pinnedRequestID = requestID;
  syncLoopRequestDetail(true);
}

function detailHasModelExchange(detail) {
  return !!(detail && (
    detail.system_prompt
    || (Array.isArray(detail.messages) && detail.messages.length > 0)
    || detail.assistant_content
    || detail.exhaust_reason
  ));
}

function appendModelExchangeDisclosure(parent, label, value, requestID) {
  if (value === null || value === undefined || value === '') return false;
  const token = label.toLowerCase().replace(/[^a-z0-9]+/g, '_');
  return appendTraceDisclosure(parent, label, value, 'request:' + requestID + ':exchange:' + token, { copy: true });
}

function makeModelExchangeResponse(detail) {
  if (!detail) return '';
  if (detail.assistant_content) return detail.assistant_content;
  if (detail.exhaust_reason) return 'No assistant content retained. Exhausted: ' + detail.exhaust_reason;
  return '';
}

function makeLoadModelExchangePrompt(requestID) {
  const wrap = document.createElement('div');
  wrap.className = 'trace-cell__raw-prompt';

  let statusText = 'Retained request detail is available on demand for this turn.';
  if (activeRequestID === requestID) {
    statusText = 'Loading retained request detail. This will expose the system prompt, Messages[] payload, and model response when available.';
    if (requestDetailCooldown.requestID === requestID && Date.now() < requestDetailCooldown.until) {
      statusText = requestDetailCooldown.status === 404
        ? 'Retained request detail is not available for this turn.'
        : requestDetailCooldown.status === 503
          ? 'Request detail retention is disabled for this runtime.'
          : 'Request detail failed to load for this turn.';
    }
  }
  const status = makeNotebookStatus(statusText);
  if (status) wrap.appendChild(status);

  const actions = document.createElement('div');
  actions.className = 'trace-cell__raw-actions';
  actions.appendChild(makeTraceAction(
    activeRequestID === requestID ? 'Reload raw exchange' : 'Load raw exchange',
    'Load system prompt, Messages[] payload, and model response for this request',
    () => loadNotebookRequestDetail(requestID),
  ));
  actions.appendChild(makeTraceAction(
    'Open request window',
    'Open retained request detail in a separate window',
    () => openRequestWindow(requestID),
  ));
  wrap.appendChild(actions);
  return wrap;
}

function appendNotebookModelExchange(body, requestID, detail) {
  if (!requestID) return;
  if (!detail || !detailHasModelExchange(detail)) {
    appendNotebookSection(body, 'Raw Model Exchange', makeLoadModelExchangePrompt(requestID));
    return;
  }

  const exchange = document.createElement('div');
  exchange.className = 'trace-cell__evidence';
  appendModelExchangeDisclosure(exchange, 'System Prompt', detail.system_prompt, requestID);
  appendModelExchangeDisclosure(exchange, 'Messages[] Payload', Array.isArray(detail.messages) ? detail.messages : null, requestID);
  appendModelExchangeDisclosure(exchange, 'Model Response', makeModelExchangeResponse(detail), requestID);
  if (exchange.childElementCount === 0) {
    exchange.appendChild(makeNotebookStatus('Request detail loaded, but no raw prompt, Messages[], or model response was retained for this request.'));
  }
  appendNotebookSection(body, 'Raw Model Exchange', exchange);
}

function normalizeRequestTitleText(value) {
  if (value === null || value === undefined || value === '') return '';
  const text = typeof value === 'string' ? value : formatTraceJSON(value);
  return stripRequestTitlePreamble(text.replace(/\s+/g, ' ').trim());
}

function stripRequestTitlePreamble(text) {
  if (!text) return '';
  const signalWithTimestamp = text.match(/^Signal message from .*? \[ts:[^\]]+\]:\s*/);
  if (signalWithTimestamp) return text.slice(signalWithTimestamp[0].length).trim();
  const signalWithoutTimestamp = text.match(/^Signal message from [^:]+:\s*/);
  if (signalWithoutTimestamp) return text.slice(signalWithoutTimestamp[0].length).trim();
  return text;
}

function latestUserMessageText(messages) {
  if (!Array.isArray(messages)) return '';
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i] || {};
    if (msg.role === 'user') {
      const text = normalizeRequestTitleText(msg.content);
      if (text) return text;
    }
  }
  return '';
}

function turnRequestTitle(snap, detail) {
  const text = normalizeRequestTitleText(
    snap.request_text
      || snap.user_content
      || (snap.summary && (snap.summary.request_text || snap.summary.user_content))
      || (detail && detail.user_content)
      || latestUserMessageText(detail && detail.messages),
  );
  return text ? truncate(text, 120) : '';
}

function buildLiveNotebookCell(loop) {
  const ctx = loop._llmContext || {};
  const requestID = loop._currentRequestID || ctx.request_id || '';
  const detail = getNotebookRequestDetail(requestID);
  const tooling = normalizeTooling(ctx.tooling, {
    configuredTags: loop.tooling && loop.tooling.configured_tags,
    loadedTags: Array.isArray(ctx.active_tags) && ctx.active_tags.length > 0 ? ctx.active_tags : loop.active_tags,
    effectiveTools: ctx.effective_tools,
    excludedTools: loop.tooling && loop.tooling.excluded_tools,
  });
  const elapsed = formatDuration(getLoopIterationElapsedMs(loop));
  const metaBits = [];
  if (elapsed) metaBits.push('running ' + elapsed);
  if (loop._liveModel || ctx.model) metaBits.push('sampling on ' + shortModelName(loop._liveModel || ctx.model));
  if (ctx.est_tokens) metaBits.push('~' + formatTokens(ctx.est_tokens) + ' context tokens');
  if (ctx.tools) metaBits.push(formatNumber(ctx.tools) + ' tools offered');

  const { cell, body } = makeNotebookCell({
    kind: 'live',
    ordinal: 'now',
    time: elapsed,
    eyebrow: loop._supervisor ? 'Live supervisor iteration' : 'Live iteration',
    title: 'Active model turn',
    state: 'live',
    stateKind: 'live',
    meta: metaBits.join(' · ') || 'The loop is currently executing.',
    chips: [
      makeRequestTurnChip(requestID),
      loop._liveModel ? makeTraceChip(shortModelName(loop._liveModel), 'forensics-scope__chip--active') : null,
      tooling.loadedCapabilities.length > 0 ? makeTraceChip(formatNumber(tooling.loadedCapabilities.length) + ' capabilities') : null,
      tooling.effectiveTools.length > 0 ? makeTraceChip(formatNumber(tooling.effectiveTools.length) + ' tools') : null,
    ],
    actions: [
      makeTraceCopyAction('copy request_id', requestID, 'Copy this turn request_id'),
      makeTraceCopyAction('copy request JSON', detail ? stringifyForensicsJSON(detail) : '', 'Copy retained request detail JSON', requestID ? 'Request detail is still loading or unavailable.' : 'No request_id for this turn.'),
    ],
  });

  const scope = makeIterationScopePanel([
    tooling.loadedCapabilities.length > 0
      ? { label: 'Loaded capabilities', capabilities: tooling.loadedCapabilities, className: 'tag-chip tag-chip--active' }
      : null,
    tooling.effectiveTools.length > 0
      ? { label: 'Tool surface', values: tooling.effectiveTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
  ]);
  appendNotebookSection(body, 'Scope', scope);
  appendNotebookToolSection(body, detail, null, loop._liveTools || []);
  appendNotebookToolSurface(body, requestID, detail);
  appendNotebookSpawnedWork(body, loop, null, true);
  appendNotebookModelExchange(body, requestID, detail);
  appendNotebookEvidence(body, detail);
  return cell;
}

function buildPastNotebookCell(loop, snap, isTop) {
  const detail = getNotebookRequestDetail(snap.request_id);
  const requestTitle = turnRequestTitle(snap, detail);
  const fallbackTitle = snap.error
    ? 'Turn ended with an issue'
    : snap.supervisor
      ? 'Supervisor turn'
      : snap.request_id
        ? 'Request ' + shortID(snap.request_id)
        : 'Loop turn';
  const toolCount = countToolCalls(snap.tools_used);
  const tooling = normalizeTooling(snap.tooling, {
    loadedTags: Array.isArray(snap.active_tags) ? snap.active_tags : [],
    effectiveTools: Array.isArray(snap.effective_tools) ? snap.effective_tools : [],
    toolsUsed: snap.tools_used || null,
  });
  const metaBits = [];
  if (snap.completed_at) metaBits.push(formatTraceTime(snap.completed_at));
  if (snap.elapsed_ms) metaBits.push(formatDuration(snap.elapsed_ms));
  if (snap.model) metaBits.push(shortModelName(snap.model));
  if (snap.input_tokens || snap.output_tokens) {
    metaBits.push(formatTokens(snap.input_tokens || 0) + ' in / ' + formatTokens(snap.output_tokens || 0) + ' out');
  }
  if (toolCount > 0) metaBits.push(formatNumber(toolCount) + ' tool call' + (toolCount === 1 ? '' : 's'));
  if (snap.error) metaBits.push(snap.error);

  const { cell, body } = makeNotebookCell({
    kind: snap.error ? 'issue' : (isTop ? 'recent' : 'past'),
    ordinal: snap.number ? '#' + snap.number : 'turn',
    time: snap.completed_at ? formatTimeShort(new Date(snap.completed_at)) : '',
    eyebrow: isTop ? 'Most recent completed iteration' : 'Previous iteration',
    title: requestTitle || fallbackTitle,
    fullTitle: requestTitle || '',
    state: snap.error ? 'issue' : 'complete',
    stateKind: snap.error ? 'issue' : 'complete',
    meta: metaBits.join(' · '),
    chips: [
      makeRequestTurnChip(snap.request_id),
      snap.model ? makeTraceChip(shortModelName(snap.model), snap.supervisor ? 'forensics-scope__chip--active' : '') : null,
      snap.supervisor ? makeTraceChip('supervisor') : null,
      tooling.loadedCapabilities.length > 0 ? makeTraceChip(formatNumber(tooling.loadedCapabilities.length) + ' capabilities') : null,
    ],
    actions: [
      makeTraceCopyAction('copy request_id', snap.request_id, 'Copy this turn request_id'),
      makeTraceCopyAction('copy request JSON', detail ? stringifyForensicsJSON(detail) : '', 'Copy retained request detail JSON', snap.request_id ? 'Click the request chip to load this request detail first.' : 'No request_id for this turn.'),
    ],
    collapsible: true,
    defaultOpen: isTop,
    foldKey: snap.request_id || snap.conv_id || ('turn-' + (snap.number || 'unknown')),
  });

  const scope = makeIterationScopePanel([
    tooling.loadedCapabilities.length > 0
      ? { label: 'Loaded capabilities', capabilities: tooling.loadedCapabilities, className: 'tag-chip tag-chip--active' }
      : null,
    tooling.effectiveTools.length > 0
      ? { label: 'Tool surface', values: tooling.effectiveTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
  ]);
  appendNotebookSection(body, 'Scope', scope);
  appendNotebookToolSection(body, detail, snap.tools_used || tooling.toolsUsed);
  appendNotebookToolSurface(body, snap.request_id, detail);
  appendNotebookSpawnedWork(body, loop, snap, isTop);
  appendNotebookModelExchange(body, snap.request_id, detail);
  appendNotebookEvidence(body, detail);
  return cell;
}

function buildStatusNotebookCell(loop) {
  const next = describeNextTurn(loop);
  const { cell, body } = makeNotebookCell({
    kind: loop.state === 'error' ? 'issue' : 'status',
    ordinal: 'state',
    time: '',
    eyebrow: 'No retained iteration yet',
    title: next.title,
    state: formatSchemaToken(loop.state || 'pending'),
    stateKind: loop.state === 'error' ? 'issue' : 'complete',
    meta: next.meta,
    chips: [
      makeTraceChip(formatSchemaToken(loop.state || 'pending')),
      loop.event_driven ? makeTraceChip('event driven') : null,
      loop.handler_only ? makeTraceChip('handler') : makeTraceChip('model loop'),
    ],
  });
  appendNotebookSpawnedWork(body, loop, null, true);
  return cell;
}

function renderTraceNotebook(loop) {
  if (!forensics.notebook) return;
  const history = Array.isArray(iterationHistory) ? iterationHistory : [];
  const detail = getActiveRequestDetail();
  const childCount = getTraceChildLoops(loop).length;
  forensics.notebook.innerHTML = '';
  if (forensics.notebookMeta) {
    const bits = [];
    if (loop.state === 'processing') bits.push('live');
    if (history.length > 0) bits.push(formatNumber(history.length) + ' retained turns');
    if (detail && detail.tool_calls) bits.push(formatNumber(detail.tool_calls.length) + ' retained tool records');
    if (detail) {
      const retainedToolDefinitions = countToolDefinitions(getToolDefinitionSnapshots(detail));
      if (retainedToolDefinitions > 0) bits.push(formatNumber(retainedToolDefinitions) + ' retained tool definitions');
    }
    if (childCount > 0) bits.push(formatNumber(childCount) + ' child loop' + (childCount === 1 ? '' : 's'));
    forensics.notebookMeta.textContent = bits.join(' · ') || 'awaiting first turn';
  }

  if (loop.state === 'processing') {
    forensics.notebook.appendChild(buildLiveNotebookCell(loop));
    for (const snap of history.slice(0, 7)) {
      forensics.notebook.appendChild(buildPastNotebookCell(loop, snap, false));
    }
    return;
  }

  if (history.length > 0) {
    for (const [index, snap] of history.slice(0, 8).entries()) {
      forensics.notebook.appendChild(buildPastNotebookCell(loop, snap, index === 0));
    }
    return;
  }

  forensics.notebook.appendChild(buildStatusNotebookCell(loop));
}

function describeNextTurn(loop) {
  const cfg = getLoopConfig(loop);
  if (loop.state === 'processing') {
    return {
      title: 'After the active turn',
      meta: 'The loop will compute its next sleep or wait state when this request completes.',
    };
  }
  if (loop.state === 'waiting') {
    return {
      title: 'Next external event',
      meta: loop.event_driven
        ? 'The next turn starts when the subscribed event source delivers a matching payload.'
        : 'The loop is waiting for its next trigger.',
    };
  }
  if (loop.state === 'sleeping') {
    const sleepTimer = sleepTimers.get(nodeId);
    let title = 'Next scheduled wake';
    if (sleepTimer && sleepTimer.durationMs > 0) {
      const remaining = sleepTimer.durationMs - (Date.now() - sleepTimer.startedAt);
      if (remaining > 0) title = 'Wakes in ' + formatDuration(remaining);
    }
    const supervisor = cfg.Supervisor && cfg.SupervisorProb > 0
      ? Math.round(cfg.SupervisorProb * 100) + '% supervisor chance'
      : '';
    return {
      title,
      meta: supervisor || 'Normal model turn unless the loop adjusts its own sleep.',
    };
  }
  if (loop.state === 'error') {
    return {
      title: 'Retry on next wake',
      meta: loop.last_error || 'The next turn will retry after backoff or an external wake.',
    };
  }
  return {
    title: formatSchemaToken(loop.state || 'pending'),
    meta: 'No future turn is currently scheduled by the dashboard snapshot.',
  };
}

function makeTurnChip(text, className = '') {
  const chip = makeScopeChip(text, className);
  if (chip) chip.classList.add('trace-cell__chip');
  return chip;
}

function makeRequestTurnChip(requestID) {
  if (!requestID) return null;
  const chip = makeTurnChip('req ' + shortID(requestID), 'forensics-scope__chip--entity');
  if (!chip) return null;
  chip.classList.add('trace-cell__chip--clickable');
  chip.title = 'Click to inspect request\n' + requestID;
  chip.addEventListener('click', (e) => {
    e.stopPropagation();
    if (e.shiftKey) {
      void writeClipboardText(requestID).catch(() => {});
      return;
    }
    if (typeof window.onRequestChipClick === 'function') {
      window.onRequestChipClick(requestID);
    }
  });
  return chip;
}

function setForensicsRequestLoading(requestID) {
  activeRequestID = requestID || '';
  activeRequestJSON = '';
  activeRequestDetail = null;
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
  if (forensics.empty) {
    forensics.empty.textContent = requestID
      ? 'Loading request detail ' + shortID(requestID) + '…'
      : 'Waiting for the first request detail in this loop.';
  }
  setForensicsLoaded(false);
  if (loopData) renderTraceNotebook(loopData);
}

async function fetchRequestDetailIntoForensics(requestID) {
  if (!requestID) {
    activeRequestID = '';
    activeRequestJSON = '';
    activeRequestDetail = null;
    clearRequestDetailCooldown(requestID);
    if (forensics.ids) forensics.ids.innerHTML = '';
    if (forensics.meta) forensics.meta.innerHTML = '';
    if (forensics.requestMeta) {
      forensics.requestMeta.textContent = '';
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    if (forensics.empty) {
      forensics.empty.textContent = loopData && loopData.state === 'processing'
        ? 'The loop is active, but request detail is still materializing for the current turn. Live tool and iteration telemetry stays in the sidebar until it is ready.'
        : 'Waiting for the first request detail in this loop.';
    }
    setForensicsLoaded(false);
    if (loopData) renderTraceNotebook(loopData);
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
      activeRequestDetail = null;
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
      if (forensics.empty) {
        forensics.empty.textContent = resp.status === 404
          ? 'This request detail is no longer available. It may have been evicted from the live buffer, or archival content may not include this turn.'
          : resp.status === 503
            ? 'Request detail is unavailable because content retention is disabled for this runtime.'
            : 'Request detail failed to load.';
      }
      setForensicsLoaded(false);
      if (loopData) renderTraceNotebook(loopData);
      updateForensicsControls();
      return;
    }

    const detail = await resp.json();
    const detailLooksFinal = !!(detail.assistant_content || detail.output_tokens > 0 || detail.exhausted);
    activeRequestID = requestID;
    activeRequestJSON = JSON.stringify(detail, null, 2);
    activeRequestDetail = detail;
    clearRequestDetailCooldown(requestID);
    if (detailLooksFinal) {
      clearRecentlyCompletedRequest(requestID);
    }

    if (forensics.requestMeta) {
      const metaBits = [];
      if (!followLatestRequest) metaBits.push('Pinned');
      metaBits.push('req ' + shortID(requestID));
      if (detail.model) metaBits.push(shortModelName(detail.model));
      if (detail.iteration_count) metaBits.push(formatNumber(detail.iteration_count) + ' iterations');
      forensics.requestMeta.textContent = metaBits.join(' · ');
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    renderRequestDetail(detail, {
      ids: forensics.ids,
      meta: forensics.meta,
      content: forensics.content,
      waterfall: forensics.waterfall,
    });
    setForensicsLoaded(true);
    if (loopData) {
      renderTraceNotebook(loopData);
    }
    updateForensicsControls();
  } catch (err) {
    console.warn('Failed to fetch request detail:', err);
    activeRequestDetail = null;
    rememberRequestDetailFailure(requestID, 0);
    if (forensics.ids) forensics.ids.innerHTML = '';
    if (forensics.meta) forensics.meta.innerHTML = '';
    if (forensics.requestMeta) {
      forensics.requestMeta.textContent = 'Request detail failed';
      forensics.requestMeta.classList.remove('forensics-card__meta--loading');
    }
    if (forensics.empty) forensics.empty.textContent = 'Request detail failed to load.';
    setForensicsLoaded(false);
    if (loopData) renderTraceNotebook(loopData);
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
    const titleText = (loopData.name || nodeId.slice(0, 8)) + ' trace';
    const subtitleBits = [
      followLatestRequest ? 'Following live causality for this loop.' : 'Pinned to a specific request while live state continues updating.',
      loopData.state === 'processing' ? 'Active tool calls and delegate branches update as events arrive.' : 'Latest retained turn remains the forensic anchor while the loop waits or sleeps.',
    ].filter(Boolean);
    forensics.title.textContent = titleText;
    forensics.title.title = subtitleBits.join(' ');
  }
  if (forensics.subtitle) {
    forensics.subtitle.textContent = '';
    forensics.subtitle.hidden = true;
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

  const refreshCompleted = shouldRefreshRecentlyCompletedRequest(targetRequestID);
  if (shouldCooldownRequestDetail(targetRequestID, force)) return;
  if (!force && activeRequestID === targetRequestID && forensics.empty?.hidden && !refreshCompleted) return;
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
    replaceLoopStatusSnapshot(Array.isArray(statuses) ? statuses : []);
    const match = loopStatusByID.get(nodeId);
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
        match._currentRequestID = (match._llmContext && match._llmContext.request_id) || '';
      }
      loopData = match;
      document.title = 'Thane \u00b7 ' + (match.name || nodeId.slice(0, 8)) + ' trace';
      renderLoopDetail();
    }
    setConnStatus('ok', 'Connected \u2014 ' + formatTime(new Date()));
  });

  es.addEventListener('loop', (e) => {
    const evt = JSON.parse(e.data);
    const updatedStatus = upsertLoopStatusFromEvent(evt);
    if (evt.data && evt.data.loop_id === nodeId) {
      applyLoopEvent(evt);
      renderLoopDetail();
    } else if (loopData && updatedStatus && updatedStatus.parent_id === nodeId) {
      renderTraceNotebook(loopData);
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

  if (evt.kind === 'loop_iteration_complete' && evt.data && evt.data.request_id) {
    noteRecentlyCompletedRequest(evt.data.request_id);
  }
}

function renderLoopDetail() {
  if (!loopData) return;

  document.title = 'Thane \u00b7 ' + (loopData.name || nodeId.slice(0, 8)) + ' trace';
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
  renderLoopScope(loopData);
  renderTraceNotebook(loopData);
  syncLoopRequestDetail();
  updatePopupFooter();

  const currentTooling = getLoopCurrentTooling(loopData);
  const tagsSection = $('#detail-tags');
  const tagsList = $('#detail-tags-list');
  const groups = makeIterationScopePanel([
    currentTooling.loadedCapabilities.length > 0
      ? { label: 'Loaded capabilities', capabilities: currentTooling.loadedCapabilities, className: 'tag-chip tag-chip--active' }
      : null,
    currentTooling.configuredTags.length > 0
      ? { label: 'Configured tags', values: currentTooling.configuredTags, className: 'tag-chip tag-chip--muted' }
      : null,
    currentTooling.effectiveTools.length > 0
      ? { label: 'Tool surface', values: currentTooling.effectiveTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
    currentTooling.excludedTools.length > 0
      ? { label: 'Excluded tools', values: currentTooling.excludedTools, className: 'iter-card__tool-item iter-card__tool-item--scope' }
      : null,
  ]);
  if (groups) {
    tagsSection.hidden = false;
    tagsList.innerHTML = '';
    tagsList.appendChild(groups);
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
  writeClipboardText(activeRequestJSON).then(() => {
    forensics.copyJSON.textContent = 'Copied';
    forensics.copyJSON.classList.add('copy-btn--copied');
    setTimeout(() => {
      forensics.copyJSON.textContent = 'JSON';
      forensics.copyJSON.classList.remove('copy-btn--copied');
    }, 1200);
  }).catch(() => {
    forensics.copyJSON.textContent = 'Failed';
    setTimeout(() => {
      forensics.copyJSON.textContent = 'JSON';
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
