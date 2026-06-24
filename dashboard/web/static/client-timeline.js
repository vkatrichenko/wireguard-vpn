// client-timeline.js — renders the per-client connection timeline (online
// bands) inside the expanded client-detail panel. Single IIFE, no module
// system, loaded with `defer` from dashboard.html after Chart.js + the
// date-fns adapter (which it reuses; it adds no dependency).
//
// Data delivery: the panel handler embeds the derived session spans + the
// [from,to] window as JSON in a <script type="application/json"> block beside
// the canvas (server-side, from the same history.Derive call that feeds the
// summary <dl>). We parse that block rather than issuing a second fetch — the
// panel is pubkey-keyed while the history API is name-keyed, so reading the
// already-rendered data sidesteps the mismatch and the extra round-trip, and
// lets the render test assert the bands reflect the seeded handshakes.
//
// Shape: a horizontal floating-bar chart (`type: "bar"`, `indexAxis: "y"`).
// Each session is one bar whose value is the [start, end] time pair on a time
// x-axis bounded to [from, to]; Chart.js draws that as a band from start to
// end. A single-handshake session (start == end) is widened by a small visual
// epsilon so the band is still visible as a sliver.
//
// Lifecycle mirrors charts.js's per-client chart: hydrated on the htmx swap
// that brings the detail fragment in (the swapped <script> tag does NOT
// auto-execute, so we read it on htmx:afterSwap), re-rendered when the panel
// is re-fetched (range change → tab reload → re-expand), and torn down per
// pubkey so reopening a row doesn't leak a Chart instance. A failed panel swap
// keeps the prior content and trips the shared #stale-pill via htmx-stale.js —
// this module adds no stale logic of its own.
//
// Theme-aware: gridline / tick colors are read from --gridline / --text-muted
// and the band fill from --accent, re-applied on the window __themeChanged
// event exactly like charts.js, so a runtime theme toggle recolors live bands.
(function () {
  "use strict";

  // SINGLE_HANDSHAKE_EPS widens a zero-duration session so its band is a
  // visible sliver rather than an invisible zero-width bar. 30s is well under
  // the 10m session-gap threshold, so it never visually merges two sessions.
  var SINGLE_HANDSHAKE_EPS_MS = 30 * 1000;

  // timelineCharts holds the Chart instances keyed by pubkey so reopening the
  // same row (or a re-render on range change) tears the prior one down cleanly,
  // and the __themeChanged handler can recolor them in place.
  var timelineCharts = new Map();

  function themeColors() {
    var root = getComputedStyle(document.documentElement);
    return {
      grid: root.getPropertyValue("--gridline").trim() || "rgba(0, 0, 0, 0.08)",
      text: root.getPropertyValue("--text-muted").trim() || "#6b7280",
      band: root.getPropertyValue("--accent").trim() || "#2563eb",
    };
  }

  // insertNotice mirrors charts.js: a styled <p> after the panel heading, or —
  // when the detail panel has no <h2> — right before the canvas. Used for the
  // empty ("No sessions yet") state.
  function insertNotice(canvas, cls, text) {
    var p = document.createElement("p");
    p.className = cls;
    p.textContent = text;
    canvas.insertAdjacentElement("beforebegin", p);
  }

  // timeScaleConfig matches charts.js so the timeline x-axis labels read the
  // same as the throughput chart for the same range.
  function timeScaleConfig(range) {
    var unit;
    switch (range) {
      case "1h": unit = "minute"; break;
      case "7d": unit = "day"; break;
      case "6h":
      case "24h":
      default:   unit = "hour"; break;
    }
    return {
      unit: unit,
      tooltipFormat: "MMM d, h:mm a",
      displayFormats: { minute: "h:mm a", hour: "haaa", day: "MMM d" },
    };
  }

  // parsePayload reads + JSON-parses the embedded data block for a pubkey.
  // Returns null on a missing/invalid block so the caller can render the
  // empty state rather than throwing.
  function parsePayload(pubkey) {
    var el = document.getElementById("client-timeline-data-" + pubkey);
    if (!el) return null;
    try {
      var p = JSON.parse(el.textContent);
      if (!p || !Array.isArray(p.sessions)) return null;
      return p;
    } catch (e) {
      console.error("client-timeline.js: bad payload JSON for " + pubkey, e);
      return null;
    }
  }

  function destroyTimeline(pubkey) {
    var prior = timelineCharts.get(pubkey);
    if (prior) {
      try { prior.destroy(); } catch (e) { /* already destroyed */ }
      timelineCharts.delete(pubkey);
    }
  }

  // initTimeline reads the embedded payload for pubkey and builds the
  // floating-bar timeline. Re-entrant: destroys any prior instance first.
  function initTimeline(pubkey) {
    var canvas = document.getElementById("client-timeline-" + pubkey);
    if (!canvas) return;
    destroyTimeline(pubkey);

    var payload = parsePayload(pubkey);
    if (!payload) {
      insertNotice(canvas, "error", "Failed to load timeline data");
      return;
    }
    if (payload.sessions.length === 0) {
      insertNotice(canvas, "empty", "No sessions yet");
      // Still draw the (empty) axis so the panel doesn't jump on a later
      // refresh that brings sessions in.
    }

    var range = canvas.dataset.range || "24h";
    var colors = themeColors();

    // One bar carrying every session as a [start, end] pair. Chart.js renders
    // each pair as a floating band on the time scale. All bands share the same
    // y category so they stack on a single "Online" lane.
    var bars = payload.sessions.map(function (s) {
      var start = new Date(s.start).getTime();
      var end = new Date(s.end).getTime();
      if (end <= start) end = start + SINGLE_HANDSHAKE_EPS_MS;
      return { x: [start, end], y: "Online" };
    });

    var fromMs = payload.from ? new Date(payload.from).getTime() : undefined;
    var toMs = payload.to ? new Date(payload.to).getTime() : undefined;

    var chart = new Chart(canvas, {
      type: "bar",
      data: {
        labels: ["Online"],
        datasets: [
          {
            label: "Online",
            data: bars,
            backgroundColor: colors.band,
            borderWidth: 0,
            borderSkipped: false,
            barThickness: 22,
          },
        ],
      },
      options: {
        indexAxis: "y",
        scales: {
          x: {
            type: "time",
            min: fromMs,
            max: toMs,
            time: timeScaleConfig(range),
            grid: { color: colors.grid },
            ticks: { color: colors.text },
            border: { color: colors.grid },
          },
          y: {
            grid: { display: false, color: colors.grid },
            ticks: { color: colors.text },
            border: { color: colors.grid },
          },
        },
        plugins: {
          legend: { display: false },
          tooltip: {
            callbacks: {
              label: function (ctx) {
                var v = ctx.raw && ctx.raw.x;
                if (!v) return "";
                var a = new Date(v[0]).toLocaleString();
                var b = new Date(v[1]).toLocaleString();
                return a + " → " + b;
              },
            },
          },
        },
        animation: false,
      },
    });
    timelineCharts.set(pubkey, chart);
  }

  // applyTheme recolors live timelines in place on a runtime theme toggle —
  // grid/tick from the tokens, band fill from --accent.
  function applyTheme() {
    if (timelineCharts.size === 0) return;
    var colors = themeColors();
    timelineCharts.forEach(function (chart) {
      try {
        chart.options.scales.x.grid.color = colors.grid;
        chart.options.scales.y.grid.color = colors.grid;
        chart.options.scales.x.ticks.color = colors.text;
        chart.options.scales.y.ticks.color = colors.text;
        chart.options.scales.x.border.color = colors.grid;
        chart.options.scales.y.border.color = colors.grid;
        chart.data.datasets[0].backgroundColor = colors.band;
        chart.update("none");
      } catch (e) {
        console.warn("client-timeline.js: theme update failed", e);
      }
    });
  }

  // The detail fragment is swapped into the placeholder row on row click and
  // re-swapped on a range-change-driven re-expand. Match the same path
  // charts.js matches so we hydrate on exactly that swap and ignore unrelated
  // ones (tab partials, the 10s metrics refresh).
  document.body.addEventListener("htmx:afterSwap", function (event) {
    var path = (event.detail && event.detail.requestConfig && event.detail.requestConfig.path) || "";
    var m = path.match(/^\/partial\/clients\/([^/]+)\/detail$/);
    if (!m) return;
    initTimeline(decodeURIComponent(m[1]));
  });

  window.addEventListener("__themeChanged", applyTheme);
})();
