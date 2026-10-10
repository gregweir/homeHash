/* homeHash dashboard — vanilla JS, no dependencies.
 * Live data via SSE (/api/events); history via /api/miners/{id}/history.
 * All dynamic DOM text is set via textContent (names are untrusted).
 */
"use strict";

const $ = (id) => document.getElementById(id);
const SVGNS = "http://www.w3.org/2000/svg";

/* ---------- formatting ---------- */

const fmtNum = (n, digits) => n.toLocaleString(undefined, {
  maximumFractionDigits: digits, minimumFractionDigits: digits,
});

function fmtHashrateGHz(gh) {
  if (gh >= 1000) return `${fmtNum(gh / 1000, 2)}`;
  return `${fmtNum(gh, 1)}`;
}
const hashrateUnit = (gh) => (gh >= 1000 ? "TH/s" : "GH/s");

function fmtUptime(seconds) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  if (d > 0) return `${d}d ${h}h`;
  const m = Math.floor((seconds % 3600) / 60);
  return `${h}h ${m}m`;
}

const fmtTime = (ms) => new Date(ms).toLocaleTimeString(undefined, {
  hour: "2-digit", minute: "2-digit",
});
const fmtTimeFull = (ms) => new Date(ms).toLocaleTimeString(undefined, {
  hour: "2-digit", minute: "2-digit", second: "2-digit",
});
const fmtDateShort = (ms) => new Date(ms).toLocaleDateString(undefined, {
  month: "short", day: "numeric",
});

/* ---------- state ---------- */

// Colors follow the entity (miner), never the current filter state.
const SERIES_TOKENS = ["var(--series-1)", "var(--series-2)"];
const colorOf = new Map();

function seriesColor(minerID) {
  if (!colorOf.has(minerID)) colorOf.set(minerID, SERIES_TOKENS[colorOf.size % SERIES_TOKENS.length]);
  return colorOf.get(minerID);
}

const state = {
  farm: null,
  miners: [],
  series: new Map(),     // minerID -> [{t, gh}] for the active range (rolling)
  farmSpark: [],         // [{t, gh}] farm hashrate, last ~24 points
  connected: false,
  everConnected: false,
  rangeHours: 24,        // matches the pressed range button in index.html
  controlEnabled: false, // server-reported write access (HOMEHASH_CONTROL)
};

const sparkCaps = 120;          // rolling SSE points per miner
const MAX_TABLE_ROWS = 200;

function pushPoint(buf, t, gh, cap) {
  if (buf.length && t <= buf[buf.length - 1].t) return; // reject out-of-order replays
  buf.push({ t, gh });
  if (buf.length > cap) buf.splice(0, buf.length - cap);
  return true;
}

/* ---------- theme ---------- */

const THEME_KEY = "homehash-theme";
function applyTheme(mode) {
  // mode: "auto" | "light" | "dark"
  if (mode === "auto") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", mode);
  try { localStorage.setItem(THEME_KEY, mode); } catch { /* per-viewer convenience */ }
}
try {
  const saved = localStorage.getItem(THEME_KEY);
  if (saved && saved !== "auto") document.documentElement.setAttribute("data-theme", saved);
} catch { /* ignore */ }

$("theme-toggle").addEventListener("click", () => {
  const cur = document.documentElement.getAttribute("data-theme");
  applyTheme(cur === "dark" ? "light" : cur === "light" ? "auto" : "dark");
});

/* ---------- KPI tiles ---------- */

function renderFarm(farm, miners) {
  if (!farm) return;
  const gh = farm.total_hashrate_gh || 0;
  $("kpi-hashrate").textContent = fmtHashrateGHz(gh);
  $("kpi-hashrate-unit").textContent = hashrateUnit(gh);
  const online = onlineCount(farm);
  $("kpi-hashrate-sub").textContent =
    gh > 0 ? `${farm.total_miners} miners · avg ${fmtNum(farm.avg_temperature_c, 1)}°C` : "no data yet";

  $("kpi-power").textContent = fmtNum(farm.total_power_w, 1);
  $("kpi-power-sub").textContent =
    farm.total_power_w > 0
      ? `${fmtNum(1000 / farm.total_power_w, 1)} J/TH · ${fmtNum(gh / farm.total_power_w, 1)} GH/W`
      : "no power data";

  const eff = farm.efficiency_gh_per_w || 0;
  $("kpi-efficiency").textContent = eff > 0 ? fmtNum(eff, 1) : "—";
  $("kpi-efficiency-sub").textContent = eff > 0 ? "hash rate per watt" : "awaiting power data";

  $("kpi-online").textContent = `${online}`;
  $("kpi-miners-sub").textContent = `${online}/${farm.total_miners} online`;

  $("footer-updated").textContent =
    `updated ${fmtTimeFull(Date.now())}`;
}

