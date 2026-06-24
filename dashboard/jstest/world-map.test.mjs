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
