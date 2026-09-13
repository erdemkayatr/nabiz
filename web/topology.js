"use strict";

// ==========================================================================
// TOPOLOGY
//
// The layout is solved by a hand-written force simulation: nodes repel each
// other, edges pull like springs, and the centre gathers them gently. Nodes
// can be dragged, and the canvas can be zoomed and panned.
//
// Edge thickness shows call volume; the dots flowing along it show call rate.
// Thickness is history, flow is the present moment.
// ==========================================================================

const NODE_ICONS = {
  service: "M12 3.2 19.4 7.5v9L12 20.8 4.6 16.5v-9zM12 3.2v8.6m0 0 7.4-4.3M12 11.8 4.6 7.5",
  database: "M5 6.6c0-1.6 3.1-2.9 7-2.9s7 1.3 7 2.9-3.1 2.9-7 2.9-7-1.3-7-2.9zM5 6.6v10.8c0 1.6 3.1 2.9 7 2.9s7-1.3 7-2.9V6.6M5 12c0 1.6 3.1 2.9 7 2.9s7-1.3 7-2.9",
  messaging: "M3.5 6.5h17v11h-17zM3.5 7.4 12 13.3l8.5-5.9",
  external: "M12 3.4a8.6 8.6 0 100 17.2 8.6 8.6 0 000-17.2zM3.4 12h17.2M12 3.4c2.3 2.4 2.3 14.8 0 17.2M12 3.4c-2.3 2.4-2.3 14.8 0 17.2",
  user: "M12 11.8a3.9 3.9 0 100-7.8 3.9 3.9 0 000 7.8zM4.4 20.4c0-3.8 3.4-5.7 7.6-5.7s7.6 1.9 7.6 5.7",
};

const topo = {
  nodes: [], links: [], byId: new Map(),
  alpha: 0, raf: null,
  view: { x: 0, y: 0, k: 1 },
  selected: null, drag: null, pan: null, dragMoved: false,
  size: { w: 900, h: 600 },
  maxLinkCalls: 1, needsFit: false, sig: null,
};

function nodeColor(n) {
  if (n.errorRate > 0.01) return "var(--err)";
  switch (n.type) {
    case "database": return "var(--db)";
    case "messaging": return "var(--mq)";
    case "external": return "var(--ext)";
    case "user": return "var(--user)";
    default: return "var(--accent)";
  }
}

async function loadTopology() {
  try {
    const data = await api("/api/v1/topology?from=" + state.range + "&level=" + state.level + scopeQuery());
    buildGraph(data);
    markOk();
  } catch (e) {
    if (e.status === 401) return;
    stopSim();
    $("#graph").replaceChildren();
    $("#detail").hidden = true;
    drawMessage(t("err.topology") + ": " + friendlyError(e), "var(--err)");
    markStale();
  }
}

function buildGraph(data) {
  const wrap = $("#canvas-wrap");
  topo.size = { w: wrap.clientWidth || 900, h: wrap.clientHeight || 600 };

  const rawNodes = data.nodes || [];
  const rawLinks = (data.edges || []).filter((e) => e.source !== e.target);

  if (!rawNodes.length) {
    stopSim();
    topo.nodes = []; topo.links = []; topo.byId = new Map(); topo.sig = null;
    $("#graph").replaceChildren();
    $("#detail").hidden = true;
    drawMessage(t("empty.traffic"), "var(--text-dim)");
    return;
  }

  // If the topology's shape has not changed, never re-run the simulation: a
  // graph that makes nodes jump every 10 seconds cannot be followed. Only the
  // numbers are refreshed.
  const sig = rawNodes.map((n) => n.id).sort().join("|") + "##" +
              rawLinks.map((e) => e.source + ">" + e.target).sort().join("|");
  if (sig === topo.sig && topo.nodes.length) {
    updateInPlace(rawNodes, rawLinks);
    return;
  }
  topo.sig = sig;

  // The shape changed, but familiar nodes keep their positions.
  const prev = topo.byId;
  const maxCalls = Math.max(1, ...rawNodes.map((n) => n.calls));

  topo.nodes = rawNodes.map((n) => {
    const old = prev.get(n.id);
    return Object.assign({}, n, {
      r: 17 + 13 * Math.sqrt(n.calls / maxCalls),
      x: old ? old.x : null, y: old ? old.y : null,
      vx: 0, vy: 0, fixed: old ? old.fixed : false,
    });
  });
  topo.byId = new Map(topo.nodes.map((n) => [n.id, n]));

  topo.links = rawLinks
    .filter((e) => topo.byId.has(e.source) && topo.byId.has(e.target))
    .map((e, i) => Object.assign({}, e, { id: "l" + i, s: topo.byId.get(e.source), t: topo.byId.get(e.target) }));
  topo.maxLinkCalls = Math.max(1, ...topo.links.map((l) => l.calls));

  seedPositions();
  renderGraph();
  // The first layout needs a full simulation; re-laying out the entire graph
  // for one added node is needless.
  topo.alpha = prev.size ? 0.45 : 1;
  if (!prev.size) { topo.needsFit = true; topo.view = { x: 0, y: 0, k: 1 }; }
  startSim();
}