function onlineCount(farm) { return farm.online || 0; }

/* ---------- small SVG helpers ---------- */

function el(tag, attrs) {
  const node = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) node.setAttribute(k, v);
  return node;
}

/* Sparkline: last 12 points in the de-emphasis hue, tail + end dot in accent. */
function renderSparkline(container, points, accent) {
  container.textContent = ""; // clear previous content
  if (points.length < 3) return;

  const W = 200, H = 34, pad = 2;
  const values = points.map((p) => p.gh);
  const min = Math.min(...values), max = Math.max(...values);
  const span = max - min || 1;
  const x = (i) => pad + (i / (values.length - 1)) * (W - 2 * pad);
  const y = (v) => H - pad - ((v - min) / span) * (H - 2 * pad);

  const svg = el("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none" });
  const pts = points.map((p, i) => `${x(i).toFixed(1)},${y(p.gh).toFixed(1)}`).join(" ");
  if (values.length >= 2) {
    svg.appendChild(el("polyline", {
      points: pts, fill: "none",
      stroke: "var(--deemphasis)", "stroke-width": "1.5",
      "stroke-linecap": "round", "stroke-linejoin": "round",
    }));
    // current period in the accent
    svg.appendChild(el("polyline", {
      points: `${x(values.length - 2).toFixed(1)},${y(values[values.length - 2]).toFixed(1)} ${x(values.length - 1).toFixed(1)},${y(values[values.length - 1]).toFixed(1)}`,
      fill: "none", stroke: accent, "stroke-width": "1.5", "stroke-linecap": "round",
    }));
  }
  svg.appendChild(el("circle", {
    cx: x(values.length - 1).toFixed(1), cy: y(values[values.length - 1]).toFixed(1),
    r: "2.5", fill: accent,
  }));
  container.textContent = "";
  container.replaceChildren(svg);
}

/* ---------- hero chart ---------- */

const CHART = { H: 240, pad: { top: 10, right: 16, bottom: 24, left: 52 } };

function seriesInView() {
  const cutoff = Date.now() - state.rangeHours * 3.6e6;
  const out = [];
  for (const [id, buf] of state.series) {
    const pts = buf.filter((p) => p.t >= cutoff);
    if (pts.length >= 2) out.push({ id, pts });
  }
  return out;
}

function niceTicks(min, max, target = 4) {
  if (!(max > min)) max = min + 1;
  const raw = (max - min) / target;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const norm = raw / mag;
  const step = (norm >= 5 ? 5 : norm >= 2 ? 2 : 1) * mag;
  const start = Math.ceil(min / step) * step;
  const ticks = [];
  for (let v = start; v <= max + 1e-9; v += step) ticks.push(v);
  return ticks;
}

function downsample(points, targetPx) {
  if (points.length <= targetPx * 2) return points;
  const want = targetPx * 2;
  const out = [];
  const stride = points.length / want;
  let last = 0;
  for (let i = 0; i < points.length; i += stride) {
    const j = Math.min(points.length - 1, Math.floor(i));
    out.push(points[j]);
    last = j;
  }
  if (last < points.length - 1) out.push(points[points.length - 1]);
  return out;
}

