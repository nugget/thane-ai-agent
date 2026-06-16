// main.js — console entry point.
//
// Loads the node-graph home view (app.js self-boots on import) and wires the
// hash router with the surface views. theme.js initializes independently from
// its own script tag. Surface views are placeholders until their step lands.

import { initRouter, registerSurface } from './router.js';
import { placeholderView } from './views/placeholder.js';
import './app.js';

registerSurface('models', placeholderView('Models & Routing',
  'Fleet, registry, deployment + resource policies, and the routing audit trail.'));
registerSurface('loop-definitions', placeholderView('Loop Definitions',
  'The durable loop-definition catalog — eligibility, policy, and effective inherited config.'));
registerSurface('usage', placeholderView('Usage & History',
  'Token and cost usage over time, session stats, and the conversation archive.'));
registerSurface('schedules', placeholderView('Schedule & Wakes',
  'Scheduled tasks and their execution history, from /v1/schedules.'));

initRouter();
