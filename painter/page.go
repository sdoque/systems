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
var seen = new Map();   // id -> when it was first drawn, for fading in
var leaving = new Map(); // id -> when it stopped being reported, for fading out

// hash turns a name into a number, so a thing's place comes from what it is.
// The same cloud therefore draws the same way in every browser and after every
// restart, which is what lets an operator stop hunting for things.
function hash(text) {
  var h = 2166136261;
  for (var i = 0; i < text.length; i++) { h ^= text.charCodeAt(i); h = Math.imul(h, 16777619); }
  return (h >>> 0) / 4294967295;
}

// place spreads n things around a disk of the given radius, at angles taken
// from their names rather than from their order, so adding one does not move
// the others.
function place(name, radius) {
  var angle = hash(name) * Math.PI * 2;
  var r = radius * (0.45 + 0.5 * hash(name + "/r"));
  return { x: Math.cos(angle) * r, y: Math.sin(angle) * r };
}

function colourOf(level) {
  if (level === "authorized") return "var(--authorized)";
  if (level === "identified") return "var(--identified)";
  if (level === "enrolling") return "var(--enrolling)";
  if (level === "open") return "var(--open)";
  return "var(--unknown)";
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

function draw() {
  stage.innerHTML = "";
  if (!cloud) return;

  var w = stage.clientWidth, h = stage.clientHeight;
  var root = el("g", { transform: "translate(" + (w / 2 + view.x) + "," + (h / 2 + view.y) + ") scale(" + view.scale + ")" }, stage);
  var detail = levelOfDetail();
  var now = Date.now();
  geometry = {};

  // The cloud itself: the disk everything else sits on.
  el("circle", { r: 300, fill: "#fff", stroke: "var(--line)", "stroke-width": 1 / view.scale }, root);
  if (detail === 0) {
    el("text", { y: -320, "text-anchor": "middle", "font-size": 22, fill: "var(--ink)" }, root)
      .textContent = cloud.name;
  }

  var lines = el("g", {}, root);   // drawn under the disks
  var disks = el("g", {}, root);

  (cloud.hosts || []).forEach(function (host) {
    var hp = place("host:" + host.name, 170);
    var hostG = el("g", { transform: "translate(" + hp.x + "," + hp.y + ")", class: "disk" }, disks);
    el("circle", { r: 110, fill: "#eef3f8", stroke: "var(--line)", "stroke-width": 1 / view.scale }, hostG);
    el("text", { y: -118, "text-anchor": "middle", "font-size": 11, fill: "var(--dim)" }, hostG)
      .textContent = host.name;

    (host.systems || []).forEach(function (sys) {
      var id = host.name + "/" + sys.name;
      if (!seen.has(id)) seen.set(id, now);
      var age = (now - seen.get(id)) / 700;
      var sp = place("sys:" + id, 70);
      var sysG = el("g", { transform: "translate(" + sp.x + "," + sp.y + ")", class: "disk",
                           opacity: Math.min(1, age) }, hostG);

      el("circle", { r: 34, fill: colourOf(sys.level), "fill-opacity": 0.22,
                     stroke: colourOf(sys.level), "stroke-width": 2 / view.scale }, sysG);
      if (detail >= 1) {
        el("text", { y: 48, "text-anchor": "middle", "font-size": 10, fill: "var(--ink)" }, sysG)
          .textContent = sys.name;
      }

      (sys.assets || []).forEach(function (asset) {
        var aid = id + "/" + asset.name;
        var ap = place("asset:" + aid, 20);
        geometry[aid] = { x: hp.x + sp.x + ap.x, y: hp.y + sp.y + ap.y };

        if (detail >= 2) {
          var assetG = el("g", { transform: "translate(" + ap.x + "," + ap.y + ")" }, sysG);
          el("circle", { r: 7, fill: "#fff", stroke: colourOf(sys.level),
                         "stroke-width": 1.5 / view.scale }, assetG);
          if (detail >= 3) {
            el("text", { y: 18, "text-anchor": "middle", "font-size": 6, fill: "var(--dim)" }, assetG)
              .textContent = asset.name;
          }
          // A service this asset asked for and nobody provides: the one state
          // that looks healthy from every other angle.
          var unmet = (asset.wants || []).filter(function (want) { return !want.satisfied; });
          if (unmet.length) {
            var dot = el("circle", { cx: 9, cy: -9, r: 3.2, fill: "var(--open)" }, assetG);
            var pulse = el("animate", { attributeName: "opacity", values: "1;0.15;1",
                                        dur: "1.4s", repeatCount: "indefinite" }, dot);
            void pulse;
            el("title", {}, dot).textContent =
              "wants, and nothing provides: " + unmet.map(function (w) { return w.definition; }).join(", ");
          }
        }
      });
    });
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
        el("text", { x: mx, y: my - 3, "text-anchor": "middle", "font-size": 6, fill: "var(--dim)" }, lines)
          .textContent = link.definition;
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
      // Anything no longer reported is remembered briefly, so a system that
      // stops answering dims rather than vanishing: gone and not answering just
      // now are different things, and only one of them is worth a callout.
      var present = new Set();
      (next.hosts || []).forEach(function (h) {
        (h.systems || []).forEach(function (s) { present.add(h.name + "/" + s.name); });
      });
      seen.forEach(function (_, id) { if (!present.has(id)) leaving.set(id, Date.now()); });
      leaving.forEach(function (when, id) {
        if (present.has(id)) { leaving.delete(id); return; }
        if (Date.now() - when > 30000) { leaving.delete(id); seen.delete(id); }
      });
      draw();
    })
    .catch(function (err) {
      var notes = document.getElementById("notes");
      notes.className = "hint note";
      notes.textContent = "cannot reach the painter: " + err;
    });
}

refresh();
setInterval(refresh, 5000);
setInterval(draw, 1000); // so fades and the pulse keep moving between refreshes
</script>
</body>
</html>
`
