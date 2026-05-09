// charts.js — fetches /api/metrics?range=24h once on page load and renders
// four trend charts (CPU%, Memory%, Rx rate, Tx rate) using Chart.js with the
// date-fns adapter. Single IIFE, no module system, no bundler. Loaded with
// `defer` from dashboard.html so DOM is parsed before this runs.
//
// Empty arrays render a "No data yet — collecting…" message under each card
// heading; the poller writes the first row at startup so this state is only
// visible during the first ~30s of process lifetime. Fetch failures render a
// red error message under each card heading and skip Chart construction.
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
    const canvases = document.querySelectorAll("canvas[data-chart]");
    const sysEmpty = !payload.system || payload.system.ts.length === 0;
    const trafficEmpty = !payload.traffic || payload.traffic.ts.length === 0;
    if (sysEmpty && trafficEmpty) {
      canvases.forEach((c) => insertNotice(c, "empty", "No data yet — collecting…"));
      return;
    }

    canvases.forEach((canvas) => {
      const series = canvas.dataset.chart;
      const data = datasetFor(series, payload);
      new Chart(canvas, {
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
            x: { type: "time", time: { unit: "hour" } },
            y: { beginAtZero: true },
          },
          plugins: { legend: { display: false } },
          animation: false,
        },
      });
    });
  }

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
