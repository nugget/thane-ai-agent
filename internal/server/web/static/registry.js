'use strict';

const registryEls = {
  title: document.getElementById('registry-title'),
  subtitle: document.getElementById('registry-subtitle'),
  sectionTitle: document.getElementById('registry-section-title'),
  meta: document.getElementById('registry-meta'),
  summary: document.getElementById('registry-summary'),
  empty: document.getElementById('registry-empty'),
  toolboxSection: document.getElementById('registry-toolbox-section'),
  toolboxList: document.getElementById('registry-toolbox-list'),
  modelResourcesSection: document.getElementById('registry-model-resources-section'),
  modelResources: document.getElementById('registry-model-resources'),
  modelDeploymentsSection: document.getElementById('registry-model-deployments-section'),
  modelDeployments: document.getElementById('registry-model-deployments'),
  refresh: document.getElementById('registry-refresh'),
  openCore: document.getElementById('registry-open-core'),
};

const registryParams = new URLSearchParams(window.location.search);
const registryKey = String(registryParams.get('registry') || 'toolbox').trim().toLowerCase();

function openCoreWindow() {
  const w = window.open(
    '/static/detail.html?type=system&name=' + encodeURIComponent('Core'),
    'detail-system',
    'popup=yes,width=900,height=450',
  );
  if (w) {
    w.addEventListener('load', () => {
      w.document.title = 'Thane \u00b7 Core';
    });
  }
}

function resetRegistrySections() {
  registryEls.summary.innerHTML = '';
  registryEls.meta.textContent = '';
  registryEls.toolboxList.innerHTML = '';
  registryEls.modelResources.innerHTML = '';
  registryEls.modelDeployments.innerHTML = '';
  registryEls.empty.hidden = true;
  registryEls.empty.textContent = '';
  registryEls.toolboxSection.hidden = true;
  registryEls.modelResourcesSection.hidden = true;
  registryEls.modelDeploymentsSection.hidden = true;
}

function configureRegistryChrome(key) {
  if (key === 'models') {
    registryEls.title.textContent = 'Model Registry';
    registryEls.sectionTitle.textContent = 'Model Registry';
    registryEls.subtitle.textContent = 'Inspect routing resources, discovered deployments, policy overlays, and observed runtime attributes for the current model bench.';
    document.title = 'Thane \u00b7 Model Registry';
    return;
  }
  if (key === 'scheduled') {
    registryEls.title.textContent = 'Scheduled Loops';
    registryEls.sectionTitle.textContent = 'Scheduled Loops';
    registryEls.subtitle.textContent = 'This focused registry window is reserved for scheduled loop definitions, cadence, and wake policy once that backend catalog is exposed.';
    document.title = 'Thane \u00b7 Scheduled Loops';
    return;
  }
  registryEls.title.textContent = 'Toolbox & Capabilities';
  registryEls.sectionTitle.textContent = 'Toolbox & Capabilities';
  registryEls.subtitle.textContent = 'Browse the runtime capability catalog and tool membership directly from the backend source of truth. Future tool metrics can layer into this view without overloading the core inspector.';
  document.title = 'Thane \u00b7 Toolbox & Capabilities';
}

function renderToolboxRegistry(sys) {
  const catalog = sys && sys.capability_catalog;
  const entries = getCapabilityCatalogEntries(sys);
  registryEls.toolboxSection.hidden = false;
  renderCapabilityCatalog(
    registryEls.summary,
    registryEls.toolboxList,
    registryEls.meta,
    entries,
    catalog && catalog.activation_tools,
  );
}

function renderModelRegistryView(sys) {
  registryEls.modelResourcesSection.hidden = false;
  registryEls.modelDeploymentsSection.hidden = false;
  renderModelRegistry(
    registryEls.summary,
    registryEls.modelResources,
    registryEls.modelDeployments,
    registryEls.meta,
    sys ? sys.model_registry : null,
    sys ? sys.router_stats : null,
  );
}

function renderPlannedRegistry() {
  registryEls.summary.appendChild(buildSystemStat('Status', 'planned'));
  registryEls.summary.appendChild(buildSystemStat('Scope', 'scheduler'));
  registryEls.meta.textContent = 'backend catalog pending';
  registryEls.empty.hidden = false;
  registryEls.empty.textContent = 'Scheduled loop registry plumbing is not exposed yet. This window exists now so the focused-registry pattern is ready when that backend surface lands.';
}

async function fetchRegistry() {
  resetRegistrySections();
  configureRegistryChrome(registryKey);

  if (registryKey === 'scheduled') {
    renderPlannedRegistry();
    return;
  }

  try {
    const resp = await fetch('/api/system');
    if (!resp.ok) throw new Error('HTTP ' + resp.status);
    const sys = await resp.json();

    if (registryKey === 'models') {
      renderModelRegistryView(sys);
      return;
    }
    renderToolboxRegistry(sys);
  } catch (err) {
    console.warn('Failed to load registry view:', registryKey, err);
    registryEls.empty.hidden = false;
    registryEls.empty.textContent = 'Could not load current registry data from /api/system.';
  }
}

registryEls.refresh.addEventListener('click', () => {
  void fetchRegistry();
});

registryEls.openCore.addEventListener('click', () => {
  openCoreWindow();
});

void fetchRegistry();
setInterval(() => {
  void fetchRegistry();
}, 10000);