function drawChart() {
  const wrap = $("chart-wrap");
  const tooltip = $("tooltip");
  const empty = $("chart-empty");
  const series = seriesInView();
  const width = Math.max(320, wrap.clientWidth || 900);
  const { H, pad } = CHART;
  const plotW = width - pad.left - pad.right;
  const plotH = H - pad.top - pad.bottom;

  empty.style.display = series.length ? "none" : "";
  wrap.querySelectorAll("svg, .hoverline").forEach((n) => n.remove());
  if (!series.length) return;

  // domain from merged timestamps (union across miners' sampling moments)
  let t0 = Infinity, t1 = -Infinity, vMax = -Infinity, vMin = Infinity;
  for (const s of series) {
    t0 = Math.min(t0, s.pts[0].t), t1 = Math.max(t1, s.pts[s.pts.length - 1].t);
    for (const p of s.pts) {
      vMax = Math.max(vMax, p.gh);
      vMin = Math.min(vMin, p.gh);
    }
  }
  // x-domain is the requested range, so the chart does not "zoom" between draws
  t0 = Math.min(t0, Date.now() - state.rangeHours * 3.6e6);
  t1 = Math.max(t1, Date.now());
  vMin = Math.min(vMin, 0);

  const x = (t) => pad.left + ((t - t0) / (t1 - t0 || 1)) * plotW;
  const y = (v) => pad.top + plotH - ((v - vMin) / (vMax - vMin || 1)) * plotH;

  const svg = el("svg", { viewBox: `0 0 ${width} ${H}`, role: "img", tabindex: "0" });
  svg.setAttribute("aria-label", "Farm hash rate over time per miner");

  // hairline grid: recessive, solid
  for (const tick of niceTicks(vMin, vMax)) {
    svg.appendChild(el("line", {
      x1: pad.left, x2: pad.left + plotW,
      y1: y(tick), y2: y(tick),
      stroke: "var(--grid)", "stroke-width": "1",
    }));
    svg.appendChild(textEl(pad.left - 8, y(tick) + 3, fmtNum(tick, 0), "end"));
  }

  // baseline: x axis with time labels
  svg.appendChild(el("line", {
    x1: pad.left, x2: pad.left + plotW, y1: pad.top + plotH, y2: pad.top + plotH,
    stroke: "var(--baseline)", "stroke-width": "1",
  }));
  const xTickCount = width < 500 ? 2 : 5;
  for (let i = 0; i <= xTickCount; i++) {
    const t = t0 + ((t1 - t0) * i) / xTickCount;
    // middle ticks drop the date on narrow screens so labels can't collide
    let lbl;
    if (state.rangeHours <= 1) lbl = fmtTime(t);
    else if (width < 500 && i !== 0 && i !== xTickCount) lbl = fmtTime(t);
    else lbl = `${fmtDateShort(t)} ${fmtTime(t)}`;
    // anchor the end ticks inward so the label never clips at the card edge
    const anchor = i === 0 ? "start" : i === xTickCount ? "end" : "middle";
    svg.appendChild(textEl(x(t), H - 6, lbl, anchor, "var(--text-muted)"));
  }

  // series: area wash + 2px line; end dot with 2px surface ring
  for (const s of series) {
    const color = seriesColor(s.id);
    const pts = downsample(s.pts, Math.floor(plotW));
    const linePt = pts.map((p) => `${x(p.t).toFixed(1)},${y(p.gh).toFixed(1)}`);
    const first = pts[0], last = pts[pts.length - 1];
    svg.appendChild(el("polygon", {
      points: `${x(first.t)},${pad.top + plotH} ${linePt.join(" ")} ${x(last.t)},${pad.top + plotH}`,
      fill: color, "fill-opacity": "0.10", stroke: "none",
    }));
    svg.appendChild(el("polyline", {
      points: linePt.join(" "), fill: "none", stroke: color,
      "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round",
    }));
    svg.appendChild(el("circle", {
      cx: x(last.t), cy: y(last.gh), r: "4", fill: color,
      stroke: "var(--surface-1)", "stroke-width": "2",
    }));
  }

  // hover layer: crosshair + one tooltip listing every series
  const crosshair = el("line", {
    y1: pad.top, y2: pad.top + plotH, stroke: "var(--baseline)",
    "stroke-width": "1", visibility: "hidden",
  });
  svg.appendChild(crosshair);
  wrap.appendChild(svg);

  const byTime = series.map((s) => s.pts);
  function hoverAt(px) {
    if (!byTime.length) return;
    // find the point per series nearest the pointer's X
    const rows = [];
    let bestT = null;
    for (const pts of byTime) {
      let best = null, bestDist = Infinity;
      for (const p of pts) {
        const d = Math.abs(x(p.t) - px);
        if (d < bestDist) { bestDist = d; best = p; }
      }
      if (best && bestDist < 40) { rows.push(best); if (bestT === null || Math.abs(best.t - px) < Math.abs(bestT - px)) bestT = best.t; }
    }
    if (!rows.length) { hideTooltip(); return; }

    crosshair.setAttribute("x1", x(bestT));
    crosshair.setAttribute("x2", x(bestT));
    crosshair.setAttribute("visibility", "visible");

    tooltip.textContent = "";
    const timeEl = document.createElement("div");
    timeEl.className = "tt-time";
    timeEl.textContent = `${fmtDateShort(bestT)} ${fmtTimeFull(bestT)}`;
    tooltip.appendChild(timeEl);
    for (const p of rows) {
      const s = series.find((s) => s.pts.includes(p));
      const row = document.createElement("div");
      row.className = "tt-row";
      const key = document.createElement("span");
      key.className = "linekey";
      key.style.background = seriesColor(s.id);
      const name = document.createElement("span");
      name.textContent = minerName(s.id);
      const val = document.createElement("span");
      val.className = "tt-val";
      val.textContent = `${fmtNum(p.gh, 1)} GH/s`;
      row.append(key, name, val);
      tooltip.appendChild(row);
    }
    positionTooltip(px, tooltip, wrap);
    tooltip.classList.add("show");
  }
  function hideTooltip() {
    tooltip.classList.remove("show");
    tooltip.style.left = "0px"; // drop the parked position so it can't overflow the page
    crosshair.setAttribute("visibility", "hidden");
  }

  svg.addEventListener("pointermove", (e) => {
    const rect = svg.getBoundingClientRect();
    hoverAt(((e.clientX - rect.left) / rect.width) * width);
  });
  svg.addEventListener("pointerleave", hideTooltip);
  // keyboard: focus shows the last point; arrows walk the timeline
  let kbIndex = -1;
  svg.addEventListener("focus", () => { kbIndex = byTime[0] ? byTime[0].length - 1 : -1; if (kbIndex >= 0) hoverAt(x(byTime[0][kbIndex].t)); });
  svg.addEventListener("keydown", (e) => {
    if (!byTime.length) return;
    if (e.key === "ArrowLeft") kbIndex = Math.max(0, kbIndex - 1);
    else if (e.key === "ArrowRight") kbIndex = Math.min(byTime[0].length - 1, kbIndex + 1);
    else return;
    e.preventDefault();
    hoverAt(x(byTime[0][kbIndex].t));
  });
  svg.addEventListener("blur", hideTooltip);
}

