// Node unit test for the pure equirectangular projection (and the co-location
// clustering helper) exported by web/static/world-map.js. This file lives
// OUTSIDE web/ on purpose: everything under dashboard/web/ is compiled into the
// binary by `//go:embed all:web`, and shipping test code in the production
// artifact is unacceptable. It require()s the module across the directory
// boundary; the module's DOM wiring is guarded behind `typeof document` so
// loading it under node executes only the pure helpers + the module.exports.
//
// Run: node --test  (from dashboard/jstest/, or `node --test dashboard/jstest`)

import { test } from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const wm = require(join(here, "..", "web", "static", "world-map.js"));

const TOL = 1e-6;

// near asserts |a-b| <= TOL so a clean linear projection isn't tripped by FP
// noise. Corners and the origin land exactly; the tolerance covers the
// fractional cases.
function near(a, b, msg) {
  assert.ok(Math.abs(a - b) <= TOL, `${msg}: ${a} !~= ${b}`);
}

test("origin (0,0) projects to the map center", () => {
  const p = wm.project(0, 0);
  near(p.x, wm.MAP_W / 2, "center x");
  near(p.y, wm.MAP_H / 2, "center y");
});

test("four corners project to the viewBox corners", () => {
  // top-left = (lon -180, lat +90)
  let p = wm.project(-180, 90);
  near(p.x, 0, "TL x");
  near(p.y, 0, "TL y");

  // top-right = (lon +180, lat +90)
  p = wm.project(180, 90);
  near(p.x, wm.MAP_W, "TR x");
  near(p.y, 0, "TR y");

  // bottom-left = (lon -180, lat -90)
  p = wm.project(-180, -90);
  near(p.x, 0, "BL x");
  near(p.y, wm.MAP_H, "BL y");

  // bottom-right = (lon +180, lat -90)
  p = wm.project(180, -90);
  near(p.x, wm.MAP_W, "BR x");
  near(p.y, wm.MAP_H, "BR y");
});

test("negative coordinates fall in the lower-left quadrant", () => {
  // (lon -90, lat -45): a quarter-globe west and a quarter-globe south.
  const p = wm.project(-90, -45);
  near(p.x, wm.MAP_W * 0.25, "x");
  near(p.y, wm.MAP_H * 0.75, "y");
});

test("a few real cities land at plausible viewBox positions", () => {
  // London ~ (51.5, -0.13): just west of center, upper-middle band.
  const lon = wm.project(-0.13, 51.5);
  near(lon.x, ((-0.13 + 180) / 360) * wm.MAP_W, "London x");
  near(lon.y, ((90 - 51.5) / 180) * wm.MAP_H, "London y");

  // Sydney ~ (-33.9, 151.2): far east, southern hemisphere (y > center).
  const syd = wm.project(151.2, -33.9);
  assert.ok(syd.x > wm.MAP_W * 0.9, "Sydney east of 90% width");
  assert.ok(syd.y > wm.MAP_H / 2, "Sydney south of equator");
});

test("projectPercent matches project scaled to viewBox percent", () => {
  const pct = wm.projectPercent(0, 0);
  near(pct.left, 50, "center left %");
  near(pct.top, 50, "center top %");

  const tl = wm.projectPercent(-180, 90);
  near(tl.left, 0, "TL left %");
  near(tl.top, 0, "TL top %");
});

test("co-located peers cluster to one group with an online flag", () => {
  const peers = [
    { name: "a", lat: 51.5, lon: -0.1, online: true },
    { name: "b", lat: 51.53, lon: -0.12, online: false }, // rounds to 51.5,-0.1
    { name: "c", lat: -33.9, lon: 151.2, online: false }, // distinct city
  ];
  const clusters = wm.clusterPeers(peers);
  assert.equal(clusters.length, 2, "two distinct clusters");
  const london = clusters.find((g) => g.peers.length === 2);
  assert.ok(london, "London peers grouped");
  assert.equal(london.online, true, "cluster online if any member online");
});

