// charts.js — fetches per-range trend series from /api/metrics/system and
// /api/metrics/traffic and renders four trend charts (CPU%, Memory%, Rx rate,
// Tx rate) using Chart.js with the date-fns adapter. Single IIFE, no module
// system, no bundler. Loaded with `defer` from dashboard.html so DOM is parsed
// before this runs.
//
// Each canvas element carries a `data-range` attribute rendered server-side
// from the partial endpoint's validated `?range=`. The range selector in the
// page header is an htmx-driven form: on <select> change htmx swaps #tab-body
// with the new partial, the new canvases land with the new data-range, and
// loadAndRender() re-fetches the matching window. The spec wording says
// "chart.update()" — that's the right shape when only the dataset changes,
// but the canvas DOM nodes themselves are new after an htmx swap, so the
// only correct path is destroy-the-old-instances + recreate-on-new-canvases.
//
// Empty arrays render a "No data yet — collecting…" message under each card
// heading; the poller writes the first row at startup so this state is only
// visible during the first ~30s of process lifetime. Fetch failures render a
// red error message under each card heading and skip Chart construction.
// /system and /traffic are fetched independently so a 500 on one endpoint
// still lets the other pair of charts render — we don't want a transient
// /system error to also blank out the rx/tx charts.
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

  // SYSTEM_SERIES / TRAFFIC_SERIES route a canvas's data-chart attribute onto
  // the endpoint that supplies its data. Used by loadAndRender to decide
  // which fetches are actually needed for the current DOM.
  const SYSTEM_SERIES = new Set(["cpu", "memory"]);
  const TRAFFIC_SERIES = new Set(["rx", "tx"]);

  // charts holds the Chart instances built by renderCharts so the
  // __themeChanged handler can iterate and patch colors in place, and so
  // loadAndRender can destroy the old instances before recreating.
  const charts = [];
  // clientCharts holds per-row expand charts keyed by pubkey so reopening
  // the same row tears down the prior instance cleanly.
  const clientCharts = new Map();
  // lastPayload retains the combined {system, traffic} shape so the
  // __themeChanged recreate-fallback can re-render without a network round
  // trip. Kept as a combined payload (not split) so datasetFor stays
  // unchanged from its pre-split form.
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
  // built from the combined synthetic payload. Returns [] when the relevant
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

  // destroyCharts tears down every tracked global chart instance. Called
  // before each loadAndRender re-render and inside the theme recreate
  // fallback. try/catch because Chart.destroy can throw if the canvas was
  // already detached (htmx swap) — we don't care, we're throwing them out.
  function destroyCharts() {
    charts.forEach((c) => {
      try { c.destroy(); } catch (e) { /* already destroyed */ }
    });
    charts.length = 0;
  }

  // buildChart constructs a single Chart.js line chart on the given canvas
  // and pushes the instance into charts[] for later teardown / theme patch.
  // Extracted from renderCharts so the per-canvas options block stays in
  // one place — datasetFor returning [] still produces a chart so the
  // axes render; the empty-state notice is inserted by renderCharts.
  function buildChart(canvas, series, data, colors) {
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
  }

  // renderCharts iterates canvas[data-chart] elements and builds Chart.js
  // instances against the combined synthetic payload. Used by the theme
  // recreate-fallback (which already has lastPayload in hand) so the
  // dataset-construction path stays in one place. loadAndRender calls this
  // after assembling its synthetic payload from the per-endpoint fetches.
  function renderCharts(payload) {
    lastPayload = payload;
    destroyCharts();

    const canvases = document.querySelectorAll("canvas[data-chart]");
    if (canvases.length === 0) return;

    const sysEmpty = !payload.system || !payload.system.ts || payload.system.ts.length === 0;
    const trafficEmpty = !payload.traffic || !payload.traffic.ts || payload.traffic.ts.length === 0;
    const colors = themeColors();

    canvases.forEach((canvas) => {
      const series = canvas.dataset.chart;
      const needsSystem = SYSTEM_SERIES.has(series);
      const needsTraffic = TRAFFIC_SERIES.has(series);
      if ((needsSystem && sysEmpty) || (needsTraffic && trafficEmpty)) {
        insertNotice(canvas, "empty", "No data yet — collecting…");
        return;
      }
      const data = datasetFor(series, payload);
      buildChart(canvas, series, data, colors);
    });
  }

  // loadAndRender groups the canvases currently in the DOM by their
  // data-range attribute and issues at most one /system + one /traffic
  // fetch per unique range value. Each fetch is independent — a failure on
  // /system renders an error notice on cpu/memory canvases only, leaving
  // /traffic to render rx/tx normally (and vice versa). This is the on-
  // page-load entrypoint and the on-tab-body-swap (= on-range-change)
  // entrypoint; the canvases' data-range attrs are the source of truth.
  function loadAndRender() {
    const canvases = Array.from(document.querySelectorAll("canvas[data-chart]"));
    if (canvases.length === 0) {
      // Nothing to render on this tab. Keep lastPayload as-is so a later
      // tab swap that brings canvases in can fall through to a fresh fetch.
      destroyCharts();
      return;
    }

    // Group canvases by (range, endpoint). In practice all charts in a tab
    // share one range today, but the grouping is cheap and future-proofs
    // mixed-range tabs (e.g. an overview).
    const sysByRange = new Map(); // range -> canvas[]
    const trafByRange = new Map();
    canvases.forEach((canvas) => {
      const series = canvas.dataset.chart;
      const range = canvas.dataset.range || "24h";
      if (SYSTEM_SERIES.has(series)) {
        if (!sysByRange.has(range)) sysByRange.set(range, []);
        sysByRange.get(range).push(canvas);
      } else if (TRAFFIC_SERIES.has(series)) {
        if (!trafByRange.has(range)) trafByRange.set(range, []);
        trafByRange.get(range).push(canvas);
      }
    });

    // Tear down old instances before any new fetch resolves — otherwise
    // a slow fetch lets stale charts linger on top of new canvases.
    destroyCharts();

    // Track payloads keyed by range so the render pass can pick the right
    // window for each canvas. system[range] / traffic[range] are absent on
    // fetch failure; the render pass uses that as the error signal.
    const systemPayloads = new Map();
    const trafficPayloads = new Map();
    const systemErrors = new Set(); // ranges where /system failed
    const trafficErrors = new Set();

    const fetches = [];
    sysByRange.forEach((_canvases, range) => {
      const url = "/api/metrics/system?range=" + encodeURIComponent(range);
      fetches.push(
        fetch(url)
          .then((resp) => {
            if (!resp.ok) throw new Error("HTTP " + resp.status);
            return resp.json();
          })
          .then((payload) => { systemPayloads.set(range, payload); })
          .catch((err) => {
            console.error("charts.js: failed to load " + url, err);
            systemErrors.add(range);
          }),
      );
    });
    trafByRange.forEach((_canvases, range) => {
      const url = "/api/metrics/traffic?range=" + encodeURIComponent(range);
      fetches.push(
        fetch(url)
          .then((resp) => {
            if (!resp.ok) throw new Error("HTTP " + resp.status);
            return resp.json();
          })
          .then((payload) => { trafficPayloads.set(range, payload); })
          .catch((err) => {
            console.error("charts.js: failed to load " + url, err);
            trafficErrors.add(range);
          }),
      );
    });

    Promise.all(fetches).then(() => {
      // Stash a combined synthetic payload keyed off the first range we
      // saw — the theme recreate-fallback only needs *some* recent shape,
      // and re-deriving it via renderCharts(lastPayload) is fine for a
      // single-range tab. Cross-range tabs would land on the last range
      // iterated; acceptable for the rare theme-toggle fallback path.
      const firstSysRange = sysByRange.keys().next().value;
      const firstTrafRange = trafByRange.keys().next().value;
      lastPayload = {
        system: firstSysRange ? systemPayloads.get(firstSysRange) || null : null,
        traffic: firstTrafRange ? trafficPayloads.get(firstTrafRange) || null : null,
      };

      const colors = themeColors();
      canvases.forEach((canvas) => {
        const series = canvas.dataset.chart;
        const range = canvas.dataset.range || "24h";

        if (SYSTEM_SERIES.has(series)) {
          if (systemErrors.has(range)) {
            insertNotice(canvas, "error", "Failed to load metrics");
            return;
          }
          const payload = systemPayloads.get(range);
          if (!payload || !payload.ts || payload.ts.length === 0) {
            insertNotice(canvas, "empty", "No data yet — collecting…");
            return;
          }
          const data = datasetFor(series, { system: payload, traffic: null });
          buildChart(canvas, series, data, colors);
          return;
        }

        if (TRAFFIC_SERIES.has(series)) {
          if (trafficErrors.has(range)) {
            insertNotice(canvas, "error", "Failed to load metrics");
            return;
          }
          const payload = trafficPayloads.get(range);
          if (!payload || !payload.ts || payload.ts.length === 0) {
            insertNotice(canvas, "empty", "No data yet — collecting…");
            return;
          }
          const data = datasetFor(series, { system: null, traffic: payload });
          buildChart(canvas, series, data, colors);
          return;
        }
      });
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
      // but the spec asks for a recreate fallback. If we have a cached
      // payload, re-render against it; otherwise re-fetch via the same
      // entrypoint as the page-load path so /system + /traffic stay split.
      console.warn("charts.js: update() failed, recreating charts", err);
      if (lastPayload) {
        renderCharts(lastPayload);
      } else {
        loadAndRender();
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
    const target = event.detail && event.detail.target;

    // Per-client detail expand — existing behavior.
    const path = (event.detail && event.detail.requestConfig && event.detail.requestConfig.path) || "";
    const m = path.match(/^\/partial\/clients\/([^/]+)\/detail$/);
    if (m) {
      const pubkey = decodeURIComponent(m[1]);
      const canvas = document.getElementById("client-chart-" + pubkey);
      if (canvas) {
        const range = canvas.dataset.range || "24h";
        initClientChart(pubkey, range);
      }
      return;
    }

    // Tab-body swap — this is the on-<select>-change path. The range-
    // selector form fires hx-get="/partial/system" (or /network) which
    // swaps #tab-body with HTML containing canvases whose new data-range
    // attribute matches the user's selection. The canvas DOM nodes
    // themselves are new — Chart.update() wouldn't help here, so we
    // destroy the old instances and re-fetch the matching window via
    // loadAndRender. This is also the path used for a tab switch where
    // the prior render had a different set of canvases entirely.
    if (target && target.id === "tab-body") {
      loadAndRender();
    }
  });

  window.addEventListener("__themeChanged", applyThemeToCharts);

  // Initial page-load render. loadAndRender reads each canvas's data-range
  // and fetches the matching /system + /traffic windows.
  loadAndRender();
})();