function textEl(cx, cyLabel, str, anchor, fill) {
  const t = el("text", {
    x: cx, y: cyLabel, "text-anchor": anchor || "start",
    "font-size": "10", fill: fill || "var(--text-muted)",
  });
  t.textContent = str;
  return t;
}

function positionTooltip(px, tooltip, wrap) {
  const twrap = wrap.getBoundingClientRect();
  tooltip.style.top = "8px";
  tooltip.style.left = "8px";
  const tw = tooltip.offsetWidth;
  const frac = px / twrap.width;
  let left = frac * twrap.width + 14;
  if (left + tw + 8 > twrap.width) left = frac * twrap.width - tw - 14;
  tooltip.style.left = `${Math.max(4, left)}px`;
}

function minerName(id) {
  const m = state.miners.find((m) => m.id === id);
  return (m && m.model) || id;
}

/* legend: present for 2+ series; identity mirrored from the chart marks */
function renderLegend() {
  const legend = $("chart-legend");
  legend.textContent = "";
  const series = seriesInView();
  if (series.length < 2) return;
  for (const s of series) {
    const key = document.createElement("span");
    key.className = "key";
    const swatch = document.createElement("span");
    swatch.className = "swatch";
    swatch.style.background = seriesColor(s.id);
    const name = document.createElement("span");
    name.textContent = minerName(s.id);
    const current = document.createElement("span");
    current.className = "current";
    current.textContent = `${fmtNum(s.pts[s.pts.length - 1].gh, 1)} GH/s`;
    key.append(swatch, name, current);
    legend.appendChild(key);
  }
}

/* table view: the chart's accessibility twin */
function renderTable() {
  const head = $("chart-table-head");
  const body = $("chart-table-body");
  head.textContent = "";
  body.textContent = "";
  const series = seriesInView();
  if (!series.length) return;

  const trh = document.createElement("tr");
  const th0 = document.createElement("th");
  th0.textContent = "Time";
  trh.appendChild(th0);
  for (const s of series) {
    const th = document.createElement("th");
    th.textContent = `${minerName(s.id)} (GH/s)`;
    trh.appendChild(th);
  }
  head.appendChild(trh);

  // index rows on the union of timestamps; show the most recent MAX_TABLE_ROWS
  const byT = new Map();
  for (const s of series) {
    for (const p of s.pts) {
      if (!byT.has(p.t)) byT.set(p.t, {});
      byT.get(p.t)[s.id] = p.gh;
    }
  }
  const times = [...byT.keys()].sort((a, b) => a - b).slice(-MAX_TABLE_ROWS);
  for (const t of times) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.textContent = `${fmtDateShort(t)} ${fmtTimeFull(t)}`;
    tr.appendChild(td);
    for (const s of series) {
      const td2 = document.createElement("td");
      const v = byT.get(t)[s.id];
      td2.textContent = v === undefined ? "" : fmtNum(v, 1);
      tr.appendChild(td2);
    }
    body.appendChild(tr);
  }
}

/* ---------- miner cards ---------- */

const cardByMiner = new Map();

function renderMiners(miners) {
  const grid = $("miner-grid");
  $("miners-empty").style.display = miners.length ? "none" : "";

  for (const m of miners) {
    let card = cardByMiner.get(m.id);
    if (!card) {
      card = buildMinerCard(m);
      cardByMiner.set(m.id, card);
      grid.appendChild(card);
    }
    updateMinerCard(card, m);
  }
}

