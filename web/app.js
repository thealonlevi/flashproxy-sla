// Status page: per product, show the BEST vantage by default, with chips to
// switch vantages. One /api/overview call drives the top section.
let overview = null;
const vantageSel = {}; // package -> chosen vantage (sticky across refreshes)
let selected = null; // package shown in the latency chart
let curMin = 60;

function fmt(v) { return (v == null || isNaN(v)) ? "—" : Math.round(v); }
function shortV(v) { return v.replace(/^aws-/, ""); }

function vantageOf(c) {
  const want = vantageSel[c.package] || c.default_vantage;
  return c.vantages.find((v) => v.vantage === want) ||
         c.vantages.find((v) => v.vantage === c.default_vantage) ||
         c.vantages[0];
}

function barTitle(b) {
  const t = new Date(b.t * 1000).toLocaleTimeString();
  if (b.status === "no_data" || !b.samples) return t + " · no data";
  return `${t} · ${b.status} · median ${Math.round(b.median)}ms · ${Math.round(b.success_pct)}% ok · n=${Math.round(b.samples)}`;
}

async function loadOverview() {
  let d;
  try { d = await (await fetch("api/overview")).json(); } catch (e) { return; }
  overview = d;
  document.getElementById("updated").textContent =
    "updated " + new Date(d.generated_at).toLocaleTimeString();

  const banner = document.getElementById("banner");
  banner.dataset.status = d.overall.status;
  document.getElementById("banner-text").textContent = d.overall.label;

  renderComponents();
  if (!selected && d.components && d.components.length) selectComponent(d.components[0].package);
}

function renderComponents() {
  const d = overview;
  if (!d) return;
  const win = d.window_minutes || 90;
  const host = document.getElementById("components");
  host.innerHTML = "";

  (d.components || []).forEach((c) => {
    const v = vantageOf(c);
    vantageSel[c.package] = v.vantage;

    const row = document.createElement("div");
    row.className = "comp" + (c.package === selected ? " sel" : "");
    row.dataset.pkg = c.package;

    const bars = v.bars.map((b) =>
      `<i class="bar" data-status="${b.status}" title="${barTitle(b).replace(/"/g, "&quot;")}"></i>`
    ).join("");

    // vantage chips (best marked, selected highlighted)
    const chips = c.vantages.map((vv) => {
      const cls = "vchip" + (vv.vantage === v.vantage ? " sel" : "") +
                  (vv.vantage === c.default_vantage ? " best" : "");
      const med = vv.status === "no_data" ? "—" : fmt(vv.connect_ms_median) + "ms";
      const star = vv.vantage === c.default_vantage ? "★ " : "";
      return `<button class="${cls}" data-pkg="${c.package}" data-v="${vv.vantage}" data-status="${vv.status}">${star}${shortV(vv.vantage)} ${med}</button>`;
    }).join("");

    row.innerHTML =
      `<div class="comp-top">` +
        `<span class="comp-name">${c.package}</span>` +
        `<span class="comp-stat" data-status="${v.status}">${v.status.replace("_", " ")}</span>` +
      `</div>` +
      `<div class="metric"><span class="big">${fmt(v.connect_ms_median)}</span>` +
        `<span class="unit">ms median connect · via ${shortV(v.vantage)}</span></div>` +
      `<div class="vchips">${chips}</div>` +
      `<div class="bars">${bars}</div>` +
      `<div class="comp-bot">` +
        `<span class="left">${win}m ago</span>` +
        `<span class="mid">avg ${fmt(v.connect_ms_avg)}ms · p95 ${fmt(v.connect_ms_p95)}ms · ` +
          `${(v.success_pct ?? 0).toFixed(1)}% ok · n=${fmt(v.samples)}/10m</span>` +
        `<span class="right">uptime ${(v.uptime_pct ?? 100).toFixed(1)}% · now</span>` +
      `</div>`;

    row.onclick = () => selectComponent(c.package);
    host.appendChild(row);
  });

  // chip clicks switch vantage without selecting the component for the chart
  host.querySelectorAll(".vchip").forEach((b) => {
    b.onclick = (e) => {
      e.stopPropagation();
      vantageSel[b.dataset.pkg] = b.dataset.v;
      renderComponents();
      if (b.dataset.pkg === selected) loadSeries();
    };
  });
}

function selectComponent(pkg) {
  selected = pkg;
  document.querySelectorAll(".comp").forEach((el) => el.classList.toggle("sel", el.dataset.pkg === pkg));
  loadSeries();
}

async function loadSeries() {
  if (!selected) return;
  const v = vantageSel[selected] || "";
  document.getElementById("metrics-title").textContent = `connect latency · ${selected} · via ${shortV(v)}`;
  let d;
  try {
    d = await (await fetch(`api/series?package=${encodeURIComponent(selected)}&minutes=${curMin}&vantage=${encodeURIComponent(v)}`)).json();
  } catch (e) { return; }
  drawChart(d.points || []);
  loadScenarios(selected, v);
}

