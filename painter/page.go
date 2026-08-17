/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

// pageHTML is the whole page: no stylesheet, no script and no font fetched from
// anywhere.
//
// A plant's network reaches the machines in it and often nothing else, and this
// page is served by a system on that network. A page that needs a library from
// a content delivery network is a page that works at a desk and shows nothing
// in a substation, which is where it is wanted. Everything here is therefore
// plain SVG and plain JavaScript.
//
// Positions are derived from names rather than from a layout run: a system
// lands in the same place on every load, in every browser, and when it comes
// back after a restart it returns to where it was. An operator learning a plant
// should not have to find things afresh each time, and a thing that returns to
// its own place is the difference between fading in and appearing at random.
const pageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Local cloud</title>
<style>
  :root {
    --ink: #1b2733; --dim: #7b8794; --line: #b3c0cc; --paper: #f7f9fb;
    --open: #d0342c; --enrolling: #e08a1e; --identified: #2f8fd6; --authorized: #2e9e5b;
    --unknown: #9aa5b1;
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; height: 100%; background: var(--paper); color: var(--ink);
    font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
  #stage { width: 100%; height: 100%; display: block; cursor: grab; touch-action: none; }
  #stage.dragging { cursor: grabbing; }
  header { position: fixed; top: 0; left: 0; right: 0; padding: 10px 14px;
    display: flex; gap: 16px; align-items: baseline; pointer-events: none; }
  h1 { font-size: 15px; font-weight: 600; margin: 0; }
  .hint { color: var(--dim); font-size: 12px; }
  #legend { position: fixed; bottom: 0; left: 0; padding: 10px 14px; font-size: 12px;
    color: var(--dim); display: flex; gap: 14px; flex-wrap: wrap; align-items: center; }
  .swatch { display: inline-block; width: 10px; height: 10px; border-radius: 50%;
    margin-right: 5px; vertical-align: -1px; }
  .note { color: var(--open); }
  .disk { transition: opacity .8s ease; }
  text { pointer-events: none; user-select: none; }
</style>
</head>
<body>
<header>
  <h1 id="cloudname">local cloud</h1>
  <span class="hint">wheel to zoom &middot; drag to pan &middot; the further in, the more it tells you</span>
  <span class="hint" id="notes"></span>
</header>
<svg id="stage"></svg>
<div id="legend">
  <span><i class="swatch" style="background:var(--authorized)"></i>authorized</span>
  <span><i class="swatch" style="background:var(--identified)"></i>identified</span>
  <span><i class="swatch" style="background:var(--enrolling)"></i>enrolling</span>
  <span><i class="swatch" style="background:var(--open)"></i>open</span>
  <span>&mdash; observes</span>
  <span>&ndash; &ndash; acts</span>
  <span><i class="swatch" style="background:var(--open);opacity:.9"></i>wants a service nothing provides</span>
</div>
<script>
"use strict";
var SVG = "http://www.w3.org/2000/svg";
var stage = document.getElementById("stage");

// The view. scale is what the wheel changes; everything else follows from it.
var view = { x: 0, y: 0, scale: 1 };
var cloud = null;
var seen = new Map();       // id -> when it was first drawn
var arrivals = new Map();   // id -> when it appeared, for the green flash
var departures = new Map(); // id -> { at, x, y, name }, for the red one
var centres = new Map();    // id -> where it was last drawn, so a departure knows where

// How long each announcement lasts. A change in a plant is worth interrupting
// somebody for, and a picture that merely settles into a new arrangement does
// not do that: the eye follows movement and colour, so a system arriving or
// leaving is given both, briefly, and then the picture goes quiet again.
var ARRIVING = 2000;
var LEAVING = 1200;