function buildMinerCard(m) {
  const card = document.createElement("div");
  card.className = "miner-card";
  card.dataset.miner = m.id;

  const top = document.createElement("div");
  top.className = "top";
  const left = document.createElement("div");
  const name = document.createElement("div");
  name.className = "name";
  name.textContent = m.model || m.id;
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = m.ip || "";
  if (m.firmware) meta.textContent += ` · ${m.firmware}`;
  left.append(name, meta);
  const chip = document.createElement("span");
  chip.className = "status-chip";
  chip.appendChild(Object.assign(document.createElement("span"), { className: "dot" }));
  const chipLabel = document.createElement("span");
  chip.appendChild(chipLabel);
  // Mining paused (device-reported): separate tag, not color alone.
  const pausedChip = document.createElement("span");
  pausedChip.className = "paused-chip";
  pausedChip.textContent = "Paused";
  pausedChip.hidden = true;
  top.append(left, chip, pausedChip);

  const spark = document.createElement("sparkline");
  spark.style.height = "34px";
  spark.style.display = "block";

  const stats = document.createElement("div");
  stats.className = "miner-stats";

  const gh = statBlock("Hash rate", "GH/s");
  const temp = tempMeter();
  const power = statBlock("Power", "W");
  const fan = statBlock("Fan", "%");
  const shares = {
    root: div("stat"),
    label: div("sl"), value: div("sv"), sub: div("sub"),
  };
  shares.root.append(shares.label, shares.value, shares.sub);
  shares.label.textContent = "Shares accepted";
  const uptime = statBlock("Uptime", "");

  stats.append(gh.root, temp.root, power.root, fan.root, shares.root, uptime.root);

  const pool = document.createElement("div");
  pool.className = "meta";
  pool.textContent = "";

  // Control row: only visible when the server reports write access.
  const actions = document.createElement("div");
  actions.className = "miner-actions";
  const pauseBtn = document.createElement("button");
  pauseBtn.className = "ctl-btn";
  pauseBtn.type = "button";
  pauseBtn.textContent = "Pause";
  const settingsBtn = document.createElement("button");
  settingsBtn.className = "ctl-btn subtle";
  settingsBtn.type = "button";
  settingsBtn.textContent = "Settings";
  actions.append(pauseBtn, settingsBtn);
  pauseBtn.addEventListener("click", () => togglePause(m.id));
  settingsBtn.addEventListener("click", () => openSettingsDialog(m.id));

  card.append(top, spark, stats, pool, actions);
  card._refs = {
    name, meta, chip, chipLabel, pausedChip, spark, gh, temp, power, fan,
    shares, uptime, pool, actions, pauseBtn, settingsBtn,
  };
  return card;
}

function statBlock(label, unit) {
  const root = div("stat");
  const labelEl = div("sl");
  const value = div("sv");
  labelEl.textContent = label;
  if (unit) {
    const u = document.createElement("span");
    u.className = "sunit";
    u.textContent = unit;
    value.appendChild(u);
  }
  root.append(labelEl, value);
  return { root, value, valEl: value };
}

function tempMeter() {
  const root = div("stat meter-row");
  const label = div("sl");
  label.textContent = "Temperature";
  const valueWrap = div("sv");
  valueWrap.textContent = "—";
  const meter = div("meter");
  const fill = document.createElement("span");
  fill.className = "fill";
  meter.appendChild(fill);
  root.append(label, valueWrap, meter);
  return { root, value: valueWrap, meter, fill };
}

function div(cls) {
  const d = document.createElement("div");
  d.className = cls;
  return d;
}

/* Set a stat's value + unit; rebuilding both keeps the unit alive across updates. */
function setVal(target, text, unit) {
  target.textContent = "";
  const t = document.createElement("span");
  t.textContent = text;
  target.appendChild(t);
  if (unit) {
    const u = document.createElement("span");
    u.className = "sunit";
    u.textContent = unit;
    target.appendChild(u);
  }
}

