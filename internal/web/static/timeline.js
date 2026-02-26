// timeline.js — NLE-style session timeline renderer for the Thane dashboard.
// Fetches iteration data from the JSON API and renders proportionally-sized
// iteration boxes in the timeline track.
//
// Idempotent: safe to re-execute on HTMX content swaps.
(function () {
  "use strict";

  // Preserve state across re-executions (HTMX navigates away and back).
  var activeSessionID = (window.timeline && window.timeline._activeID) || null;
  var selectedIterIndex = null;

  // modelClass maps a model string to a CSS class for color coding.
  function modelClass(model) {
    if (!model) return "unknown";
    var m = model.toLowerCase();
    if (m.indexOf("claude") !== -1 || m.indexOf("anthropic") !== -1)
      return "claude";
    if (
      m.indexOf("ollama") !== -1 ||
      m.indexOf("llama") !== -1 ||
      m.indexOf("mistral") !== -1 ||
      m.indexOf("gemma") !== -1 ||
      m.indexOf("qwen") !== -1
    )
      return "ollama";
    return "unknown";
  }

  // shortModel extracts a compact display name from a full model string.
  function shortModel(model) {
    if (!model) return "?";
    // Strip provider prefix like "ollama:" or "anthropic:"
    var parts = model.split(":");
    var name = parts.length > 1 ? parts[parts.length - 1] : model;
    // Strip date suffixes like -20250514
    name = name.replace(/-\d{8}$/, "");
    // Truncate long names
    if (name.length > 20) name = name.substring(0, 18) + "\u2026";
    return name;
  }

  // fmtTokens formats a token count compactly: 29000 → "29K".
  function fmtTokens(n) {
    if (n < 1000) return String(n);
    if (n < 1000000) return (n / 1000).toFixed(n < 10000 ? 1 : 0) + "K";
    return (n / 1000000).toFixed(1) + "M";
  }

  // fmtMs formats milliseconds as a human-readable duration.
  function fmtMs(ms) {
    if (ms < 1000) return ms + "ms";
    var s = ms / 1000;
    if (s < 60) return s.toFixed(1) + "s";
    var m = Math.floor(s / 60);
    var rem = Math.floor(s % 60);
    return m + "m " + rem + "s";
  }

  // el creates a DOM element with optional class and text.
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  // renderTrack builds the iteration boxes inside the timeline track.
  function renderTrack(data) {
    var track = document.getElementById("timeline-track");
    var empty = document.getElementById("timeline-empty");
    var detail = document.getElementById("timeline-detail");
    var info = document.getElementById("timeline-session-info");

    track.innerHTML = "";
    detail.style.display = "none";
    detail.innerHTML = "";
    selectedIterIndex = null;

    // Session info header with link to full detail page.
    var title = data.session.title || data.session.id.substring(0, 8);
    var statusBadge =
      data.session.status === "active" ? "badge-teal" : "badge-ok";
    info.innerHTML =
      '<span class="badge ' +
      statusBadge +
      '">' +
      data.session.status +
      "</span> " +
      '<a href="/sessions/' +
      encodeURIComponent(data.session.id) +
      '" hx-get="/sessions/' +
      encodeURIComponent(data.session.id) +
      '" hx-target="#content" hx-push-url="true">' +
      escapeHTML(title) +
      "</a>" +
      ' <span class="text-muted">' +
      escapeHTML(data.session.duration) +
      "</span>";

    // Let HTMX discover the dynamically-added hx-* attributes.
    if (typeof htmx !== "undefined") htmx.process(info);

    if (!data.iterations || data.iterations.length === 0) {
      empty.textContent = "No iterations recorded for this session.";
      empty.style.display = "";
      track.style.display = "none";
      return;
    }

    empty.style.display = "none";
    track.style.display = "";

    // Compute proportional widths. Use durationMs with a minimum.
    var totalMs = 0;
    for (var i = 0; i < data.iterations.length; i++) {
      totalMs += Math.max(data.iterations[i].duration_ms, 500);
    }

    for (var idx = 0; idx < data.iterations.length; idx++) {
      var iter = data.iterations[idx];

      // Connector between boxes.
      if (idx > 0) {
        var conn = el("div", "iter-connector");
        var label = el("span", "connector-label", "\u25B6");
        // Show break reason on the connector if previous iteration had one.
        var prev = data.iterations[idx - 1];
        if (prev.break_reason) {
          label.textContent = prev.break_reason;
          label.classList.add("badge", "badge-warn");
        }
        conn.appendChild(label);
        track.appendChild(conn);
      }

      // Iteration box.
      var box = el("div", "iter-box");
      box.setAttribute("data-model-class", modelClass(iter.model));
      box.setAttribute("data-iter-index", String(iter.index));

      // Proportional width (percentage of track, min 5rem enforced by CSS).
      var pct = (Math.max(iter.duration_ms, 500) / totalMs) * 100;
      box.style.flexBasis = Math.max(pct, 5) + "%";
      box.style.flexGrow = "0";

      // Title row.
      var titleRow = el("div", "iter-title");
      titleRow.appendChild(el("span", null, "#" + iter.index));
      if (iter.break_reason) {
        var brBadge = el("span", "badge badge-warn", iter.break_reason);
        titleRow.appendChild(brBadge);
      }
      box.appendChild(titleRow);

      // Model.
      box.appendChild(el("div", "iter-model", shortModel(iter.model)));

      // Stats row.
      var stats = el("div", "iter-stats");
      stats.appendChild(
        el(
          "span",
          null,
          fmtTokens(iter.input_tokens) +
            "\u2192" +
            fmtTokens(iter.output_tokens),
        ),
      );
      stats.appendChild(el("span", null, fmtMs(iter.duration_ms)));
      if (iter.tools_offered && iter.tools_offered.length) {
        stats.appendChild(
          el("span", "text-muted", iter.tools_offered.length + " offered"),
        );
      }
      box.appendChild(stats);

      // Tool badges.
      if (iter.has_tool_calls && iter.tool_calls && iter.tool_calls.length) {
        var tools = el("div", "iter-tools");
        for (var t = 0; t < iter.tool_calls.length; t++) {
          var tc = iter.tool_calls[t];
          var cls = tc.has_error ? "badge badge-err" : "badge badge-muted";
          tools.appendChild(el("span", cls, tc.name));
        }
        box.appendChild(tools);
      } else if (iter.tool_call_count > 0) {
        var tools = el("div", "iter-tools");
        var label =
          iter.tool_call_count +
          " tool" +
          (iter.tool_call_count !== 1 ? "s" : "") +
          " (unlinked)";
        tools.appendChild(el("span", "badge badge-muted", label));
        box.appendChild(tools);
      }

      // Click handler — expand detail below.
      box.addEventListener("click", makeClickHandler(idx, data));
      track.appendChild(box);
    }

    // Children section.
    if (data.children && data.children.length > 0) {
      var childSection = el("div", "timeline-children");
      childSection.appendChild(el("h3", null, "Delegate Sessions"));
      for (var c = 0; c < data.children.length; c++) {
        var child = data.children[c];
        var link = el(
          "span",
          "child-session-link",
          child.title || child.id.substring(0, 8),
        );
        link.setAttribute("data-child-id", child.id);
        link.addEventListener("click", makeChildClickHandler(child.id));
        childSection.appendChild(link);
      }
      track.parentNode.appendChild(childSection);
    }
  }

  function makeClickHandler(idx, data) {
    return function (e) {
      e.stopPropagation();
      toggleDetail(idx, data);
    };
  }

  function makeChildClickHandler(childID) {
    return function (e) {
      e.stopPropagation();
      window.timeline.load(childID);
    };
  }

  // toggleDetail shows/hides the detail panel for an iteration.
  function toggleDetail(idx, data) {
    var detail = document.getElementById("timeline-detail");
    var boxes = document.querySelectorAll(".iter-box");

    // Deselect previous.
    for (var b = 0; b < boxes.length; b++) boxes[b].classList.remove("selected");

    if (selectedIterIndex === idx) {
      // Collapse.
      detail.style.display = "none";
      detail.innerHTML = "";
      selectedIterIndex = null;
      return;
    }

    selectedIterIndex = idx;
    boxes[idx].classList.add("selected");

    var iter = data.iterations[idx];
    detail.innerHTML = "";
    detail.style.display = "";

    // Title.
    var titleDiv = el("div", "detail-title");
    titleDiv.appendChild(el("strong", null, "Iteration #" + iter.index));
    titleDiv.appendChild(el("span", "mono text-muted", iter.model));
    titleDiv.appendChild(el("span", "text-muted", fmtMs(iter.duration_ms)));
    titleDiv.appendChild(
      el(
        "span",
        "text-muted",
        fmtTokens(iter.input_tokens) +
          " \u2192 " +
          fmtTokens(iter.output_tokens) +
          " tokens",
      ),
    );
    if (iter.break_reason) {
      titleDiv.appendChild(el("span", "badge badge-warn", iter.break_reason));
    }
    detail.appendChild(titleDiv);

    // Tool calls.
    if (iter.tool_calls && iter.tool_calls.length > 0) {
      var list = el("ul", "tool-list");
      for (var t = 0; t < iter.tool_calls.length; t++) {
        var tc = iter.tool_calls[t];
        var li = el("li");
        li.appendChild(el("span", "mono", tc.name));
        li.appendChild(el("span", "text-muted", fmtMs(tc.duration_ms)));
        var statusBadge = tc.has_error
          ? el("span", "badge badge-err", "error")
          : el("span", "badge badge-ok", "ok");
        li.appendChild(statusBadge);
        list.appendChild(li);
      }
      detail.appendChild(list);
    } else if (iter.tool_call_count > 0) {
      detail.appendChild(
        el(
          "p",
          "text-muted",
          iter.tool_call_count +
            " tool call" +
            (iter.tool_call_count !== 1 ? "s" : "") +
            " recorded but not yet linked to this iteration.",
        ),
      );
    } else {
      detail.appendChild(
        el("p", "text-muted", "No tool calls in this iteration."),
      );
    }

    // Tools offered.
    if (iter.tools_offered && iter.tools_offered.length > 0) {
      var offered = el("div", "iter-tools-offered");
      offered.appendChild(
        el(
          "span",
          "text-muted",
          iter.tools_offered.length + " tools offered: ",
        ),
      );
      for (var o = 0; o < iter.tools_offered.length; o++) {
        offered.appendChild(el("span", "badge badge-muted", iter.tools_offered[o]));
      }
      detail.appendChild(offered);
    }

    // Link to full session detail.
    var link = el("a", "text-muted");
    link.href = "/sessions/" + encodeURIComponent(activeSessionID);
    link.textContent = "View full session detail \u2192";
    link.style.display = "block";
    link.style.marginTop = "0.5rem";
    link.style.fontSize = "0.8125rem";
    detail.appendChild(link);
  }

  function escapeHTML(str) {
    var div = document.createElement("div");
    div.appendChild(document.createTextNode(str || ""));
    return div.innerHTML;
  }

  // highlightRow marks the active session row in the table.
  function highlightRow(sessionID) {
    var rows = document.querySelectorAll(".timeline-session-row");
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute("data-session-id") === sessionID) {
        rows[i].classList.add("active");
      } else {
        rows[i].classList.remove("active");
      }
    }
  }

  // load fetches timeline data for a session and renders it.
  function load(sessionID) {
    if (!sessionID) return;
    activeSessionID = sessionID;
    highlightRow(sessionID);

    // Remove stale children sections.
    var oldChildren = document.querySelectorAll(".timeline-children");
    for (var i = 0; i < oldChildren.length; i++) oldChildren[i].remove();

    var empty = document.getElementById("timeline-empty");
    empty.textContent = "Loading\u2026";
    empty.style.display = "";
    document.getElementById("timeline-track").style.display = "none";

    fetch("/sessions/" + encodeURIComponent(sessionID) + "/timeline.json")
      .then(function (resp) {
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        return resp.json();
      })
      .then(function (data) {
        renderTrack(data);
      })
      .catch(function (err) {
        empty.textContent = "Failed to load timeline: " + err.message;
        empty.style.display = "";
        document.getElementById("timeline-track").style.display = "none";
      });
  }

  // Expose API. _activeID lets the next execution restore state.
  window.timeline = {
    load: load,
    get _activeID() {
      return activeSessionID;
    },
  };

  // If returning to the timeline page with a previously-selected session,
  // re-render it automatically.
  if (activeSessionID && document.getElementById("timeline-track")) {
    load(activeSessionID);
  }
})();
