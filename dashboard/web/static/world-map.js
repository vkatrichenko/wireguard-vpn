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

  // ----- Zoom / pan transform model (spec 010 Slice 2) --------------------

  // The basemap + markers sit inside a .geo-canvas that we translate+scale; a
  // clipping .geo-viewport hides the overflow. zoom is bounded to [1, 8]: 1 is
  // the fit-the-viewport baseline (reset), 8 keeps the equirectangular outline
  // legible without turning into mush. tx/ty are pixel translates in viewport
  // space applied BEFORE scale (transform-origin: 0 0).
  var MIN_ZOOM = 1;
  var MAX_ZOOM = 8;
  var ZOOM_STEP = 1.5; // multiplicative per +/- click
  // Gentler multiplicative step for the wheel: a notch is far finer-grained than
  // a button press, so a 1.5x jump per tick overshoots. ~1.15 gives smooth
  // zoom-to-cursor without feeling sluggish (spec 010 Slice 3).
  var ZOOM_STEP_WHEEL = 1.15;

  // view is module-level (NOT per-render) so the 10s /api/geo tick — which only
  // replaces .geo-markers children — never resets the user's zoom/pan.
  var view = { z: 1, tx: 0, ty: 0 };

  // clampZoom bounds a zoom factor to [MIN_ZOOM, MAX_ZOOM]. Pure.
  function clampZoom(z) {
    if (z < MIN_ZOOM) return MIN_ZOOM;
    if (z > MAX_ZOOM) return MAX_ZOOM;
    return z;
  }

  // clampPan bounds a translate (tx, ty) so the scaled canvas always covers the
  // viewport — the map can never be dragged off-screen leaving a blank gutter.
  // The canvas is vw*z wide; with transform-origin 0 0 the visible window is
  // [-tx, -tx+vw]/z, so tx must lie in [vw - vw*z, 0] = [vw*(1-z), 0]. At z=1
  // both bounds collapse to 0 (no pan possible). Pure: no DOM, no view mutation.
  function clampPan(tx, ty, z, vw, vh) {
    var minX = vw * (1 - z);
    var minY = vh * (1 - z);
    return {
      tx: tx > 0 ? 0 : tx < minX ? minX : tx,
      ty: ty > 0 ? 0 : ty < minY ? minY : ty,
    };
  }

  // zoomAt computes the new (tx, ty) that keeps the viewport-space anchor
  // (cx, cy) fixed under the cursor/finger/midpoint when zoom changes oldZoom ->
  // newZoom. With transform-origin 0 0 the identity is
  // tx' = cx - (cx - tx) * (newZoom/oldZoom) (same for ty). Side-effect-free and
  // NOT pan-clamped — callers run the result through clampPan with the live
  // viewport size. Shared by zoomAbout (buttons), wheel, and pinch (Slice 3).
  function zoomAt(cx, cy, oldZoom, newZoom, tx, ty) {
    var ratio = newZoom / oldZoom;
    return {
      tx: cx - (cx - tx) * ratio,
      ty: cy - (cy - ty) * ratio,
    };
  }

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

    // applyView writes the current `view` onto the DOM: the canvas gets the
    // translate+scale transform, and the --geo-z custom property drives the
    // marker counter-scale (CSS: .geo-marker scales by 1/var(--geo-z)), so dots
    // stay a constant on-screen size at any zoom with no per-marker JS loop.
    // Markers re-rendered by the 10s tick inherit --geo-z automatically since it
    // lives on the canvas. Also refreshes the +/-/reset disabled states.
    function applyView(card) {
      card = card || document.getElementById("geo-map");
      if (!card) return;
      var canvas = card.querySelector(".geo-canvas");
      if (!canvas) return;
      canvas.style.transform =
        "translate(" + view.tx + "px, " + view.ty + "px) scale(" + view.z + ")";
      canvas.style.transformOrigin = "0 0";
      canvas.style.setProperty("--geo-z", String(view.z));
      // Toggle .is-zoomed on the viewport so CSS can flip touch-action (pan-y at
      // fit -> none when zoomed) and the grab cursor. At z=1 the map must yield
      // touch-drag to page scroll; zoomed in it captures pinch/drag (Slice 3).
      var vp = card.querySelector(".geo-viewport");
      if (vp) vp.classList.toggle("is-zoomed", view.z > MIN_ZOOM + 1e-9);
      syncZoomButtons(card);
    }

    function syncZoomButtons(card) {
      var atMin = view.z <= MIN_ZOOM + 1e-9;
      var atMax = view.z >= MAX_ZOOM - 1e-9;
      var inBtn = card.querySelector('[data-geo-zoom="in"]');
      var outBtn = card.querySelector('[data-geo-zoom="out"]');
      var resetBtn = card.querySelector('[data-geo-zoom="reset"]');
      if (inBtn) inBtn.disabled = atMax;
      if (outBtn) outBtn.disabled = atMin;
      if (resetBtn) resetBtn.disabled = atMin;
    }

    function viewportSize(card) {
      var vp = card.querySelector(".geo-viewport");
      if (!vp) return { w: 0, h: 0 };
      var r = vp.getBoundingClientRect();
      return { w: r.width, h: r.height };
    }

    // zoomAbout scales toward the given viewport-space anchor (vx, vy): the
    // point under the anchor stays fixed. Delegates the anchor math to the pure
    // zoomAt helper, then clamps so the canvas keeps covering the viewport.
    function zoomAbout(card, nextZ, vx, vy) {
      var size = viewportSize(card);
      var z0 = view.z;
      var z1 = clampZoom(nextZ);
      if (z1 === z0) {
        applyView(card);
        return;
      }
      var anchored = zoomAt(vx, vy, z0, z1, view.tx, view.ty);
      var clamped = clampPan(anchored.tx, anchored.ty, z1, size.w, size.h);
      view.z = z1;
      view.tx = clamped.tx;
      view.ty = clamped.ty;
      applyView(card);
    }

    function resetView(card) {
      view.z = 1;
      view.tx = 0;
      view.ty = 0;
      applyView(card);
    }

    function wireZoomControls(card) {
      var zoom = card.querySelector(".geo-zoom");
      if (!zoom || zoom.__wired) return;
      zoom.__wired = true;
      zoom.addEventListener("click", function (ev) {
        var btn = ev.target.closest("[data-geo-zoom]");
        if (!btn || btn.disabled) return;
        ev.stopPropagation();
        var size = viewportSize(card);
        var cx = size.w / 2;
        var cy = size.h / 2;
        var action = btn.getAttribute("data-geo-zoom");
        if (action === "in") zoomAbout(card, view.z * ZOOM_STEP, cx, cy);
        else if (action === "out") zoomAbout(card, view.z / ZOOM_STEP, cx, cy);
        else if (action === "reset") resetView(card);
      });
    }

    // wireGestures layers wheel / drag-pan / pinch-zoom onto the .geo-viewport
    // using Pointer Events. Idempotent via the __gesturesWired guard so an htmx
    // re-mount never double-binds. All math routes through zoomAt + clampPan, so
    // gestures share the button controls' transform model exactly.
    function wireGestures(card) {
      var vp = card.querySelector(".geo-viewport");
      if (!vp || vp.__gesturesWired) return;
      vp.__gesturesWired = true;

      // Active pointers by pointerId: {x, y} in viewport-local pixels. One entry
      // = drag-pan; two = pinch. A separate `pinch` snapshot holds the gesture's
      // initial distance / zoom / start-translate so we scale relative to the
      // gesture origin (not per-move-frame, which would drift).
      var pointers = new Map();
      var drag = null; // { id, lastX, lastY } while a single-pointer pan is live
      var pinch = null; // { startDist, startZoom, startTx, startTy } during pinch

      function localXY(ev) {
        var r = vp.getBoundingClientRect();
        return { x: ev.clientX - r.left, y: ev.clientY - r.top };
      }

      function twoPointers() {
        var it = pointers.values();
        return [it.next().value, it.next().value];
      }

      function dist(a, b) {
        var dx = a.x - b.x;
        var dy = a.y - b.y;
        return Math.sqrt(dx * dx + dy * dy);
      }

      vp.addEventListener("pointerdown", function (ev) {
        // Ignore the controls (they have their own handlers) and right-click.
        if (ev.button && ev.button !== 0) return;
        if (ev.target.closest(".geo-zoom")) return;
        var p = localXY(ev);
        pointers.set(ev.pointerId, p);

        if (pointers.size === 2) {
          // Second finger down: switch from pan to pinch. Snapshot the origin.
          drag = null;
          var two = twoPointers();
          pinch = {
            startDist: dist(two[0], two[1]) || 1,
            startZoom: view.z,
            startTx: view.tx,
            startTy: view.ty,
          };
          try { vp.setPointerCapture(ev.pointerId); } catch (e) {}
        } else if (pointers.size === 1 && view.z > MIN_ZOOM + 1e-9) {
          // Single pointer + zoomed in => start a drag-pan. At fit (z=1) we do
          // NOT capture, so a touch-drag bubbles to page scroll.
          drag = { id: ev.pointerId, lastX: p.x, lastY: p.y };
          try { vp.setPointerCapture(ev.pointerId); } catch (e) {}
        }
      });

      vp.addEventListener("pointermove", function (ev) {
        if (!pointers.has(ev.pointerId)) return;
        var p = localXY(ev);
        pointers.set(ev.pointerId, p);
        var size = viewportSize(card);

        if (pinch && pointers.size >= 2) {
          var two = twoPointers();
          var curDist = dist(two[0], two[1]) || 1;
          var mx = (two[0].x + two[1].x) / 2;
          var my = (two[0].y + two[1].y) / 2;
          var z1 = clampZoom(pinch.startZoom * (curDist / pinch.startDist));
          var anchored = zoomAt(mx, my, pinch.startZoom, z1, pinch.startTx, pinch.startTy);
          var c = clampPan(anchored.tx, anchored.ty, z1, size.w, size.h);
          view.z = z1;
          view.tx = c.tx;
          view.ty = c.ty;
          applyView(card);
        } else if (drag && drag.id === ev.pointerId) {
          var nx = view.tx + (p.x - drag.lastX);
          var ny = view.ty + (p.y - drag.lastY);
          drag.lastX = p.x;
          drag.lastY = p.y;
          var cp = clampPan(nx, ny, view.z, size.w, size.h);
          view.tx = cp.tx;
          view.ty = cp.ty;
          applyView(card);
        }
      });

      function endPointer(ev) {
        pointers.delete(ev.pointerId);
        try { vp.releasePointerCapture(ev.pointerId); } catch (e) {}
        if (drag && drag.id === ev.pointerId) drag = null;
        if (pointers.size < 2) pinch = null;
        // A surviving single pointer after a pinch ends could resume a pan, but
        // re-seeding from a stale lastX/lastY would jump; require a fresh down.
      }
      vp.addEventListener("pointerup", endPointer);
      vp.addEventListener("pointercancel", endPointer);

      // Wheel: zoom-to-cursor with a gentle step. preventDefault lives ONLY here
      // (non-passive listener) so the wheel over the map zooms instead of
      // scrolling the page, while every other surface scrolls normally.
      vp.addEventListener(
        "wheel",
        function (ev) {
          ev.preventDefault();
          var size = viewportSize(card);
          var p = localXY(ev);
          var factor = ev.deltaY < 0 ? ZOOM_STEP_WHEEL : 1 / ZOOM_STEP_WHEEL;
          zoomAbout(card, view.z * factor, p.x, p.y);
        },
        { passive: false }
      );
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
      // backgroundColor (longhand), NOT the `background` shorthand: the
      // shorthand resets background-clip to border-box, defeating the
      // stylesheet's `background-clip: content-box` and bleeding the fill
      // across the full 44px touch box instead of the 14px dot.
      marker.style.backgroundColor = online ? colors.online : colors.offline;
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
          // Derive the empty-state from what is actually on the map (the
          // overlay's live children) rather than the cluster count, so the
          // "No mappable peers." text can never coexist with a rendered
          // marker. renderMarkers clears the overlay first, so children.length
          // is exactly the marker count for this tick.
          if (emptyEl) emptyEl.hidden = overlay.children.length > 0;
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

    // mountCard wires the zoom controls and paints the current view onto a
    // freshly-mounted card. Idempotent: wireZoomControls guards against double-
    // binding, and applyView re-projects the persisted module-level `view` so a
    // tab re-mount restores the user's last zoom/pan rather than snapping to
    // fit. Called on cold load and after every htmx swap that brings the card in.
    function mountCard() {
      var card = document.getElementById("geo-map");
      if (!card) return;
      wireZoomControls(card);
      wireGestures(card);
      applyView(card);
    }

    var timer = null;
    function startTicking() {
      if (timer) return;
      mountCard();
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
        mountCard();
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
    module.exports = { project: project, projectPercent: projectPercent, clusterPeers: clusterPeers, clusterKey: clusterKey, MAP_W: MAP_W, MAP_H: MAP_H, clampZoom: clampZoom, clampPan: clampPan, zoomAt: zoomAt, MIN_ZOOM: MIN_ZOOM, MAX_ZOOM: MAX_ZOOM };
  }
})();