function updateMinerCard(card, m) {
  const rf = card._refs;
  rf.name.textContent = m.model || m.id;
  rf.meta.textContent = m.ip || "";
  if (m.firmware) rf.meta.textContent += ` · ${m.firmware}`;

  // Control row exists on every card; the server decides whether it shows.
  const controlled = state.controlEnabled && m.status === "online";
  card.classList.toggle("controlled", controlled);
  rf.pauseBtn.hidden = !controlled;
  rf.settingsBtn.hidden = !controlled;

  // Status: dot + label (never color alone).
  rf.chip.className = `status-chip ${m.status || "offline"}`;
  rf.chipLabel.textContent =
    m.status === "online" ? "Online" : m.status === "warning" ? "Warning" : "Offline";
  const paused = !!(m.stats && m.stats.paused);
  rf.pausedChip.hidden = !paused;
  if (paused) {
    // The device still serves stats, so "Online" alone would mislead.
    rf.pauseBtn.textContent = "Resume";
  } else {
    rf.pauseBtn.textContent = "Pause";
  }

  if (m.pool_url) rf.pool.textContent = `pool ${m.pool_url}`;

  if (!m.stats) {
    setVal(rf.gh.valEl, "—", "");
    rf.temp.value.textContent = "—";
    setVal(rf.power.valEl, "—", "");
    setVal(rf.fan.valEl, "—", "");
    rf.shares.value.textContent = "—";
    rf.uptime.valEl.textContent = "—";
    return;
  }
  const s = m.stats;
  setVal(rf.gh.valEl, fmtHashrateGHz(s.hash_rate_gh), hashrateUnit(s.hash_rate_gh));

  // Temperature meter: severity fill; label + value carry the number.
  const temp = s.temperature_c || 0;
  rf.temp.value.textContent = `${fmtNum(temp, 1)}°C`;
  rf.temp.fill.style.width = `${Math.min(100, (temp / 90) * 100)}%`;
  const meterEl = rf.temp.meter;
  let level = "";
  if (temp >= 85) level = "critical";
  else if (temp >= 75) level = "hot";       // serious
  else if (temp >= 60) level = "warm";      // warning
  meterEl.className = `meter${level ? " " + level : ""}`;

  setVal(rf.power.valEl, fmtNum(s.power_w, 1), "W");
  setVal(rf.fan.valEl, s.fan_speed > 0 ? String(s.fan_speed) : "—", "%");
  rf.shares.value.textContent = s.accepted.toLocaleString();
  const total = (s.accepted || 0) + (s.rejected || 0);
  rf.shares.sub.textContent =
    total > 0 ? `${fmtNum(((s.rejected || 0) / total) * 100, 2)}% rejected` : "";
  rf.uptime.valEl.textContent = fmtUptime(s.uptime_seconds || 0);

  // sparkline from the rolling SSE buffer
  const buf = state.series.get(m.id) || [];
  renderSparkline(rf.spark, buf.slice(-12), seriesColor(m.id));
}

/* ---------- SSE ---------- */

let lastFarmRenderHash = "";
function onSnapshot(msg) {
  state.farm = msg.farm;
  state.miners = msg.miners || [];
  // Write access is server-reported: read-only until /api/stats says control is on.
  state.controlEnabled = !!(state.farm && state.farm.control_enabled);
  const now = Date.now();

  // farm sparkline (rolling)
  if (state.farm && (state.farm.total_hashrate_gh || state.farm.total_hashrate_gh === 0)) {
    pushPoint(state.farmSpark, now, state.farm.total_hashrate_gh, 24);
  }

  // per-miner rolling buffers for the chart + card sparklines
  let chartDirty = false;
  for (const m of state.miners) {
    if (!m.stats) continue;
    const t = Date.parse(m.stats.timestamp);
    if (Number.isNaN(t)) continue;
    const buf = state.series.get(m.id) || [];
    if (pushPoint(buf, t, m.stats.hash_rate_gh, sparkCaps)) {
      state.series.set(m.id, buf);
    }
    chartDirty = true;
  }

  setConnection(true);
  renderFarm(state.farm, state.miners);
  renderMiners(state.miners);
  if (chartDirty) {
    renderSparkline($("kpi-hashrate-spark"), state.farmSpark.slice(-12), "var(--series-1)");
    drawChart();
    renderLegend();
    renderTable();
  }
}

function setConnection(up) {
  state.connected = up;
  const conn = $("connection");
  const label = $("connection-label");
  if (up) {
    conn.className = "connection live";
    label.textContent = "Live";
  } else {
    conn.className = "connection stale";
    label.textContent = state.everConnected ? "Reconnecting…" : "Connecting…";
  }
}

function startSSE() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    try {
      state.everConnected = true;
      onSnapshot(JSON.parse(e.data));
    } catch (err) {
      console.error("bad event payload", err);
    }
  };
  es.onerror = () => setConnection(false); // EventSource retries on its own
}

/* ---------- range filter ---------- */

async function loadHistory(hours) {
  const miners = state.miners.length ? state.miners : null;
  const wrapCutoff = Date.now() - hours * 3.6e6;
  if (!miners) {
    const res = await fetch("/api/miners");
    if (!res.ok) return;
    const body = await res.json();
    state.miners = body.miners || [];
  }
  const bufs = await Promise.all(state.miners.map(async (m) => {
    const res = await fetch(`/api/miners/${encodeURIComponent(m.id)}/history?hours=${state.rangeHours}`);
    if (!res.ok) return [];
    const body = await res.json();
    const cutoff = wrapCutoff;
    return (body.data || [])
      .filter((d) => d.hash_rate_gh > 0)
      .map((d) => ({ t: Date.parse(d.timestamp), gh: d.hash_rate_gh }))
      .filter((p) => p.t >= cutoff);
  }));
  // insert in miners order — Promise.all settles out of order, and the series
  // Map's insertion order is the chart/legend draw order
  state.miners.forEach((m, i) => state.series.set(m.id, bufs[i]));
  drawChart();
  renderLegend();
  renderTable();
  renderMiners(state.miners);
}

