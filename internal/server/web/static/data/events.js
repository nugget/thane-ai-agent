// data/events.js — one shared SSE stream for /v1/loops/events.
//
// The graph (and any live surface, e.g. the schedule view) subscribes here
// rather than opening its own EventSource. The stream opens on the first
// subscriber and closes on the last (refcounted), so there is never more than
// one connection regardless of how many surfaces are live. Event payloads are
// byte-identical to the former /api/loops/events: a "snapshot" array, then
// "loop" and "delegate" events. EventSource handles reconnection internally;
// subscribers observe it through onState('connecting'|'connected'|'disconnected').

const STREAM_URL = '/v1/loops/events';

let source = null;
let closing = false;
const subscribers = new Set();

function dispatch(method, payload) {
  for (const sub of subscribers) {
    const fn = sub[method];
    if (!fn) continue;
    try {
      fn(payload);
    } catch (err) {
      console.error('SSE subscriber error in', method, err);
    }
  }
}

function open() {
  closing = false;
  dispatch('onState', 'connecting');
  source = new EventSource(STREAM_URL);
  source.addEventListener('snapshot', (e) => dispatch('onSnapshot', JSON.parse(e.data)));
  source.addEventListener('loop', (e) => dispatch('onLoop', JSON.parse(e.data)));
  source.addEventListener('delegate', (e) => dispatch('onDelegate', JSON.parse(e.data)));
  source.onopen = () => { if (!closing) dispatch('onState', 'connected'); };
  source.onerror = () => { if (!closing) dispatch('onState', 'disconnected'); };
}

function close() {
  closing = true;
  if (source) {
    source.close();
    source = null;
  }
}

// subscribe registers a handler set ({onSnapshot, onLoop, onDelegate, onState},
// any subset) and opens the stream on the first subscriber. Returns an
// unsubscribe function that closes the stream once the last subscriber leaves.
export function subscribe(handlers) {
  subscribers.add(handlers);
  if (subscribers.size === 1) open();
  return function unsubscribe() {
    subscribers.delete(handlers);
    if (subscribers.size === 0) close();
  };
}

window.addEventListener('pagehide', close);
window.addEventListener('beforeunload', close);