// Same shape, new numbers: refresh node and edge metrics in place.
// Positions, zoom and selection are left untouched.
function updateInPlace(rawNodes, rawLinks) {
  const maxCalls = Math.max(1, ...rawNodes.map((n) => n.calls));

  for (const fresh of rawNodes) {
    const n = topo.byId.get(fresh.id);
    if (!n) continue;
    Object.assign(n, { calls: fresh.calls, errors: fresh.errors, errorRate: fresh.errorRate });
    n.r = 17 + 13 * Math.sqrt(n.calls / maxCalls);

    const color = nodeColor(n);
    const circles = n.g.querySelectorAll("circle");
    circles[0].setAttribute("r", n.r + 5);
    circles[0].setAttribute("fill", color);
    circles[1].setAttribute("r", n.r);
    circles[1].setAttribute("stroke", color);

    const icon = n.g.querySelector(".node-icon");
    const scale = (n.r * 1.15) / 24;
    icon.setAttribute("stroke", color);
    icon.setAttribute("transform", "translate(" + (-12 * scale) + "," + (-12 * scale) + ") scale(" + scale.toFixed(3) + ")");

    n.g.querySelector(".node-label").setAttribute("y", n.r + 16);
    const meta = n.g.querySelector(".node-meta");
    meta.setAttribute("y", n.r + 30);
    meta.textContent = n.type === "user" ? t("type.user") : fmtCount(n.calls) + " · " + fmtPct(n.errorRate);
    n.g.querySelector("title").textContent = nodeTooltip(n);
  }

  const freshByKey = new Map(rawLinks.map((e) => [e.source + ">" + e.target, e]));
  topo.maxLinkCalls = Math.max(1, ...rawLinks.map((e) => e.calls));
  for (const l of topo.links) {
    const fresh = freshByKey.get(l.source + ">" + l.target);
    if (!fresh) continue;
    Object.assign(l, {
      calls: fresh.calls, errors: fresh.errors, errorRate: fresh.errorRate,
      avgMs: fresh.avgMs, p95Ms: fresh.p95Ms, p99Ms: fresh.p99Ms, maxMs: fresh.maxMs,
    });
    const bad = l.errorRate > 0.01;
    l.path.classList.toggle("bad", bad);
    l.path.setAttribute("marker-end", bad ? "url(#arrow-bad)" : "url(#arrow)");
    l.path.setAttribute("stroke-width", (1.3 + 3.2 * Math.sqrt(l.calls / topo.maxLinkCalls)).toFixed(2));
    l.path.querySelector("title").textContent = linkTooltip(l);
    l.hit.querySelector("title").textContent = linkTooltip(l);
    l.speed = 0.055 + 0.055 * Math.min(1, Math.log10(l.calls + 1) / 4);
    for (const d of l.dots) d.node.setAttribute("fill", bad ? "var(--err)" : "var(--accent)");
  }

  positionNodes();
  renderDetail();
}

