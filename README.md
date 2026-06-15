# flashproxy-status

An open-source SLA status page for proxy services. It **actively simulates the
payload archetypes of real proxy users** and measures the connect latency they
experience, so degradation is detected before customers complain.

It is a **closed, fully reproducible system**: every number on the public page
was recorded by this app itself, into its *own* ClickHouse. Nothing is read from
any internal/production data source. Clone it, point it at any proxy, and you get
the same dashboard.

```
 origin ──▶ [proxy under test] ──▶ worker ──▶ own ClickHouse ──▶ website ──▶ public page
   (deterministic upstream)      (probes +     (system of       (read-only   (status.* )
                                  writes,        record)          renderer)
                                  writer role)
```

The only thing crossing into the system from the outside is the **proxy endpoint
+ credentials** you put in config — an input, not a data source. Read and write are
split by ClickHouse role, so you can run **N workers** (e.g. one VM per region, or
per package) all writing to one ClickHouse, with a read-only website on top.

## Quick start

```bash
# Bring up ClickHouse + origin + website (no real proxy needed):
docker compose up --build
# open http://localhost:8080  (renders; shows "no data" until a worker runs)

# Add a worker against your own proxy to see live numbers:
ISP_PROXY_URL='http://USER:PASS@HOST:30' docker compose --profile demo up --build
```

The demo worker opens an HTTP CONNECT tunnel through your proxy every 15s and
records `connect_ms` directly into ClickHouse. Cards turn green and the chart fills.

## What it measures

Per package, per vantage:

- **average connect-ms** and **median connect-ms** (the time the *proxy* takes to
  establish the upstream connection — `CONNECT` → `200`),
- success rate, p95 connect-ms, dial-ms (client→proxy, kept separate to localize
  regressions), and TTFB against the deterministic origin.

Connect latency is the first scenario; the roadmap adds one per archetype
(streaming/buffering, large-object, hi-freq small-payload bots, broad scraping,
long-maintained sessions) — each recording the same `connect_ms` plus its own KPI.

## Architecture

| Component | What it does |
|---|---|
| `cmd/origin` | Deterministic dual-stack upstream (`/connect`, `/bytes/{n}`, `/small`, `/hold`). Pure SLA signal, no third-party variance. |
| `cmd/worker` | Runs scenarios from one vantage, writes results **directly** to ClickHouse as the `sla_writer` role. The only component that touches a real proxy. Run as many as you like. Set `"monitor": true` on one to also evaluate SLO + fire Discord alerts. |
| `cmd/website` | **Read-only.** Serves the JSON API + status page as the `sla_reader` role; never writes. Publishes the public read-only key at `/api/meta`. |
| `internal/{probe,slo,chstore,model}` | Scenarios, SLO evaluation, stdlib-only ClickHouse HTTP client, shared types. |
| `web/` | Framework-free console-style status page. |

Dependency-free Go (stdlib only) — builds offline, trivially reproducible.

## ClickHouse roles

`schema/clickhouse.sql` creates the tables; `schema/roles.sql` creates two roles and
three users (concurrency-capped via `max_concurrent_queries_for_user`):

| User | Role | Cap | Use |
|---|---|---|---|
| `flashproxy-status-public` | `sla_reader` | 500 | **published** on the status page — anyone can reproduce it |
| `flashproxy-status-website` | `sla_reader` | 50 | the website renders with this |
| `flashproxy-status-worker` | `sla_writer` | 200 | workers push probe results |

> SQL-created users need a writeable access storage (`<local_directory>` under
> `user_directories`) or ClickHouse errors with "no writeable access storage".

## Configuration

JSON with `${ENV}` expansion so secrets never live in the file.

- `config/website.example.json` — listen addr, ClickHouse (reader) creds, the public
  key to publish, SLO thresholds.
- `config/worker.example.json` — vantage id, ClickHouse (writer) creds, and the list
  of packages → `proxy_url` (e.g. `${ISP_PROXY_URL}`), target, and IP version.

## Deploy

- `deploy/clickhouse-deploy-prompt.md` — runbook to bring up a self-hosted ClickHouse
  on a bare metal: loopback-bind + reserved ports, the three roles, exposed only via a
  Cloudflare tunnel (`ch.flashproxy.com`).
- `deploy/terraform/` — two-region AWS (Ashburn + Frankfurt) workers + origin +
  website on `t4g.small`, dual-stack. Secrets via env / `*.tfvars` (gitignored).
- CI (`.github/workflows/docker.yml`) builds the multi-arch image to
  `ghcr.io/<owner>/flashproxy-sla`.

## Roadmap

- ✅ connect scenario, worker/website split, ClickHouse role model, status page, Discord alerts, AWS Terraform.
- **Next:** streaming / large-object / hi-freq / scraping / long-session scenarios; throughput & stability KPIs.
- baseline-relative anomaly detection + error-budget / 90-day uptime from self-recorded history.
- per-archetype frontend, vantage compare, incident timeline; Postgres/SQLite store backends.

## License

MIT — see [LICENSE](LICENSE).
