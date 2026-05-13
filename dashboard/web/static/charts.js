// charts.js — fetches /api/metrics?range=24h once on page load and renders
// four trend charts (CPU%, Memory%, Rx rate, Tx rate) using Chart.js with the
// date-fns adapter. Single IIFE, no module system, no bundler. Loaded with
// `defer` from dashboard.html so DOM is parsed before this runs.
//
// Empty arrays render a "No data yet — collecting…" message under each card
// heading; the poller writes the first row at startup so this state is only
// visible during the first ~30s of process lifetime. Fetch failures render a
// red error message under each card heading and skip Chart construction.
//
// Theme-aware: gridline / tick / axis-border colors are read from the
// `--gridline` and `--text-muted` CSS custom properties on :root at chart-init
// time, and re-applied on the window `__themeChanged` event so a runtime
// theme toggle re-colors live charts without a page reload. Per-series line
// colors in PALETTE stay constant — they are semantic (CPU red, mem blue,
// rx green, tx amber) and legible on both backgrounds.
(function () {
  "use strict";

  const PALETTE = {
    cpu: "#e25b5b",
    memory: "#5b8de2",
    rx: "#5be29c",
    tx: "#e2b15b",
  };

  const LABELS = {
    cpu: "CPU %",
    memory: "Memory %",
    rx: "Rx B/s",
    tx: "Tx B/s",
  };

  // charts holds the Chart instances built by renderCharts so the
  // __themeChanged handler can iterate and patch colors in place.
  const charts = [];
  // clientCharts holds per-row expand charts keyed by pubkey so reopening
  // the same row tears down the prior instance cleanly.
  const clientCharts = new Map();
  let lastPayload = null;

  // themeColors reads the active theme tokens from :root. Fallbacks match
  // the light-mode defaults in app.css so a missing token still renders.
  function themeColors() {
    const root = getComputedStyle(document.documentElement);
    return {
      grid: root.getPropertyValue("--gridline").trim() || "rgba(0, 0, 0, 0.08)",
      text: root.getPropertyValue("--text-muted").trim() || "#6b7280",
    };
  }

  // insertNotice puts a styled <p> right after the card's <h2>. Used for
  // both the empty-state and the fetch-error branches so they render in a
  // consistent place inside the card.
  function insertNotice(canvas, cls, text) {
    const heading = canvas.parentElement.querySelector("h2");
    if (heading) {
      heading.insertAdjacentHTML(
        "afterend",
        '<p class="' + cls + '">' + text + "</p>",
      );
    }
  }

  // computeRate derives a per-second rate series from a cumulative counter.
  // Skips i=0 (no prior sample). On counter rollback (cum[i] < cum[i-1]) the
  // datapoint is emitted as 0 rather than negative — interface restarts can
  // reset the counter, and a negative rate would distort the y-axis.
  function computeRate(ts, cum) {
    const out = [];
    for (let i = 1; i < ts.length; i++) {
      const dtMs = new Date(ts[i]) - new Date(ts[i - 1]);
      if (dtMs <= 0) continue;
      const dBytes = cum[i] - cum[i - 1];
      const y = dBytes < 0 ? 0 : dBytes / (dtMs / 1000);
      out.push({ x: ts[i], y: y });
    }
    return out;
  }

  // datasetFor maps a canvas's data-chart attribute onto a {x, y}[] series
  // built from the /api/metrics response. Returns [] when the relevant
  // source arrays are empty so the empty-state branch can short-circuit.
  function datasetFor(series, payload) {
    const sys = payload.system || { ts: [], cpu_pct: [], mem_pct: [] };
    const tr = payload.traffic || { ts: [], rx_bytes_cum: [], tx_bytes_cum: [] };
    switch (series) {
      case "cpu":
        return sys.ts.map((t, i) => ({ x: t, y: sys.cpu_pct[i] }));
      case "memory":
        return sys.ts.map((t, i) => ({ x: t, y: sys.mem_pct[i] }));
      case "rx":
        return computeRate(tr.ts, tr.rx_bytes_cum);
      case "tx":
        return computeRate(tr.ts, tr.tx_bytes_cum);
      default:
        return [];
    }
  }

  function renderCharts(payload) {
    lastPayload = payload;
    // Reset stored instances on each render so a re-fetch fallback (theme
    // change → recreate path) doesn't leak the old ones.
    while (charts.length) charts.pop();

    const canvases = document.querySelectorAll("canvas[data-chart]");
    const sysEmpty = !payload.system || payload.system.ts.length === 0;
    const trafficEmpty = !payload.traffic || payload.traffic.ts.length === 0;
    if (sysEmpty && trafficEmpty) {
      canvases.forEach((c) => insertNotice(c, "empty", "No data yet — collecting…"));
      return;
    }

    const colors = themeColors();
    canvases.forEach((canvas) => {
      const series = canvas.dataset.chart;
      const data = datasetFor(series, payload);
      const chart = new Chart(canvas, {
        type: "line",
        data: {
          datasets: [
            {
              label: LABELS[series],
              data: data,
              borderColor: PALETTE[series],
              tension: 0.2,
              pointRadius: 0,
            },
          ],
        },
        options: {
          scales: {
            x: {
              type: "time",
              time: { unit: "hour" },
              grid: { color: colors.grid },
              ticks: { color: colors.text },
              border: { color: colors.grid },
            },
            y: {
              beginAtZero: true,
              grid: { color: colors.grid },
              ticks: { color: colors.text },
              border: { color: colors.grid },
            },
          },
          plugins: { legend: { display: false } },
          animation: false,
        },
      });
      charts.push(chart);
    });
  }

  // applyThemeToCharts re-reads the active theme tokens and patches each
  // chart's axis colors in place. Chart.js v4 update('none') skips the
  // animation pass, which is what we want for a color swap.
  function applyThemeToCharts() {
    if (charts.length === 0 && clientCharts.size === 0) return;
    const colors = themeColors();
    const all = [...charts, ...clientCharts.values()];
    try {
      all.forEach((chart) => {
        const sx = chart.options.scales.x;
        const sy = chart.options.scales.y;
        sx.grid.color = colors.grid;
        sy.grid.color = colors.grid;
        sx.ticks.color = colors.text;
        sy.ticks.color = colors.text;
        sx.border.color = colors.grid;
        sy.border.color = colors.grid;
        chart.update("none");
      });
    } catch (err) {
      // Defensive: Chart.js update() doesn't throw on color changes today,
      // but the spec asks for a recreate fallback. Easiest correct path is
      // to re-render from the last payload (or re-fetch if we never got one).
      console.warn("charts.js: update() failed, recreating charts", err);
      charts.forEach((c) => {
        try { c.destroy(); } catch (e) { /* ignore */ }
      });
      while (charts.length) charts.pop();
      if (lastPayload) {
        renderCharts(lastPayload);
      } else {
        fetch("/api/metrics?range=24h")
          .then((resp) => resp.json())
          .then(renderCharts)
          .catch((e) => console.error("charts.js: recreate refetch failed", e));
      }
    }
  }

  // initClientChart fetches per-client rx/tx rate series and renders a
  // two-dataset line chart into the canvas embedded in the expanded
  // detail row. Re-entrant: a prior instance for the same pubkey is
  // destroyed first so reopening the row doesn't leak Chart.js state.
  function initClientChart(pubkey, range) {
    const canvas = document.getElementById("client-chart-" + pubkey);
    if (!canvas) return;
    const prior = clientCharts.get(pubkey);
    if (prior) {
      try { prior.destroy(); } catch (e) { /* already destroyed */ }
      clientCharts.delete(pubkey);
    }
    fetch("/api/metrics/client/" + encodeURIComponent(pubkey) + "?range=" + encodeURIComponent(range))
      .then((resp) => {
        if (!resp.ok) throw new Error("HTTP " + resp.status);
        return resp.json();
      })
      .then((payload) => {
        if (!payload.ts || payload.ts.length === 0) {
          insertNotice(canvas, "empty", "No data yet — collecting…");
          return;
        }
        const colors = themeColors();
        const rxData = payload.ts.map((t, i) => ({ x: t, y: payload.rx_rate_bps[i] }));
        const txData = payload.ts.map((t, i) => ({ x: t, y: payload.tx_rate_bps[i] }));
        const chart = new Chart(canvas, {
          type: "line",
          data: {
            datasets: [
              {
                label: "Rx B/s",
                data: rxData,
                borderColor: PALETTE.rx,
                tension: 0.2,
                pointRadius: 0,
              },
              {
                label: "Tx B/s",
                data: txData,
                borderColor: PALETTE.tx,
                tension: 0.2,
                pointRadius: 0,
              },
            ],
          },
          options: {
            scales: {
              x: {
                type: "time",
                time: { unit: "hour" },
                grid: { color: colors.grid },
                ticks: { color: colors.text },
                border: { color: colors.grid },
              },
              y: {
                beginAtZero: true,
                grid: { color: colors.grid },
                ticks: { color: colors.text },
                border: { color: colors.grid },
              },
            },
            plugins: { legend: { display: true } },
            animation: false,
          },
        });
        clientCharts.set(pubkey, chart);
      })
      .catch((err) => {
        console.error("charts.js: failed to load per-client metrics", err);
        insertNotice(canvas, "error", "Failed to load chart data");
      });
  }

  // The detail fragment is htmx-swapped into the placeholder row when a
  // client row is clicked. Match on requestConfig.path so we don't react
  // to unrelated htmx swaps (e.g. tab partials, the 10s metrics refresh).
  document.body.addEventListener("htmx:afterSwap", function (event) {
    const path = (event.detail && event.detail.requestConfig && event.detail.requestConfig.path) || "";
    const m = path.match(/^\/partial\/clients\/([^/]+)\/detail$/);
    if (!m) return;
    const pubkey = decodeURIComponent(m[1]);
    const canvas = document.getElementById("client-chart-" + pubkey);
    if (!canvas) return;
    const range = canvas.dataset.range || "24h";
    initClientChart(pubkey, range);
  });

  window.addEventListener("__themeChanged", applyThemeToCharts);

  fetch("/api/metrics?range=24h")
    .then((resp) => {
      if (!resp.ok) throw new Error("HTTP " + resp.status);
      return resp.json();
    })
    .then(renderCharts)
    .catch((err) => {
      console.error("charts.js: failed to load /api/metrics", err);
      document
        .querySelectorAll("canvas[data-chart]")
        .forEach((c) => insertNotice(c, "error", "Failed to load metrics"));
    });
})();