// Rather than starting the simulation from random positions, we seed it with a
// layered guess: the graph settling into the same shape on every refresh is
// what preserves the operator's "this was over here yesterday" instinct.
function seedPositions() {
  const incoming = new Map(topo.nodes.map((n) => [n.id, 0]));
  for (const l of topo.links) incoming.set(l.target, (incoming.get(l.target) || 0) + 1);

  const depth = new Map();
  let frontier = topo.nodes.filter((n) => !incoming.get(n.id)).map((n) => n.id);
  if (!frontier.length) frontier = [topo.nodes.slice().sort((a, b) => b.calls - a.calls)[0].id];
  for (const id of frontier) depth.set(id, 0);

  const out = new Map();
  for (const l of topo.links) {
    if (!out.has(l.source)) out.set(l.source, []);
    out.get(l.source).push(l.target);
  }
  let cur = frontier, d = 0;
  while (cur.length && d < topo.nodes.length + 2) {
    const next = [];
    for (const id of cur) for (const t of out.get(id) || []) {
      if (!depth.has(t) || depth.get(t) < d + 1) { depth.set(t, d + 1); next.push(t); }
    }
    cur = next; d++;
  }

  const maxDepth = Math.max(0, ...depth.values());
  const perLayer = new Map();
  for (const n of topo.nodes) {
    const l = depth.get(n.id) || 0;
    if (!perLayer.has(l)) perLayer.set(l, []);
    perLayer.get(l).push(n);
  }
  const colGap = Math.max(220, topo.size.w / (maxDepth + 2));
  for (const [l, group] of perLayer) {
    group.forEach((n, i) => {
      if (n.x != null) return;   // it already has a position from the last round
      n.x = colGap * (l + 1);
      n.y = topo.size.h * ((i + 1) / (group.length + 1));
    });
  }
}

// --- simulation ---

// Labels take up about 140px below a node; a spring shorter than that turns
// the graph into an unreadable tangle. Gravity is only strong enough to keep
// disconnected parts on the canvas: its job is not to gather, only to stop
// things escaping.
const SIM = {
  repulsion: 16000, linkDist: 215, linkStrength: 0.055,
  gravity: 0.0022, damping: 0.82, minAlpha: 0.004, decay: 0.018,
};

function simStep() {
  const nodes = topo.nodes, links = topo.links;
  const cx = topo.size.w / 2, cy = topo.size.h / 2;
  const a = topo.alpha;

  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i];
    for (let j = i + 1; j < nodes.length; j++) {
      const m = nodes[j];
      let dx = m.x - n.x, dy = m.y - n.y;
      let d2 = dx * dx + dy * dy;
      if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
      const d = Math.sqrt(d2);
      // Let larger nodes claim more room.
      const f = (SIM.repulsion + (n.r + m.r) * 60) / d2 * a;
      const fx = (dx / d) * f, fy = (dy / d) * f;
      n.vx -= fx; n.vy -= fy; m.vx += fx; m.vy += fy;
    }
  }

  for (const l of links) {
    const dx = l.t.x - l.s.x, dy = l.t.y - l.s.y;
    const d = Math.hypot(dx, dy) || 1;
    const f = (d - SIM.linkDist) * SIM.linkStrength * a;
    const fx = (dx / d) * f, fy = (dy / d) * f;
    l.s.vx += fx; l.s.vy += fy; l.t.vx -= fx; l.t.vy -= fy;
  }

  for (const n of nodes) {
    if (n.fixed || (topo.drag && topo.drag.node === n)) { n.vx = 0; n.vy = 0; continue; }
    n.vx += (cx - n.x) * SIM.gravity * a;
    n.vy += (cy - n.y) * SIM.gravity * a;
    n.vx *= SIM.damping; n.vy *= SIM.damping;
    n.x += n.vx; n.y += n.vy;
  }
  topo.alpha = Math.max(0, a - a * SIM.decay);
}

function startSim() {
  if (topo.raf) return;
  let last = performance.now();
  const loop = (now) => {
    const dt = Math.min(64, now - last); last = now;
    if (topo.alpha > SIM.minAlpha) {
      simStep();
      positionNodes();
    } else if (topo.needsFit) {
      // The layout has settled: fit the graph to the canvas once. If the user
      // zooms afterwards, do not interfere again.
      topo.needsFit = false;
      fitView();
    }
    animateFlow(dt);
    topo.raf = requestAnimationFrame(loop);
  };
  topo.raf = requestAnimationFrame(loop);
}

function stopSim() {
  if (topo.raf) cancelAnimationFrame(topo.raf);
  topo.raf = null;
}

function reheat(alpha) {
  topo.alpha = Math.max(topo.alpha, alpha);
  startSim();
}

// --- drawing ---