// animating reports whether anything still needs redrawing between refreshes.
function animating() {
  var now = Date.now(), busy = false;
  arrivals.forEach(function (at, id) {
    if (now - at > ARRIVING) { arrivals.delete(id); } else { busy = true; }
  });
  departures.forEach(function (d, id) {
    if (now - d.at > LEAVING) { departures.delete(id); } else { busy = true; }
  });
  return busy;
}

// hash turns a name into a number, so a thing's place comes from what it is.
// The same cloud therefore draws the same way in every browser and after every
// restart, which is what lets an operator stop hunting for things.
function hash(text) {
  var h = 2166136261;
  for (var i = 0; i < text.length; i++) { h ^= text.charCodeAt(i); h = Math.imul(h, 16777619); }
  return (h >>> 0) / 4294967295;
}

// ringRadius is how far out n things of a given size must sit to not overlap.
// Eleven systems of radius 34 need about 139, where the first version of this
// used a fixed 70 and drew them on top of each other.
function ringRadius(n, size, minimum) {
  if (n < 2) return 0;
  return Math.max(minimum, (size / Math.sin(Math.PI / n)) * 1.15);
}

// place puts the i-th of n things on that ring.
//
// By position in a sorted list rather than by a hash of the name, which was the
// first attempt: a hash gives every system a place of its own and no guarantee
// that two of them are not the same place, and with eleven systems they
// collided badly. Sorted order is still stable — a system that restarts finds
// the same neighbours and the same spot — but a system that joins or leaves does
// shift the others round the ring. That is the price of never overlapping, and
// legibility is worth more than absolute constancy.
//
// The half-turn offset keeps the first system off the label at the top.
function place(i, n, radius) {
  if (n < 2) return { x: 0, y: 0 };
  var angle = (i / n) * Math.PI * 2 - Math.PI / 2;
  return { x: Math.cos(angle) * radius, y: Math.sin(angle) * radius };
}

function colourOf(level) {
  if (level === "authorized") return "var(--authorized)";
  if (level === "identified") return "var(--identified)";
  if (level === "enrolling") return "var(--enrolling)";
  if (level === "open") return "var(--open)";
  return "var(--unknown)";
}

// label writes text at a constant size on screen, whatever the zoom.
//
// Font sizes were in user units, so zooming in magnified the words along with
// the disks until eleven system names covered the cloud. Dividing by the scale
// keeps a label the size it was drawn at, which is the size it is legible at.
function label(text, x, y, size, fill, parent) {
  var node = el("text", { x: x, y: y, "text-anchor": "middle",
                          "font-size": size / view.scale, fill: fill }, parent);
  node.textContent = text;
  return node;
}

function el(name, attrs, parent) {
  var node = document.createElementNS(SVG, name);
  for (var k in attrs) node.setAttribute(k, attrs[k]);
  if (parent) parent.appendChild(node);
  return node;
}

// Semantic zoom: what is worth showing at this magnification. Not the same
// picture larger — a different amount of truth.
function levelOfDetail() {
  if (view.scale < 1.6) return 0;  // the cloud, and its hosts
  if (view.scale < 3.2) return 1;  // the systems on each host
  if (view.scale < 6.5) return 2;  // the unit assets in each system
  return 3;                        // services, labelled
}

var geometry = {}; // asset id -> centre, filled while drawing, used for lines
var extent = 300;  // how far the picture reaches, set while drawing
var framed = false;

// frame sets the zoom so the whole cloud is on screen, once, on the first
// picture. A cloud of three systems and a cloud of forty need very different
// magnifications to be worth looking at, and an operator opening the page
// should not have to find that out with the wheel.
function frame() {
  var w = stage.clientWidth, h = stage.clientHeight;
  if (!w || !h) return;
  view.scale = Math.min(14, Math.max(0.2, (Math.min(w, h) * 0.44) / extent));
  view.x = 0;
  view.y = 0;
}