const rangeBtns = [...document.querySelectorAll(".range-btn")];
rangeBtns.forEach((btn) => {
  btn.addEventListener("click", () => {
    rangeBtns.forEach((b) => b.setAttribute("aria-pressed", String(b === btn)));
    state.rangeHours = parseInt(btn.dataset.hours, 10);
    loadHistory(state.rangeHours);
  });
});

/* table toggle */
$("table-toggle").addEventListener("click", () => {
  const show = !$("chart-table-view").classList.contains("show");
  $("chart-table-view").classList.toggle("show", show);
  $("table-toggle").setAttribute("aria-pressed", String(show));
  $("table-toggle").textContent = show ? "View as chart" : "View as table";
});

/* re-draw on resize / theme repaint */
let resizeTimer = null;
window.addEventListener("resize", () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => { drawChart(); }, 150);
});

/* ---------- boot ---------- */

(async function boot() {
  setConnection(false);
  try {
    const res = await fetch("/api/miners");
    if (res.ok) {
      const body = await res.json();
      state.miners = body.miners || [];
      renderMiners(state.miners);
    }
    const statsRes = await fetch("/api/stats");
    if (statsRes.ok) {
      state.farm = await statsRes.json();
      renderFarm(state.farm, state.miners);
    }
  } catch (err) {
    console.error("initial load failed", err);
  }
  loadHistory(state.rangeHours);
  startSSE();
})();
/* ---------- alerts bell + panel ---------- */

const ALERTS_SEEN_KEY = "homehash.alertsSeen";
const alertsUi = { items: [], open: false };

function seenCutoff() {
  // Per-viewer convenience only: which alert the viewer last read.
  try { return localStorage.getItem(ALERTS_SEEN_KEY) || ""; }
  catch (err) { return ""; }
}

function markAlertsSeen() {
  // newest item's timestamp becomes the read marker (empty list = nothing to mark)
  if (!alertsUi.items.length) return;
  try { localStorage.setItem(ALERTS_SEEN_KEY, alertsUi.items[0].time); }
  catch (err) { /* badge just stays as-is without storage */ }
}

function kindLabel(kind) {
  switch (kind) {
    case "offline": return "Offline";
    case "temperature": return "Temperature";
    case "recovered": return "Recovered";
    case "temp-recovered": return "Recovered";
    default: return kind;
  }
}

function fmtAlertTime(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (d.toDateString() === new Date().toDateString()) return time;
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + time;
}

function updateAlertBadge() {
  const badge = $("alerts-badge");
  if (alertsUi.open) {
    markAlertsSeen();
  }
  const cutoff = seenCutoff();
  const unread = alertsUi.items.filter((a) => a.time > cutoff).length;
  if (!unread || alertsUi.open) {
    badge.hidden = true;
    badge.textContent = "";
    return;
  }
  badge.hidden = false;
  badge.textContent = unread > 9 ? "9+" : String(unread);
}

function renderAlerts() {
  const list = $("alerts-list");
  list.replaceChildren();
  $("alerts-empty").hidden = alertsUi.items.length > 0;
  alertsUi.items.forEach((a) => {
    const item = document.createElement("li");
    item.className = "alert-item " + a.level;

    const dot = document.createElement("span");
    dot.className = "alert-dot";

    const body = document.createElement("div");
    body.className = "alert-body";

    const what = document.createElement("span");
    what.className = "alert-what";
    what.textContent = (a.model || a.miner_ip || "homeHash") + " · " + kindLabel(a.kind);

    const msg = document.createElement("p");
    msg.className = "alert-msg";
    msg.textContent = a.message || "";

    body.append(what, msg);

    const when = document.createElement("span");
    when.className = "alert-when";
    when.textContent = fmtAlertTime(a.time);

    item.append(dot, body, when);
    list.append(item);
  });
}

async function refreshAlerts() {
  try {
    const res = await fetch("/api/alerts?limit=50");
    if (!res.ok) return;
    const body = await res.json();
    alertsUi.items = body.alerts || [];
    renderAlerts();
    updateAlertBadge();
  } catch (err) {
    console.error("alerts refresh failed", err);
  }
}

function setAlertsOpen(open) {
  alertsUi.open = open;
  const panel = $("alerts-panel");
  panel.hidden = !open;
  $("alerts-toggle").setAttribute("aria-expanded", String(open));
  if (open) updateAlertBadge(); // opening reads the panel: mark as seen
}

$("alerts-toggle").addEventListener("click", () => {
  setAlertsOpen(!alertsUi.open);
  if (alertsUi.open) refreshAlerts();
});

document.addEventListener("click", (e) => {
  if (alertsUi.open && !e.target.closest("#alerts-wrap")) setAlertsOpen(false);
});

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && alertsUi.open) {
    setAlertsOpen(false);
    $("alerts-toggle").focus();
  }
});

refreshAlerts();
setInterval(refreshAlerts, 30000);