function renderGraph() {
  const root = $("#graph");
  root.replaceChildren();
  root.setAttribute("viewBox", "0 0 " + topo.size.w + " " + topo.size.h);

  const defs = svg("defs");
  defs.append(marker("arrow", "var(--link)"), marker("arrow-bad", "var(--err)"));
  const glow = svg("filter", { id: "glow", x: "-60%", y: "-60%", width: "220%", height: "220%" });
  glow.innerHTML = '<feGaussianBlur stdDeviation="2.2" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge>';
  defs.append(glow);

  const viewport = svg("g", { id: "viewport" });
  const gLinks = svg("g"), gFlow = svg("g"), gNodes = svg("g");
  viewport.append(gLinks, gFlow, gNodes);
  root.append(defs, viewport);

  for (const l of topo.links) {
    const bad = l.errorRate > 0.01;
    l.path = svg("path", {
      class: "link" + (bad ? " bad" : ""),
      "stroke-width": (1.3 + 3.2 * Math.sqrt(l.calls / topo.maxLinkCalls)).toFixed(2),
      "marker-end": bad ? "url(#arrow-bad)" : "url(#arrow)",
    });
    // An invisible thick twin path: a 1.6px line is hard to hit with a mouse.
    l.hit = svg("path", { class: "link-hit" });
    l.hit.addEventListener("click", (e) => { e.stopPropagation(); select("link", l.id); });
    l.path.append(svgTitle(linkTooltip(l)));
    l.hit.append(svgTitle(linkTooltip(l)));
    gLinks.append(l.path, l.hit);

    // Flowing dots: count and speed grow with call volume, but logarithmically
    // — 10x traffic as 10x dots would make the graph unreadable.
    const count = Math.min(4, 1 + Math.floor(Math.log10(l.calls + 1)));
    l.dots = [];
    for (let i = 0; i < count; i++) {
      const c = svg("circle", { class: "flow", r: 2.6, fill: bad ? "var(--err)" : "var(--accent)", filter: "url(#glow)" });
      gFlow.append(c);
      l.dots.push({ node: c, p: i / count });
    }
    l.speed = 0.055 + 0.055 * Math.min(1, Math.log10(l.calls + 1) / 4);
  }

  for (const n of topo.nodes) {
    const color = nodeColor(n);
    const g = svg("g", { class: "node" });
    g.append(svg("circle", { r: n.r + 5, fill: color, opacity: 0.08 }));
    g.append(svg("circle", { class: "node-ring", r: n.r, fill: "var(--panel)", stroke: color, "stroke-width": 2.2 }));

    const icon = svg("path", { class: "node-icon", d: NODE_ICONS[n.type] || NODE_ICONS.service, stroke: color });
    const scale = (n.r * 1.15) / 24;
    icon.setAttribute("transform", "translate(" + (-12 * scale) + "," + (-12 * scale) + ") scale(" + scale.toFixed(3) + ")");
    g.append(icon);

    const label = svg("text", { class: "node-label", y: n.r + 16 });
    label.textContent = truncate(n.label, 22);
    const meta = svg("text", { class: "node-meta", y: n.r + 30 });
    meta.textContent = n.type === "user" ? t("type.user") : fmtCount(n.calls) + " · " + fmtPct(n.errorRate);
    g.append(label, meta, svgTitle(nodeTooltip(n)));

    g.addEventListener("pointerdown", (e) => beginDrag(e, n));
    g.addEventListener("click", (e) => { e.stopPropagation(); if (!topo.dragMoved) select("node", n.id); });

    n.g = g;
    gNodes.append(g);
  }

  applyView();
  positionNodes();
  applySelection();
  renderDetail();
}

function marker(id, color) {
  const m = svg("marker", { id: id, viewBox: "0 0 10 10", refX: "9", refY: "5", markerWidth: "5.5", markerHeight: "5.5", orient: "auto-start-reverse" });
  m.append(svg("path", { d: "M 0 0 L 10 5 L 0 10 z", fill: color }));
  return m;
}
function svgTitle(text) { const t = svg("title"); t.textContent = text; return t; }

function nodeTooltip(n) {
  const parts = [n.id, t("tooltip.type") + ": " + (TYPE_LABEL[n.type] || n.type)];
  if (n.namespace) parts.push("namespace: " + n.namespace);
  if (n.workload) parts.push("workload: " + n.workload);
  if (n.type !== "user") parts.push(fmtCount(n.calls) + " " + t("tooltip.calls") + " · " + t("tooltip.error") + " " + fmtPct(n.errorRate));
  return parts.join("\n");
}
function linkTooltip(l) {
  return l.source + " → " + l.target + "\n" +
         fmtCount(l.calls) + " " + t("tooltip.calls") + " · " + t("tooltip.error") + " " + fmtPct(l.errorRate) + "\n" +
         t("tooltip.avg") + " " + fmtMs(l.avgMs) + " · p95 " + fmtMs(l.p95Ms) + " · p99 " + fmtMs(l.p99Ms);
}

