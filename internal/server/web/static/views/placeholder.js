// views/placeholder.js — stand-in for a surface view not yet built.
//
// Returns the surface view interface the router expects ({ mount }). Each of
// the four real surfaces (Models & Routing, Loop Definitions, Usage & History,
// Schedule & Wakes) replaces its placeholder in a later step.

export function placeholderView(title, blurb) {
  return {
    mount(root) {
      const surface = document.createElement('div');
      surface.className = 'surface';

      const header = document.createElement('div');
      header.className = 'surface-header';
      const h = document.createElement('h2');
      h.textContent = title;
      header.appendChild(h);

      const empty = document.createElement('div');
      empty.className = 'surface-empty';
      const p = document.createElement('p');
      p.textContent = blurb || 'This surface is coming soon.';
      empty.appendChild(p);

      surface.append(header, empty);
      root.appendChild(surface);
    },
  };
}