function draw() {
  stage.innerHTML = "";
  if (!cloud) return;

  var w = stage.clientWidth, h = stage.clientHeight;
  var root = el("g", { transform: "translate(" + (w / 2 + view.x) + "," + (h / 2 + view.y) + ") scale(" + view.scale + ")" }, stage);
  var detail = levelOfDetail();
  var now = Date.now();
  geometry = {};

  // Sizes follow from what is inside, so a cloud of three systems and a cloud of
  // thirty are both legible rather than one being empty and the other a heap.
  var hosts = cloud.hosts || [];
  var hostSizes = hosts.map(function (host) {
    var systems = host.systems || [];
    var ring = ringRadius(systems.length, 34, 70);
    return { ring: ring, radius: Math.max(110, ring + 34 + 16) };
  });
  var biggest = hostSizes.reduce(function (m, s) { return Math.max(m, s.radius); }, 110);
  var hostRing = ringRadius(hosts.length, biggest, 0);
  var cloudRadius = Math.max(300, hostRing + biggest + 40);
  extent = cloudRadius;
  if (!framed) { framed = true; frame(); draw(); return; }

  el("circle", { r: cloudRadius, fill: "#fff", stroke: "var(--line)",
                 "stroke-width": 1 / view.scale }, root);
  label(cloud.name, 0, -cloudRadius - 14 / view.scale, 22, "var(--ink)", root);

  // Disks first, lines over them.
  //
  // The other way round is the usual convention for a node-link diagram and is
  // wrong here, because these nodes are nested containers rather than dots: a
  // host disk is opaque and covers whatever was drawn beneath it, so every line
  // between two systems on the same host was drawn and then painted over. On a
  // one-host cloud that is all of them, which is what it looked like — a picture
  // with no connections at all while the model had them.
  var disks = el("g", {}, root);
  var lines = el("g", { "pointer-events": "none" }, root);

  hosts.forEach(function (host, hi) {
    var size = hostSizes[hi];
    var hp = place(hi, hosts.length, hostRing);
    var hostG = el("g", { transform: "translate(" + hp.x + "," + hp.y + ")", class: "disk" }, disks);
    el("circle", { r: size.radius, fill: "#eef3f8", stroke: "var(--line)",
                   "stroke-width": 1 / view.scale }, hostG);
    label(host.name, 0, -size.radius - 8 / view.scale, 13, "var(--dim)", hostG);

    var systems = host.systems || [];
    systems.forEach(function (sys, si) {
      var id = host.name + "/" + sys.name;
      if (!seen.has(id)) seen.set(id, now);
      var age = (now - seen.get(id)) / 700;
      var sp = place(si, systems.length, size.ring);
      centres.set(id, { x: hp.x + sp.x, y: hp.y + sp.y, name: sys.name });
      var sysG = el("g", { transform: "translate(" + sp.x + "," + sp.y + ")", class: "disk",
                           opacity: Math.min(1, age) }, hostG);

      el("circle", { r: 34, fill: colourOf(sys.level), "fill-opacity": 0.22,
                     stroke: colourOf(sys.level), "stroke-width": 2 / view.scale }, sysG);

      // Just arrived: a green ring swelling outwards and fading, twice, so it
      // catches the eye of somebody who was looking elsewhere.
      var since = now - (arrivals.get(id) || -1e9);
      if (since >= 0 && since < ARRIVING) {
        var beat = (since % 1000) / 1000;
        el("circle", { r: 34 + beat * 26, fill: "none", stroke: "#2e9e5b",
                       "stroke-width": 3 / view.scale, "stroke-opacity": 1 - beat }, sysG);
        el("circle", { r: 34, fill: "#2e9e5b", "fill-opacity": 0.28 * (1 - since / ARRIVING) }, sysG);
      }
      // The name belongs to the system at every level past the widest: it is
      // what an operator is looking for.
      if (detail >= 1) {
        label(sys.name, 0, 34 + 12 / view.scale, 12, "var(--ink)", sysG);
      }

      var assets = sys.assets || [];
      var assetRing = ringRadius(assets.length, 7, 0);
      assets.forEach(function (asset, ai) {
        var aid = id + "/" + asset.name;
        var ap = place(ai, assets.length, Math.min(assetRing, 20));
        geometry[aid] = { x: hp.x + sp.x + ap.x, y: hp.y + sp.y + ap.y };

        if (detail >= 2) {
          var assetG = el("g", { transform: "translate(" + ap.x + "," + ap.y + ")" }, sysG);
          el("circle", { r: 7, fill: "#fff", stroke: colourOf(sys.level),
                         "stroke-width": 1.5 / view.scale }, assetG);
          if (detail >= 3) {
            label(asset.name, 0, 7 + 8 / view.scale, 9, "var(--dim)", assetG);
          }
          // A service this asset asked for and nobody provides: the one state
          // that looks healthy from every other angle.
          var unmet = (asset.wants || []).filter(function (want) { return !want.satisfied; });
          if (unmet.length) {
            var dot = el("circle", { cx: 9, cy: -9, r: 3.2, fill: "var(--open)" }, assetG);
            el("animate", { attributeName: "opacity", values: "1;0.15;1",
                            dur: "1.4s", repeatCount: "indefinite" }, dot);
            el("title", {}, dot).textContent =
              "wants, and nothing provides: " + unmet.map(function (w) { return w.definition; }).join(", ");
          }
        }
      });
    });
  });

  // Systems that have gone. They are not in the picture any more, so they are
  // drawn from where they last were: a red disk swelling and fading over a
  // moment. Without it a system leaves by simply not being drawn, which is the
  // one change a picture can make that nobody notices.
  departures.forEach(function (d, id) {
    var since = now - d.at;
    if (since > LEAVING) return;
    var grow = since / LEAVING;
    var goneG = el("g", { transform: "translate(" + d.x + "," + d.y + ")" }, disks);
    el("circle", { r: 34 + grow * 34, fill: "#d0342c", "fill-opacity": 0.35 * (1 - grow),
                   stroke: "#d0342c", "stroke-width": 3 / view.scale,
                   "stroke-opacity": 1 - grow }, goneG);
    if (detail >= 1) {
      label(d.name + " gone", 0, 34 + 12 / view.scale, 12, "#d0342c", goneG);
    }
  });

  // The lines: who depends on whom. Bundled while zoomed out, separated and
  // named once there is room to read them.
  var bundles = {};
  (cloud.links || []).forEach(function (link) {
    var a = geometry[link.from], b = geometry[link.to];
    if (!a || !b) return;
    var key = link.from + "|" + link.to;
    (bundles[key] = bundles[key] || []).push(link);
  });

  Object.keys(bundles).forEach(function (key) {
    var group = bundles[key];
    var a = geometry[group[0].from], b = geometry[group[0].to];
    if (detail <= 1) {
      // One strand for the lot, thicker the more there are.
      el("path", { d: "M" + a.x + "," + a.y + "L" + b.x + "," + b.y,
                   stroke: "var(--dim)", "stroke-width": Math.min(4, group.length) / view.scale,
                   fill: "none", "stroke-opacity": 0.55 }, lines);
      return;
    }
    group.forEach(function (link, i) {
      var lift = (i - (group.length - 1) / 2) * 6;
      var mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2 + lift;
      var path = el("path", { d: "M" + a.x + "," + a.y + "Q" + mx + "," + my + " " + b.x + "," + b.y,
                              stroke: "var(--dim)", "stroke-width": 1.4 / view.scale, fill: "none",
                              "stroke-opacity": 0.8 }, lines);
      // Mission decides the style: driving something must not look like reading
      // it. Colour is left to say one thing only, which is security.
      if (link.mission === "actuation" || link.mission === "control") {
        path.setAttribute("stroke-dasharray", (5 / view.scale) + " " + (3 / view.scale));
      }
      el("title", {}, path).textContent = link.definition + " → " + link.to;
      if (detail >= 3) {
        label(link.definition, mx, my - 4 / view.scale, 9, "var(--dim)", lines);
      }
    });
  });
}