function positionNodes() {
  for (const n of topo.nodes) {
    if (n.g) n.g.setAttribute("transform", "translate(" + n.x.toFixed(1) + "," + n.y.toFixed(1) + ")");
  }
  for (const l of topo.links) {
    const d = linkPath(l);
    l.path.setAttribute("d", d);
    l.hit.setAttribute("d", d);
  }
}

// Edges are slightly curved so A→B and B→A do not sit on top of each other.
// The ends start at the circle's edge, leaving room for the arrowhead.
function linkPath(l) {
  const s = l.s, t = l.t;
  const dx = t.x - s.x, dy = t.y - s.y;
  const bow = 0.13;
  const mx = (s.x + t.x) / 2 + dy * bow;
  const my = (s.y + t.y) / 2 - dx * bow;
  const a1 = Math.atan2(my - s.y, mx - s.x);
  const a2 = Math.atan2(my - t.y, mx - t.x);
  const sx = s.x + Math.cos(a1) * (s.r + 2), sy = s.y + Math.sin(a1) * (s.r + 2);
  const tx = t.x + Math.cos(a2) * (t.r + 7), ty = t.y + Math.sin(a2) * (t.r + 7);
  return "M " + sx.toFixed(1) + " " + sy.toFixed(1) + " Q " + mx.toFixed(1) + " " + my.toFixed(1) + " " + tx.toFixed(1) + " " + ty.toFixed(1);
}

function animateFlow(dt) {
  const step = dt / 1000;
  for (const l of topo.links) {
    if (!l.path || !l.dots) continue;
    let len;
    try { len = l.path.getTotalLength(); } catch (_) { continue; }
    if (!len) continue;
    for (const dot of l.dots) {
      dot.p = (dot.p + l.speed * step) % 1;
      const pt = l.path.getPointAtLength(dot.p * len);
      dot.node.setAttribute("cx", pt.x.toFixed(1));
      dot.node.setAttribute("cy", pt.y.toFixed(1));
    }
  }
}

function drawMessage(msg, color) {
  const root = $("#graph");
  root.replaceChildren();
  root.setAttribute("viewBox", "0 0 " + topo.size.w + " " + topo.size.h);
  const t = svg("text", { x: topo.size.w / 2, y: topo.size.h / 2, "text-anchor": "middle", fill: color, "font-size": "13" });
  t.textContent = msg;
  root.append(t);
}

// --- zoom / pan / drag ---

function applyView() {
  const vp = $("#viewport");
  if (vp) vp.setAttribute("transform", "translate(" + topo.view.x + "," + topo.view.y + ") scale(" + topo.view.k + ")");
}

// The viewBox scales the canvas to the width; convert screen pixels first.
function toCanvas(clientX, clientY) {
  const rect = $("#graph").getBoundingClientRect();
  return {
    x: (clientX - rect.left) * (topo.size.w / rect.width),
    y: (clientY - rect.top) * (topo.size.h / rect.height),
  };
}
function screenToGraph(clientX, clientY) {
  const c = toCanvas(clientX, clientY);
  return { x: (c.x - topo.view.x) / topo.view.k, y: (c.y - topo.view.y) / topo.view.k };
}

function zoomAt(factor, clientX, clientY) {
  const c = toCanvas(clientX, clientY);
  const k2 = Math.min(3.2, Math.max(0.25, topo.view.k * factor));
  // Keep the point under the cursor fixed.
  topo.view.x = c.x - (c.x - topo.view.x) * (k2 / topo.view.k);
  topo.view.y = c.y - (c.y - topo.view.y) * (k2 / topo.view.k);
  topo.view.k = k2;
  applyView();
}

function fitView() {
  if (!topo.nodes.length) return;
  const pad = 70;
  const xs = topo.nodes.map((n) => n.x), ys = topo.nodes.map((n) => n.y);
  const minX = Math.min.apply(null, xs) - pad, maxX = Math.max.apply(null, xs) + pad;
  const minY = Math.min.apply(null, ys) - pad, maxY = Math.max.apply(null, ys) + pad + 24;
  const k = Math.min(2.0, Math.max(0.25, Math.min(topo.size.w / (maxX - minX), topo.size.h / (maxY - minY))));
  topo.view.k = k;
  topo.view.x = (topo.size.w - (minX + maxX) * k) / 2;
  topo.view.y = (topo.size.h - (minY + maxY) * k) / 2;
  applyView();
}