/* ---------- miner control (settings, pause, resume) ---------- */

async function postJSON(path, body) {
  const opts = { method: "POST" };
  if (body !== undefined) {
    opts.headers = { "Content-Type": "application/json" };
    opts.body = JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(path, opts);
  } catch {
    throw new Error("network request failed");
  }
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    try {
      const data = await resp.json();
      if (data && data.error) msg = data.error;
    } catch { /* non-JSON body keeps the HTTP status line */ }
    throw new Error(msg);
  }
  return resp.json();
}

let toastTimer = null;
function showToast(text, isError) {
  const toast = $("toast");
  toast.textContent = text;
  toast.className = isError ? "error" : "ok";
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 3500);
}

async function togglePause(minerID) {
  const m = state.miners.find((x) => x.id === minerID);
  if (!m || !m.stats) return;
  const paused = !!(m.stats.paused);
  try {
    await postJSON(`/api/miners/${encodeURIComponent(minerID)}/${paused ? "resume" : "pause"}`);
    showToast(`${minerName(minerID)} mining ${paused ? "resumed" : "paused"}`);
  } catch (err) {
    showToast(`${minerName(minerID)}: ${err.message}`, true);
  }
}

/* Settings dialog: edit frequency / core voltage, preview old -> new, apply */
let dlgMinerID = null;

function dlgParse(id) {
  const raw = $(id).value.trim();
  if (raw === "") return null;
  const n = Number(raw);
  return Number.isFinite(n) ? n : NaN;
}

function updateDlgPreview() {
  const m = state.miners.find((x) => x.id === dlgMinerID);
  if (!m || !m.stats) return;
  const parts = [];
  const oldF = m.stats.frequency;
  const newF = dlgParse("dlg-frequency");
  if (oldF > 0 && newF !== null && newF !== oldF) {
    parts.push(`frequency ${fmtNum(oldF, 0)} → ${fmtNum(newF, 1)} MHz`);
  }
  const oldV = m.stats.core_voltage_mv;
  const newV = dlgParse("dlg-voltage");
  if (oldV > 0 && newV !== null && newV !== oldV) {
    parts.push(`voltage ${oldV} → ${fmtNum(newV, 0)} mV`);
  }
  $("dlg-preview").textContent =
    parts.length ? `Change will apply live, no restart: ${parts.join(", ")}` : "";
}

function openSettingsDialog(minerID) {
  const m = state.miners.find((x) => x.id === minerID);
  if (!m || !m.stats) return;
  dlgMinerID = minerID;
  $("dlg-miner-meta").textContent = `${minerName(minerID)} · ${m.ip}`;
  const s = m.stats;
  const f = $("dlg-frequency");
  const v = $("dlg-voltage");
  f.value = s.frequency > 0 ? String(Math.round(s.frequency)) : "";
  f.placeholder = s.frequency > 0 ? String(Math.round(s.frequency)) : "unknown";
  v.value = s.core_voltage_mv > 0 ? String(s.core_voltage_mv) : "";
  v.placeholder = s.core_voltage_mv > 0 ? String(s.core_voltage_mv) : "unknown";
  $("dlg-error").hidden = true;
  updateDlgPreview();
  $("miner-dialog").showModal();
  f.focus();
}

async function applySettingsDialog() {
  const errEl = $("dlg-error");
  errEl.hidden = true;
  const body = {};
  const newF = dlgParse("dlg-frequency");
  const newV = dlgParse("dlg-voltage");
  if (Number.isNaN(newF) || Number.isNaN(newV)) {
    errEl.textContent = "Values must be numbers.";
    errEl.hidden = false;
    return;
  }
  if (newF === null && newV === null) {
    errEl.textContent = "Nothing to change — adjust a value first.";
    errEl.hidden = false;
    return;
  }
  if (newF !== null) body.frequency = newF;
  if (newV !== null) body.core_voltage = newV;
  const btn = $("dlg-apply");
  btn.disabled = true;
  try {
    await postJSON(`/api/miners/${encodeURIComponent(dlgMinerID)}/settings`, body);
    $("miner-dialog").close();
    showToast(`${minerName(dlgMinerID)}: settings applied (applies live, may ramp over ~1 min)`);
    dlgMinerID = null;
  } catch (err) {
    errEl.textContent = err.message;
    errEl.hidden = false;
  } finally {
    btn.disabled = false;
  }
}

$("dlg-frequency").addEventListener("input", updateDlgPreview);
$("dlg-voltage").addEventListener("input", updateDlgPreview);
$("dlg-cancel").addEventListener("click", () => {
  $("miner-dialog").close();
  dlgMinerID = null;
});
$("miner-dialog").addEventListener("close", () => { dlgMinerID = null; });
$("dlg-apply").addEventListener("click", () => applySettingsDialog());
