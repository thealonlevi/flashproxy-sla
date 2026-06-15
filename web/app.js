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
  document.getElementById("metrics-title").textContent = `gateway ping vs proxy connect · ${selected} · via ${shortV(v)}`;
  let d;
  try {
    d = await (await fetch(`api/series?package=${encodeURIComponent(selected)}&minutes=${curMin}&vantage=${encodeURIComponent(v)}`)).json();
  } catch (e) { return; }
  drawChart(d.series || {});
  loadScenarios(selected, v);
}

const SERIES_STYLE = {
  connect: { label: "proxy connect", color: "#2f9e44" },
  ping: { label: "gateway ping", color: "#3b82f6" },
};

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
    const p = by[name];                  // via proxy
    const dr = by[name + "_direct"];     // direct baseline (no proxy)
    const have = p && p.samples;
    const kpi = have ? SCN_KPI[name](p) : "— no data";
    const ok = have ? (p.success_pct >= 98 ? "op" : (p.success_pct >= 90 ? "deg" : "dn")) : "nd";
    let sub = "";
    if (name !== "ping") {
      const haveD = dr && dr.samples;
      sub += `<div class="scn-direct">direct: ${haveD ? SCN_KPI[name](dr) : "—"}</div>`;
      // proxy overhead for latency-style scenarios (connect-ms based)
      if (have && haveD && name !== "streaming" && name !== "large_object") {
        const d = Math.round(p.connect_ms_median - dr.connect_ms_median);
        sub += `<div class="scn-delta">proxy ${d >= 0 ? "+" : ""}${d}ms</div>`;
      }
    }
    const nn = have ? `n=${fmt(p.samples)}` : "";
    return `<div class="scn" data-st="${ok}"><div class="scn-name">${SCN_LABEL[name]}</div>` +
           `<div class="scn-kpi">${kpi}</div>${sub}<div class="scn-n">${nn}</div></div>`;
  }).join("");
}

function drawChart(series) {
  const svg = document.getElementById("chart");
  const W = 880, H = 220, pad = 38;
  const names = Object.keys(series).filter((n) => series[n] && series[n].length);
  if (!names.length) {
    svg.innerHTML = `<text x="${W / 2}" y="${H / 2}" fill="#9aa195" text-anchor="middle" font-size="12">no data yet</text>`;
    document.getElementById("chart-legend").innerHTML = "&nbsp;";
    return;
  }
  const allT = [], allY = [];
  names.forEach((n) => series[n].forEach((p) => { allT.push(+p.t); allY.push(+p.median); }));
  const minX = Math.min(...allT), maxX = Math.max(...allT);
  const maxY = Math.max(10, Math.ceil(Math.max(...allY) * 1.25));
  const X = (t) => pad + (maxX === minX ? 0 : (t - minX) / (maxX - minX)) * (W - 2 * pad);
  const Y = (v) => H - pad - (v / maxY) * (H - 2 * pad);
  const mid = maxY / 2;

  const paths = names.map((n) => {
    const color = (SERIES_STYLE[n] || {}).color || "#888";
    const line = series[n].map((p, i) => (i ? "L" : "M") + X(+p.t).toFixed(1) + " " + Y(+p.median).toFixed(1)).join(" ");
    return `<path d="${line}" fill="none" stroke="${color}" stroke-width="1.5"/>`;
  }).join("");

  svg.innerHTML =
    `<line x1="${pad}" y1="${Y(0)}" x2="${W - pad}" y2="${Y(0)}" stroke="#d7dcd2"/>` +
    `<line x1="${pad}" y1="${Y(mid)}" x2="${W - pad}" y2="${Y(mid)}" stroke="#eceee9"/>` +
    `<line x1="${pad}" y1="${Y(maxY)}" x2="${W - pad}" y2="${Y(maxY)}" stroke="#eceee9"/>` +
    `<text x="6" y="${Y(maxY) + 4}" fill="#9aa195" font-size="10">${maxY}ms</text>` +
    `<text x="6" y="${Y(mid) + 4}" fill="#9aa195" font-size="10">${Math.round(mid)}</text>` +
    `<text x="6" y="${Y(0) + 4}" fill="#9aa195" font-size="10">0</text>` +
    paths;

  // legend: colored swatch + latest value per series
  document.getElementById("chart-legend").innerHTML = names.map((n) => {
    const st = SERIES_STYLE[n] || { label: n, color: "#888" };
    const pts = series[n];
    const last = pts[pts.length - 1];
    return `<span style="color:${st.color}">■</span> ${st.label} <b>${Math.round(+last.median)}ms</b>`;
  }).join(" &nbsp;&nbsp; ");
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