function beginDrag(e, node) {
  e.stopPropagation();
  const p = screenToGraph(e.clientX, e.clientY);
  topo.drag = { node: node, dx: node.x - p.x, dy: node.y - p.y };
  topo.dragMoved = false;
  // The capture has to be on the node ITSELF. Capturing on the SVG root makes
  // the root the target of the following click event too, and clicking a node
  // stops working entirely. Events still bubble up to the root from here, so
  // the pan/move listeners keep working.
  node.g.setPointerCapture(e.pointerId);
  reheat(0.35);
}

function beginPan(e) {
  if (e.target.closest(".node") || e.target.closest(".link-hit")) return;
  topo.pan = { x: e.clientX, y: e.clientY, ox: topo.view.x, oy: topo.view.y };
  $("#graph").classList.add("panning");
  $("#graph").setPointerCapture(e.pointerId);
}

function onPointerMove(e) {
  if (topo.drag) {
    const p = screenToGraph(e.clientX, e.clientY);
    topo.drag.node.x = p.x + topo.drag.dx;
    topo.drag.node.y = p.y + topo.drag.dy;
    topo.dragMoved = true;
    positionNodes();
    reheat(0.25);
    return;
  }
  if (topo.pan) {
    const rect = $("#graph").getBoundingClientRect();
    const scale = topo.size.w / rect.width;
    topo.view.x = topo.pan.ox + (e.clientX - topo.pan.x) * scale;
    topo.view.y = topo.pan.oy + (e.clientY - topo.pan.y) * scale;
    applyView();
  }
}

function onPointerUp() {
  if (topo.drag) {
    // A dropped node stays where it was put, so the operator can arrange the
    // graph the way they picture it.
    topo.drag.node.fixed = true;
    topo.drag = null;
    reheat(0.2);
  }
  topo.pan = null;
  $("#graph").classList.remove("panning");
  setTimeout(() => { topo.dragMoved = false; }, 0);
}

// --- selection and detail panel ---

function select(kind, id) {
  const same = topo.selected && topo.selected.kind === kind && topo.selected.id === id;
  topo.selected = same ? null : { kind: kind, id: id };
  applySelection();
  renderDetail();
}

function applySelection() {
  const sel = topo.selected;
  const activeNodes = new Set(), activeLinks = new Set();

  if (sel && sel.kind === "node") {
    activeNodes.add(sel.id);
    for (const l of topo.links) {
      if (l.source === sel.id || l.target === sel.id) {
        activeLinks.add(l.id); activeNodes.add(l.source); activeNodes.add(l.target);
      }
    }
  } else if (sel && sel.kind === "link") {
    const l = topo.links.find((x) => x.id === sel.id);
    if (l) { activeLinks.add(l.id); activeNodes.add(l.source); activeNodes.add(l.target); }
  }

  for (const n of topo.nodes) {
    if (n.g) n.g.classList.toggle("dimmed", !!sel && !activeNodes.has(n.id));
  }
  for (const l of topo.links) {
    const dim = !!sel && !activeLinks.has(l.id);
    if (l.path) l.path.classList.toggle("dimmed", dim);
    for (const d of l.dots || []) d.node.classList.toggle("dimmed", dim);
  }
}

// Node and edge type labels come from the dictionary; they follow the language.
const TYPE_LABEL = new Proxy({}, {
  get: (_, key) => (typeof key === "string" && STRINGS[LANG]["type." + key] !== undefined ? t("type." + key) : undefined),
  has: () => true,
});