const SCN_ORDER = ["ping", "connect", "streaming", "large_object", "hifreq_small", "scraping", "long_session"];
const SCN_LABEL = {
  ping: "gateway ping", connect: "connect", streaming: "streaming", large_object: "large object",
  hifreq_small: "hi-freq small", scraping: "scraping", long_session: "long session",
};
const SCN_KPI = {
  ping: (s) => `${fmt(s.connect_ms_median)}ms rtt`,
  connect: (s) => `${fmt(s.connect_ms_median)}ms connect`,
  streaming: (s) => `${fmt(s.throughput_mbps_avg)} Mbps`,
  large_object: (s) => `${fmt(s.ttfb_ms_median)}ms ttfb`,
  hifreq_small: (s) => `${fmt(s.connect_ms_median)}ms/conn · ${fmt(s.success_pct)}%`,
  scraping: (s) => `${fmt(s.connect_ms_median)}ms median`,
  long_session: (s) => `${fmt(s.success_pct)}% held · ${fmt(s.total_ms_median / 1000)}s`,
};

async function loadScenarios(pkg, vantage) {
  let d;
  try {
    d = await (await fetch(`api/scenarios?package=${encodeURIComponent(pkg)}&vantage=${encodeURIComponent(vantage)}`)).json();
  } catch (e) { return; }
  const by = {};
  (d.scenarios || []).forEach((s) => { by[s.scenario] = s; });
  const host = document.getElementById("scenarios");
  host.innerHTML = SCN_ORDER.map((name) => {
    const s = by[name];
    const kpi = s && s.samples ? SCN_KPI[name](s) : "— no data";
    const ok = s && s.samples ? (s.success_pct >= 98 ? "op" : (s.success_pct >= 90 ? "deg" : "dn")) : "nd";
    const n = s && s.samples ? `n=${fmt(s.samples)}` : "";
    return `<div class="scn" data-st="${ok}"><div class="scn-name">${SCN_LABEL[name]}</div>` +
           `<div class="scn-kpi">${kpi}</div><div class="scn-n">${n}</div></div>`;
  }).join("");
}

function drawChart(points) {
  const svg = document.getElementById("chart");
  const W = 880, H = 220, pad = 38;
  if (!points.length) {
    svg.innerHTML = `<text x="${W / 2}" y="${H / 2}" fill="#9aa195" text-anchor="middle" font-size="12">no data yet</text>`;
    document.getElementById("chart-legend").innerHTML = "&nbsp;";
    return;
  }
  const xs = points.map((p) => +p.t);
  const ys = points.map((p) => +p.median);
  const minX = Math.min(...xs), maxX = Math.max(...xs);
  const maxY = Math.max(10, Math.ceil(Math.max(...ys) * 1.25));
  const X = (t) => pad + (maxX === minX ? 0 : (t - minX) / (maxX - minX)) * (W - 2 * pad);
  const Y = (v) => H - pad - (v / maxY) * (H - 2 * pad);
  const line = points.map((p, i) => (i ? "L" : "M") + X(+p.t).toFixed(1) + " " + Y(+p.median).toFixed(1)).join(" ");
  const mid = maxY / 2;

  svg.innerHTML =
    `<line x1="${pad}" y1="${Y(0)}" x2="${W - pad}" y2="${Y(0)}" stroke="#d7dcd2"/>` +
    `<line x1="${pad}" y1="${Y(mid)}" x2="${W - pad}" y2="${Y(mid)}" stroke="#eceee9"/>` +
    `<line x1="${pad}" y1="${Y(maxY)}" x2="${W - pad}" y2="${Y(maxY)}" stroke="#eceee9"/>` +
    `<text x="6" y="${Y(maxY) + 4}" fill="#9aa195" font-size="10">${maxY}ms</text>` +
    `<text x="6" y="${Y(mid) + 4}" fill="#9aa195" font-size="10">${Math.round(mid)}</text>` +
    `<text x="6" y="${Y(0) + 4}" fill="#9aa195" font-size="10">0</text>` +
    `<path d="${line}" fill="none" stroke="#2f9e44" stroke-width="1.5"/>`;

  const last = points[points.length - 1];
  document.getElementById("chart-legend").textContent =
    `median connect-ms · ${points.length} × 1-min buckets · latest ${Math.round(+last.median)}ms`;
}

async function loadMeta() {
  let d;
  try { d = await (await fetch("api/meta")).json(); } catch (e) { return; }
  const p = d.public_clickhouse || {};
  document.getElementById("public-box").innerHTML =
    `Query the same data this page is built from — ClickHouse HTTP, read-only, capped concurrency:<br><br>` +
    `host&nbsp;&nbsp;&nbsp; <b>${p.url || "—"}</b><br>` +
    `db&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; <b>${p.db || "—"}</b><br>` +
    `user&nbsp;&nbsp;&nbsp; <b>${p.user || "—"}</b><br>` +
    `pass&nbsp;&nbsp;&nbsp; <b>${p.password || "—"}</b><br><br>` +
    `<span class="muted">${p.note || ""}</span>`;
}

document.querySelectorAll("#ranges button").forEach((b) => {
  b.onclick = () => {
    document.querySelectorAll("#ranges button").forEach((x) => x.classList.remove("active"));
    b.classList.add("active");
    curMin = +b.dataset.min;
    loadSeries();
  };
});

loadMeta();
loadOverview();
setInterval(loadOverview, 15000);
setInterval(loadSeries, 30000);
