# flashproxy-status

An open-source SLA status page for proxy services. It **actively simulates the
payload archetypes of real proxy users** and measures the connect latency they
experience, so degradation is detected before customers complain.

It is a **closed, fully reproducible system**: every number on the public page
was recorded by this app itself, into its *own* ClickHouse. Nothing is read from
any internal/production data source. Clone it, point it at any proxy, and you get
the same dashboard.

```
 origin ──▶ [proxy under test] ──▶ prober ──▶ collector ──▶ own ClickHouse ──▶ public page
   (deterministic upstream)      (synthetic   (ingest +        (system of      (status.* )
                                  scenarios)    SLO + alerts)    record)
```

The only thing crossing into the system from the outside is the **proxy endpoint
+ credentials** you put in config — an input, not a data source.

## Quick start

```bash
# Bring up ClickHouse + origin + collector + frontend (no real proxy needed):
docker compose up --build
# open http://localhost:8080  (the page renders; it shows "no data" until a prober runs)

# Add a prober against your own proxy to see live numbers:
ISP_PROXY_URL='http://USER:PASS@HOST:30' docker compose --profile demo up --build
```

The demo prober opens an HTTP CONNECT tunnel through your proxy to the bundled
origin every 15s and records `connect_ms`. Cards turn green and the chart fills in.

## What it measures

Per package, per vantage, the headline KPIs you asked any proxy SLA to have:

- **average connect-ms** and **median connect-ms** (the time the *proxy* takes to
  establish the upstream connection — `CONNECT` → `200`),
- success rate, p95 connect-ms, dial-ms (client→proxy, kept separate to localize
  regressions), and TTFB against the deterministic origin.

Connect latency is the Phase-1 scenario. The roadmap adds one scenario per
archetype (streaming/buffering, large-object, hi-freq small-payload bots, broad
scraping, long-maintained sessions) — each records the same `connect_ms` plus its
own KPI (throughput, setup rate, stability).

## Architecture

| Component | What it does |
|---|---|
| `cmd/origin` | Deterministic dual-stack upstream (`/connect`, `/bytes/{n}`, `/small`, `/hold`). Pure SLA signal, no third-party variance. |
| `cmd/prober` | Runs scenarios from one vantage (e.g. `us-east`, `eu-west`), ships results to the collector. The only component that touches a real proxy. |
| `cmd/collector` | Ingests results → its own ClickHouse, evaluates SLO, fires Discord alerts on status change, serves the JSON API + static page. Only ClickHouse writer. |
| `internal/chstore` | Stdlib-only ClickHouse client over the HTTP interface. The storage interface; swap for Postgres/SQLite. |
| `web/` | Framework-free status page. |

Dependency-free Go (stdlib only) — builds offline, trivially reproducible.

## Configuration

Configs are JSON with `${ENV}` expansion so secrets never live in the file.

- `config/collector.example.json` — listen addr, ingest token, ClickHouse creds, SLO thresholds, Discord webhook.
- `config/prober.example.json` — vantage id, collector URL, and the list of
  packages → `proxy_url` (e.g. `${ISP_PROXY_URL}`), the origin to CONNECT to, and IP version.

### Deploy/maintenance markers

`POST /api/events` records a self-owned marker (shown on the timeline):

```bash
curl -XPOST localhost:8080/api/events -H 'Authorization: Bearer <token>' \
  -d '{"type":"deploy","package":"isp","message":"deployed abc1234"}'
```

Wire your CI to this if you want deploy annotations — optional, no data dependency.

## Production shape (status.flashproxy.com)

- Probers on small VMs in **US and EU** (EU vantage maps to `isp_eu`); push to the collector over HTTPS with a bearer token.
- Dual-stack origin per region so `ipv6` / `ipv6-datacenter` exercise v6 egress.
- One synthetic monitoring user **per package** on the proxy side (exempt from
  data caps + connection limits; a recognizable username prefix so they're easy to
  filter out of billing/analytics).
- Front the collector with a tunnel/CDN; the public page is cacheable.

## Roadmap

- **Phase 1 (this scaffold):** connect scenario, US vantage, ClickHouse, status page, Discord alerts. ← average/median connect-ms end-to-end.
- **Phase 2:** streaming / large-object / hi-freq / scraping / long-session scenarios; EU vantage; throughput & stability KPIs.
- **Phase 3:** baseline-relative anomaly detection + error-budget / 90-day uptime, all from self-recorded history.
- **Phase 4:** full public frontend (per-archetype, vantage compare, incident timeline).
- **Phase 5:** Postgres/SQLite store backends, CI, published images.

## License

MIT — see [LICENSE](LICENSE).