function renderDetail() {
  const panel = $("#detail");
  const sel = topo.selected;
  if (!sel) { panel.hidden = true; panel.replaceChildren(); return; }

  const rows = [];
  let title, subtitle, goService = null;

  if (sel.kind === "node") {
    const n = topo.byId.get(sel.id);
    if (!n) { panel.hidden = true; return; }
    title = n.label;
    subtitle = TYPE_LABEL[n.type] || n.type;
    if (n.namespace) rows.push([t("detail.namespace"), n.namespace]);
    if (n.workload) rows.push([t("detail.workload"), n.workload]);
    if (n.type !== "user") {
      rows.push([t("detail.calls"), fmtCount(n.calls)]);
      rows.push([t("detail.errorRate"), fmtPct(n.errorRate), errClass(n.errorRate)]);
    }
    const inbound = topo.links.filter((l) => l.target === n.id);
    const outbound = topo.links.filter((l) => l.source === n.id);
    if (inbound.length) rows.push(["__section", t("detail.inbound") + " (" + inbound.length + ")"]);
    for (const l of inbound) rows.push([truncate(l.source, 20), "p95 " + fmtMs(l.p95Ms)]);
    if (outbound.length) rows.push(["__section", t("detail.outbound") + " (" + outbound.length + ")"]);
    for (const l of outbound) rows.push([truncate(l.target, 20), "p95 " + fmtMs(l.p95Ms)]);
    if (n.type === "service") goService = n.label.split("/").pop() || n.label;
  } else {
    const l = topo.links.find((x) => x.id === sel.id);
    if (!l) { panel.hidden = true; return; }
    title = truncate(l.source, 18) + " → " + truncate(l.target, 18);
    subtitle = TYPE_LABEL[l.type] || l.type;
    rows.push([t("detail.calls"), fmtCount(l.calls)]);
    rows.push([t("detail.errors"), fmtCount(l.errors) + " · " + fmtPct(l.errorRate), errClass(l.errorRate)]);
    rows.push(["__section", t("detail.latency")]);
    rows.push([t("detail.avg"), fmtMs(l.avgMs)]);
    rows.push(["p95", fmtMs(l.p95Ms)]);
    rows.push(["p99", fmtMs(l.p99Ms)]);
    rows.push([t("detail.max"), fmtMs(l.maxMs)]);
  }

  const head = el("header", {},
    el("div", { style: "min-width:0" },
      el("div", { class: "title", text: title }),
      el("div", { class: "hint", text: subtitle })),
    el("div", { class: "spacer" }),
    el("button", { class: "close", "aria-label": t("btn.close"), text: "×", onclick: () => select(sel.kind, sel.id) }));

  const body = el("div");
  for (const row of rows) {
    if (row[0] === "__section") { body.append(el("div", { class: "detail-section", text: row[1] })); continue; }
    body.append(el("div", { class: "kv" },
      el("span", { class: "k", text: row[0] }),
      row[2] ? el("b", {}, el("span", { class: "pill " + row[2], text: row[1] })) : el("b", { text: row[1] })));
  }

  panel.replaceChildren(head, body);
  if (goService) {
    panel.append(el("button", {
      class: "btn go", text: t("detail.goTraces"),
      onclick: () => { $("#f-service").value = goService; location.hash = "traces"; },
    }));
  }
  panel.hidden = false;
}

// initTopologyCanvas wires up the canvas's mouse, touch and zoom behaviour.
// Keeping everything about the topology in one file means the skeleton does not
// have to know how this view works inside.
function initTopologyCanvas() {
  const graph = $("#graph");
  graph.addEventListener("pointerdown", beginPan);
  graph.addEventListener("pointermove", onPointerMove);
  graph.addEventListener("pointerup", onPointerUp);
  graph.addEventListener("pointercancel", onPointerUp);
  graph.addEventListener("click", (e) => {
    if (!e.target.closest(".node") && !e.target.closest(".link-hit")) {
      topo.selected = null; applySelection(); renderDetail();
    }
  });
  graph.addEventListener("wheel", (e) => {
    e.preventDefault();
    zoomAt(e.deltaY < 0 ? 1.12 : 1 / 1.12, e.clientX, e.clientY);
  }, { passive: false });

  const center = () => { const r = graph.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; };
  $("#zoom-in").addEventListener("click", () => zoomAt(1.25, center()[0], center()[1]));
  $("#zoom-out").addEventListener("click", () => zoomAt(1 / 1.25, center()[0], center()[1]));
  $("#zoom-fit").addEventListener("click", fitView);

  // A tab in the background must not put load on the system it monitors.
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) stopSim();
    else if (state.view === "topology" && topo.nodes.length) startSim();
  });
  window.addEventListener("resize", () => {
    const wrap = $("#canvas-wrap");
    if (!wrap) return;
    topo.size = { w: wrap.clientWidth || 900, h: wrap.clientHeight || 600 };
    const g = $("#graph");
    if (g) g.setAttribute("viewBox", "0 0 " + topo.size.w + " " + topo.size.h);
  });
}
