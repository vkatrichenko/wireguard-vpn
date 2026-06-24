// world-map.js — renders the offline geo map card on the Clients tab (spec 006
// Slice 4). A single IIFE in the same style as charts.js / client-timeline.js,
// loaded with `defer` from dashboard.html. No dependency on Chart.js: the base
// map is the embedded /static/world.svg (a public-domain Natural Earth 110m
// outline, full-globe equirectangular, viewBox "0 0 2000 1000"), and markers
// are absolutely-positioned <button> overlays sized in PERCENT so they track
// the SVG on any resize / mobile width without re-projecting on each layout.
//
// Data: GET /api/geo (snake_case: {peers:[{name,lat,lon,city,country,online,
// last_seen}], not_mappable:N}). Pulled on a JS-owned 10s tick — the same
// cadence as the rest of the dashboard — rather than an htmx swap, because the
// card shell renders server-side once and only the markers / count / empty-
// state are data-driven. A failed fetch KEEPS the last-rendered markers and
// lets htmx-stale.js own the #stale-pill: this module never fetches via htmx
// and never touches the pill, but a same-origin fetch failure here is benign
// because the htmx metrics swaps on the same page trip the shared pill anyway.
// (We surface our own console warning; we do not invent a second stale UI.)
//
// Theme-aware: online / offline marker colors are read from the --success /
// --text-muted CSS custom properties and re-applied on the window
// __themeChanged event, exactly like charts.js recolors its datasets.
(function () {
  "use strict";

  // ----- Pure helpers (testable under node; no DOM access) ----------------

  // MAP_W / MAP_H are the embedded SVG's viewBox extent. The SVG is a true
  // full-globe plate carrée (lon -180..180 spans x 0..MAP_W, lat 90..-90 spans
  // y 0..MAP_H), so the projection is a direct linear map with NO offset.
  var MAP_W = 2000;
  var MAP_H = 1000;

  // project maps geographic (lon, lat) to SVG-space pixels in the viewBox.
  // Equirectangular: x grows west->east from lon -180, y grows north->south
  // from lat +90. Returned as absolute pixels in the [0..MAP_W]x[0..MAP_H]
  // frame; callers convert to percent for layout-independent positioning.
  function project(lon, lat) {
    return {
      x: ((lon + 180) / 360) * MAP_W,
      y: ((90 - lat) / 180) * MAP_H,
    };
  }

  // projectPercent is project() expressed as viewBox-relative percentages, so
  // a marker positioned with left/top in % stays pinned through any container
  // resize without re-running projection math.
  function projectPercent(lon, lat) {
    var p = project(lon, lat);
    return { left: (p.x / MAP_W) * 100, top: (p.y / MAP_H) * 100 };
  }

  // CLUSTER_DECIMALS controls co-location grouping: peers whose lat AND lon
  // round to the same value at this precision collapse into one marker. 1
  // decimal (~11 km) groups peers that geoip resolves to the same city centroid
  // (the common case — many peers behind one ISP egress share coordinates)
  // while keeping genuinely distinct cities apart. No clustering library.
  var CLUSTER_DECIMALS = 1;

  function clusterKey(lat, lon) {
    return lat.toFixed(CLUSTER_DECIMALS) + "," + lon.toFixed(CLUSTER_DECIMALS);
  }

  // clusterPeers groups an /api/geo peers array by rounded lat/lon. Each group
  // carries the rounded centroid (lat/lon of the first member — they round
  // equal so any member's coords are visually identical), its member peers,
  // and an `online` flag true iff ANY member is online (so a cluster with one
  // live peer reads as live). Order is preserved by first-seen key so the
  // marker set is stable across ticks (no z-order flicker).
  function clusterPeers(peers) {
    var byKey = new Map();
    var order = [];
    (peers || []).forEach(function (p) {
      var key = clusterKey(p.lat, p.lon);
      var group = byKey.get(key);
      if (!group) {
        group = { key: key, lat: p.lat, lon: p.lon, peers: [], online: false };
        byKey.set(key, group);
        order.push(key);
      }
      group.peers.push(p);
      if (p.online) group.online = true;
    });
    return order.map(function (k) { return byKey.get(k); });
  }

  // ----- DOM wiring (browser only) ----------------------------------------

  if (typeof document !== "undefined") {
    var REFRESH_MS = 10000;

    function themeColors() {
      var root = getComputedStyle(document.documentElement);
      return {
        online: root.getPropertyValue("--success").trim() || "#16a34a",
        offline: root.getPropertyValue("--text-muted").trim() || "#6b7280",
      };
    }

    // peerLine formats one co-located peer for the marker tooltip / popover:
    // "name — City, Country · online (5m ago)".
    function peerLine(p) {
      var loc = "";
      if (p.city && p.country) loc = p.city + ", " + p.country;
      else if (p.country) loc = p.country;
      else if (p.city) loc = p.city;
      var state = p.online ? "online" : "offline";
      var seen = p.last_seen ? " (" + p.last_seen + ")" : "";
      return p.name + (loc ? " — " + loc : "") + " · " + state + seen;
    }

    function applyMarkerColor(marker, online, colors) {
      marker.style.background = online ? colors.online : colors.offline;
      marker.style.opacity = online ? "1" : "0.55";
    }

    // renderMarkers replaces the overlay's children with one marker per
    // cluster. Markers are <button>s (keyboard-focusable, ≥44px touch target
    // via CSS) positioned in percent. A count badge appears when a cluster
    // holds more than one peer; the native title carries the full co-located
    // list for hover, and a tap toggles an inline popover for touch.
    function renderMarkers(overlay, clusters, colors) {
      overlay.textContent = "";
      clusters.forEach(function (c) {
        var pos = projectPercent(c.lon, c.lat);
        var marker = document.createElement("button");
        marker.type = "button";
        marker.className = "geo-marker" + (c.online ? " online" : " offline");
        marker.style.left = pos.left + "%";
        marker.style.top = pos.top + "%";
        applyMarkerColor(marker, c.online, colors);

        var lines = c.peers.map(peerLine);
        marker.title = lines.join("\n");
        marker.setAttribute("aria-label", lines.join("; "));

        if (c.peers.length > 1) {
          var badge = document.createElement("span");
          badge.className = "geo-marker-badge";
          badge.textContent = String(c.peers.length);
          marker.appendChild(badge);
        }

        // Touch/keyboard popover: a tap or Enter reveals the co-located list
        // beneath the marker (hover title is mouse-only). Toggle, and close
        // any other open popover first.
        marker.addEventListener("click", function (ev) {
          ev.stopPropagation();
          togglePopover(overlay, marker, lines);
        });
        overlay.appendChild(marker);
      });
    }

    function togglePopover(overlay, marker, lines) {
      var existing = overlay.querySelector(".geo-popover");
      var wasForThis = existing && existing.__owner === marker;
      if (existing) existing.remove();
      if (wasForThis) return;
      var pop = document.createElement("div");
      pop.className = "geo-popover";
      pop.__owner = marker;
      pop.style.left = marker.style.left;
      pop.style.top = marker.style.top;
      lines.forEach(function (l) {
        var row = document.createElement("div");
        row.textContent = l;
        pop.appendChild(row);
      });
      overlay.appendChild(pop);
    }

    function setCaption(captionEl, notMappable) {
      if (!captionEl) return;
      var n = notMappable | 0;
      captionEl.textContent = n === 1 ? "1 not mappable" : n + " not mappable";
      captionEl.hidden = n === 0;
    }

    var lastClusters = null; // retained on fetch failure so markers persist

    function loadAndRender() {
      var card = document.getElementById("geo-map");
      if (!card) return; // Clients tab not mounted
      var overlay = card.querySelector(".geo-markers");
      var emptyEl = card.querySelector(".geo-empty");
      var captionEl = card.querySelector("#geo-not-mappable");
      if (!overlay) return;

      fetch("/api/geo", { headers: { Accept: "application/json" } })
        .then(function (resp) {
          if (!resp.ok) throw new Error("HTTP " + resp.status);
          return resp.json();
        })
        .then(function (data) {
          var clusters = clusterPeers(data.peers);
          lastClusters = clusters;
          var colors = themeColors();
          renderMarkers(overlay, clusters, colors);
          setCaption(captionEl, data.not_mappable);
          if (emptyEl) emptyEl.hidden = clusters.length > 0;
        })
        .catch(function (err) {
          // Keep the last-rendered markers; htmx-stale.js owns the pill.
          console.warn("world-map.js: /api/geo fetch failed, keeping last markers", err);
        });
    }

    function applyTheme() {
      var card = document.getElementById("geo-map");
      if (!card) return;
      var colors = themeColors();
      card.querySelectorAll(".geo-marker").forEach(function (marker) {
        applyMarkerColor(marker, marker.classList.contains("online"), colors);
      });
    }

    var timer = null;
    function startTicking() {
      if (timer) return;
      loadAndRender();
      timer = window.setInterval(loadAndRender, REFRESH_MS);
    }

    // The Clients tab fragment is swapped into #tab-body by htmx (tabs.js). The
    // map card only exists while that fragment is mounted, so (re)start the
    // tick whenever a swap brings #geo-map in, and lean on loadAndRender's
    // own #geo-map guard to no-op once the user navigates away.
    document.body.addEventListener("htmx:afterSwap", function (event) {
      var target = event.detail && event.detail.target;
      if (target && target.id === "tab-body" && document.getElementById("geo-map")) {
        loadAndRender();
      }
    });

    // Dismiss an open popover on an outside click.
    document.body.addEventListener("click", function () {
      var card = document.getElementById("geo-map");
      if (!card) return;
      var pop = card.querySelector(".geo-popover");
      if (pop) pop.remove();
    });

    window.addEventListener("__themeChanged", applyTheme);

    // Cold load: the Clients tab may already be the active fragment (deep link
    // to #clients), in which case #geo-map exists at DOMContentLoaded.
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", startTicking);
    } else {
      startTicking();
    }
  }

  // Export the pure helpers for the node projection test. No-op in the browser
  // (no `module`), so this never leaks globals on the page.
  if (typeof module !== "undefined" && module.exports) {
    module.exports = { project: project, projectPercent: projectPercent, clusterPeers: clusterPeers, clusterKey: clusterKey, MAP_W: MAP_W, MAP_H: MAP_H };
  }
})();
