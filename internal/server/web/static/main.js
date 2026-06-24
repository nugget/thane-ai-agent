// main.js — console entry point.
//
// Boots the node-graph home view (createGraph) and wires the hash router with
// the surface views. theme.js initializes independently from its own script
// tag. Surface views are placeholders until their step lands.

import { initRouter, registerSurface } from './router.js';
import { placeholderView } from './views/placeholder.js';
import { createGraph, getStore } from './app.js';
import { createViewState } from './data/viewState.js';
import { loopTableView } from './views/loopTable.js';

// Boot the node graph on the real /v1 client + SSE stream.
createGraph();

// Shared interaction state (anchor + selection) for views that read the same
// store as the graph, so they stay in sync.
const viewState = createViewState();

// Process table — a flat, sortable view of the same running loops the graph
// renders, scoped by the shared anchor.
registerSurface('processes', loopTableView(getStore, viewState));

registerSurface('models', placeholderView('Models & Routing',
  'Fleet, registry, deployment + resource policies, and the routing audit trail.'));
registerSurface('loop-definitions', placeholderView('Loop Definitions',
  'The durable loop-definition catalog — eligibility, policy, and effective inherited config.'));
registerSurface('usage', placeholderView('Usage & History',
  'Token and cost usage over time, session stats, and the conversation archive.'));
registerSurface('schedules', placeholderView('Schedule & Wakes',
  'Scheduled tasks and their execution history, from /v1/schedules.'));

initRouter();
