// data/viewState.js — shared per-window interaction state.
//
// The loop store (data/loops.js) holds the DATA; this holds the user's CHOICES
// — which subtree is anchored, and which loop is selected for inspection. Both
// are observable so every view (graph, process table, breadcrumb) stays in
// sync: anchor in one, and the others re-scope; select in one, and the others
// highlight. anchor and selection are orthogonal — you can be anchored on
// `signal` while inspecting `signal/aimee`.

export function createViewState() {
  let anchor = null; // loop id to scope to; null = the whole tree
  let selection = null; // loop id being inspected; null = nothing

  const listeners = new Set();
  const emit = () => {
    const snap = { anchor, selection };
    for (const fn of listeners) fn(snap);
  };

  return {
    get anchor() {
      return anchor;
    },
    get selection() {
      return selection;
    },

    // setAnchor scopes every view to `id` + its descendants. A null id (or the
    // current anchor) resets to the whole tree.
    setAnchor(id) {
      const next = id || null;
      if (next === anchor) return;
      anchor = next;
      emit();
    },

    setSelection(id) {
      const next = id || null;
      if (next === selection) return;
      selection = next;
      emit();
    },

    // resetAnchor returns to the unanchored whole tree — the "recenter to
    // core" affordance. Selection is left untouched (anchor and selection are
    // orthogonal).
    resetAnchor() {
      if (anchor === null) return;
      anchor = null;
      emit();
    },

    subscribe(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
  };
}