// ---- the wheel, and dragging ------------------------------------------------

stage.addEventListener("wheel", function (e) {
  e.preventDefault();
  var factor = Math.exp(-e.deltaY * 0.0015);
  var next = Math.min(14, Math.max(0.55, view.scale * factor));
  // Zoom about the pointer, so the thing under the cursor stays under it.
  var rect = stage.getBoundingClientRect();
  var px = e.clientX - rect.left - rect.width / 2 - view.x;
  var py = e.clientY - rect.top - rect.height / 2 - view.y;
  var ratio = next / view.scale;
  view.x -= px * (ratio - 1);
  view.y -= py * (ratio - 1);
  view.scale = next;
  draw();
}, { passive: false });

var dragging = false, lastX = 0, lastY = 0;
stage.addEventListener("pointerdown", function (e) {
  dragging = true; lastX = e.clientX; lastY = e.clientY;
  stage.classList.add("dragging"); stage.setPointerCapture(e.pointerId);
});
stage.addEventListener("pointermove", function (e) {
  if (!dragging) return;
  view.x += e.clientX - lastX; view.y += e.clientY - lastY;
  lastX = e.clientX; lastY = e.clientY;
  draw();
});
stage.addEventListener("pointerup", function (e) {
  dragging = false; stage.classList.remove("dragging"); stage.releasePointerCapture(e.pointerId);
});
window.addEventListener("resize", draw);