test("clusterPeers tolerates a null/empty peers array", () => {
  assert.deepEqual(wm.clusterPeers(null), []);
  assert.deepEqual(wm.clusterPeers([]), []);
});

// ----- spec 010 Slice 2: zoom / pan clamps --------------------------------

test("clampZoom bounds to [MIN_ZOOM, MAX_ZOOM]", () => {
  assert.equal(wm.clampZoom(0.1), wm.MIN_ZOOM, "below min clamps up");
  assert.equal(wm.clampZoom(100), wm.MAX_ZOOM, "above max clamps down");
  assert.equal(wm.clampZoom(1), 1, "MIN passes through");
  assert.equal(wm.clampZoom(8), 8, "MAX passes through");
  assert.equal(wm.clampZoom(3.5), 3.5, "in-range passes through");
});

test("clampPan pins translate to 0 at z=1 (no pan possible)", () => {
  // At z=1 the canvas exactly fills the viewport, so any non-zero translate
  // would expose a blank gutter; both bounds collapse to 0.
  const p = wm.clampPan(50, -30, 1, 800, 400);
  assert.equal(p.tx, 0, "tx clamped to 0 at z=1");
  assert.equal(p.ty, 0, "ty clamped to 0 at z=1");
});

test("clampPan keeps the scaled canvas covering the viewport", () => {
  const vw = 800;
  const vh = 400;
  const z = 2;
  // Valid range is [vw*(1-z), 0] = [-800, 0] for x, [-400, 0] for y.
  const minX = vw * (1 - z); // -800
  const minY = vh * (1 - z); // -400

  // A positive translate (canvas slid right, exposing left gutter) clamps to 0.
  let p = wm.clampPan(120, 90, z, vw, vh);
  assert.equal(p.tx, 0, "positive tx clamps to 0");
  assert.equal(p.ty, 0, "positive ty clamps to 0");

  // An over-negative translate (right/bottom gutter) clamps to the min bound.
  p = wm.clampPan(-5000, -5000, z, vw, vh);
  assert.equal(p.tx, minX, "tx clamps to vw*(1-z)");
  assert.equal(p.ty, minY, "ty clamps to vh*(1-z)");

  // A translate already inside the valid window passes through untouched.
  p = wm.clampPan(-300, -150, z, vw, vh);
  assert.equal(p.tx, -300, "in-range tx unchanged");
  assert.equal(p.ty, -150, "in-range ty unchanged");
});

// ----- spec 010 Slice 3: cursor/midpoint-anchored zoom math ----------------

// The canvas point under viewport coord (cx, cy) is (cx - tx)/z in canvas space
// (transform-origin 0 0). After zoomAt, the SAME canvas point must map back to
// (cx, cy) on screen — i.e. the anchor stays fixed under the cursor/finger.
function screenOfCanvasPoint(canvasPt, z, tx, ty) {
  return { x: canvasPt * z + tx };
}

test("zoomAt keeps the anchor point fixed across a zoom-in step", () => {
  const cx = 300;
  const cy = 150;
  const oldZoom = 2;
  const newZoom = oldZoom * 1.15;
  const tx = -100;
  const ty = -60;

  // Canvas-space coords currently under the anchor.
  const canvasX = (cx - tx) / oldZoom;
  const canvasY = (cy - ty) / oldZoom;

  const out = wm.zoomAt(cx, cy, oldZoom, newZoom, tx, ty);

  // Same canvas point, re-projected at the new zoom + new translate, lands back
  // on the anchor.
  near(screenOfCanvasPoint(canvasX, newZoom, out.tx, out.ty).x, cx, "anchor x fixed");
  near(screenOfCanvasPoint(canvasY, newZoom, out.ty, out.ty).x, cy, "anchor y fixed");
});

test("zoomAt is identity when zoom does not change", () => {
  const out = wm.zoomAt(123, 45, 3, 3, -200, -80);
  near(out.tx, -200, "tx unchanged at equal zoom");
  near(out.ty, -80, "ty unchanged at equal zoom");
});
