// Framework-free status page: overall banner + per-component uptime bars + a
// plain connect-latency chart. One /api/overview call drives the top section.
let selected = null;
let curMin = 60;

function fmt(v) { return (v == null || isNaN(v)) ? "—" : Math.round(v); }

function barTitle(b) {
  const t = new Date(b.t * 1000).toLocaleTimeString();
  if (b.status === "no_data" || !b.samples) return t + " · no data";
  return `${t} · ${b.status} · median ${Math.round(b.median)}ms · ${Math.round(b.success_pct)}% ok · n=${Math.round(b.samples)}`;
}

async function loadOverview() {
  let d;
  try { d = await (await fetch("api/overview")).json(); } catch (e) { return; }

  document.getElementById("updated").textContent =
    "updated " + new Date(d.generated_at).toLocaleTimeString();

  const banner = document.getElementById("banner");
  banner.dataset.status = d.overall.status;
  document.getElementById("banner-text").textContent = d.overall.label;

  const win = d.window_minutes || 90;
  const host = document.getElementById("components");
  host.innerHTML = "";
  (d.components || []).forEach((c) => {
    const row = document.createElement("div");
    row.className = "comp" + (c.package === selected ? " sel" : "");
    row.dataset.pkg = c.package;

    const bars = c.bars.map((b) =>
      `<i class="bar" data-status="${b.status}" title="${barTitle(b).replace(/"/g, "&quot;")}"></i>`
    ).join("");

    row.innerHTML =
      `<div class="comp-top">` +
        `<span class="comp-name">${c.package}</span>` +
        `<span class="comp-stat" data-status="${c.status}">${c.status.replace("_", " ")}</span>` +
      `</div>` +
      `<div class="bars">${bars}</div>` +
      `<div class="comp-bot">` +
        `<span class="left">${win}m ago</span>` +
        `<span class="mid">median ${fmt(c.connect_ms_median)}ms · p95 ${fmt(c.connect_ms_p95)}ms · ` +
          `${(c.success_pct ?? 0).toFixed(1)}% ok · n=${fmt(c.samples)}/10m</span>` +
        `<span class="right">uptime ${(c.uptime_pct ?? 100).toFixed(1)}% · now</span>` +
      `</div>`;

    row.onclick = () => selectComponent(c.package);
    host.appendChild(row);
  });

  if (!selected && d.components && d.components.length) {
    selectComponent(d.components[0].package);
  }
}

function selectComponent(pkg) {
  selected = pkg;
  document.querySelectorAll(".comp").forEach((el) =>
    el.classList.toggle("sel", el.dataset.pkg === pkg));
  document.getElementById("metrics-title").textContent = "connect latency · " + pkg;
  loadSeries();
}

async function loadSeries() {
  if (!selected) return;
  let d;
  try {
    d = await (await fetch(`api/series?package=${encodeURIComponent(selected)}&minutes=${curMin}`)).json();
  } catch (e) { return; }
  drawChart(d.points || []);
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
    // grid
    `<line x1="${pad}" y1="${Y(0)}" x2="${W - pad}" y2="${Y(0)}" stroke="#d7dcd2"/>` +
    `<line x1="${pad}" y1="${Y(mid)}" x2="${W - pad}" y2="${Y(mid)}" stroke="#eceee9"/>` +
    `<line x1="${pad}" y1="${Y(maxY)}" x2="${W - pad}" y2="${Y(maxY)}" stroke="#eceee9"/>` +
    // y labels
    `<text x="6" y="${Y(maxY) + 4}" fill="#9aa195" font-size="10">${maxY}ms</text>` +
    `<text x="6" y="${Y(mid) + 4}" fill="#9aa195" font-size="10">${Math.round(mid)}</text>` +
    `<text x="6" y="${Y(0) + 4}" fill="#9aa195" font-size="10">0</text>` +
    // series
    `<path d="${line}" fill="none" stroke="#2f9e44" stroke-width="1.5"/>`;

  const last = points[points.length - 1];
  document.getElementById("chart-legend").textContent =
    `median connect-ms · ${points.length} × 1-min buckets · latest ${Math.round(+last.median)}ms`;
}

document.querySelectorAll("#ranges button").forEach((b) => {
  b.onclick = () => {
    document.querySelectorAll("#ranges button").forEach((x) => x.classList.remove("active"));
    b.classList.add("active");
    curMin = +b.dataset.min;
    loadSeries();
  };
});

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

loadMeta();
loadOverview();
setInterval(loadOverview, 15000);
setInterval(loadSeries, 30000);