// ---- keeping up with the cloud ---------------------------------------------

function refresh() {
  fetch("model", { cache: "no-store" })
    .then(function (r) { return r.json(); })
    .then(function (next) {
      cloud = next;
      document.getElementById("cloudname").textContent = next.name || "local cloud";
      var notes = document.getElementById("notes");
      notes.className = (next.notes && next.notes.length) ? "hint note" : "hint";
      notes.textContent = (next.notes || []).join(" · ");
      // The two edges worth announcing. A system that was not here and now is,
      // and one that was here and now is not — the second cannot be drawn from
      // the picture, because it is no longer in it, so it is drawn from where it
      // last was.
      var present = new Set();
      (next.hosts || []).forEach(function (h) {
        (h.systems || []).forEach(function (s) { present.add(h.name + "/" + s.name); });
      });
      var now = Date.now();
      // The first picture is not an arrival of everything. A page that opened
      // onto a cloud of thirty systems all flashing green would announce
      // nothing, since an announcement that fires for everything at once is
      // just a colour.
      if (!greeted) {
        greeted = true;
        present.forEach(function (id) { seen.set(id, now - 700); });
      }
      present.forEach(function (id) {
        if (!seen.has(id)) { arrivals.set(id, now); }
      });
      seen.forEach(function (_, id) {
        if (present.has(id)) return;
        var where = centres.get(id);
        if (where) { departures.set(id, { at: now, x: where.x, y: where.y, name: where.name }); }
        seen.delete(id);
        centres.delete(id);
      });
      draw();
      tick();
    })
    .catch(function (err) {
      var notes = document.getElementById("notes");
      notes.className = "hint note";
      notes.textContent = "cannot reach the painter: " + err;
    });
}

// tick redraws while an announcement is in progress, and stops when the picture
// is quiet again. A page showing an unchanging cloud should not be repainting
// itself sixty times a second on a machine that has other work to do.
var greeted = false;
var ticking = false;
function tick() {
  if (ticking) return;
  ticking = true;
  requestAnimationFrame(function step() {
    draw();
    if (animating()) {
      requestAnimationFrame(step);
    } else {
      ticking = false;
      draw(); // once more, to settle without the announcement on it
    }
  });
}

refresh();
setInterval(refresh, 5000);
</script>
</body>
</html>
`
