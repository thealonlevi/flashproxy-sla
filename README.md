# flashproxy-status

Open-source, fully reproducible **SLA monitoring for proxy services**. It actively
simulates the payload archetypes of real proxy users, runs each workload **both
through the proxy and directly (no-proxy baseline)**, and measures latency,
throughput, and availability — so degradation is caught before customers complain,
and the proxy's overhead over the raw network is explicit.

**Live:** <https://status.flashproxy.com> · **SLA:** <https://status.flashproxy.com/sla>
· built by **[FlashProxy](https://flashproxy.com)**.

It is a **closed, fully reproducible system**: every number on the public page was
recorded by this app itself, into its *own* ClickHouse. Nothing is read from any
internal or production data source — and that metrics database is **publicly
readable**, so anyone can independently reproduce every figure (this is what makes
the FlashProxy SLA verifiable). Clone it, point it at any proxy, and you get the
same dashboard.

```
 origin ──▶ [proxy under test] ──▶ worker ──▶ own ClickHouse ──▶ website ──▶ status page + /sla
            (+ direct baseline)   (probes,     (system of        (read-only,   (SSR, crawlable,
                                   writer role)  record)           sla_reader)   independently
                                                                                 verifiable)
```

The only thing crossing into the system from the outside is the **proxy endpoint +
credentials** you put in config — an input, not a data source. Read and write are
split by ClickHouse role, so you can run **N workers** (e.g. one VM per region) all
writing to one ClickHouse, with read-only websites on top.

## Live deployment

| | |
|---|---|
| **Status page** | <https://status.flashproxy.com> |
| **SLA** | <https://status.flashproxy.com/sla> — 100% uptime guarantee, automatically compensated, independently verifiable |
| **Public metrics DB** (read-only) | `https://ch.flashproxy.com` · user `flashproxy-status-public` · password `flashproxy-public-ro` · database `sla` |
| **Company** | <https://flashproxy.com> |

The production deployment runs workers in **AWS Ashburn (us-east-1)** and **Frankfurt
(eu-central-1)**, writing to a self-hosted ClickHouse on a bare-metal host exposed only
through a Cloudflare tunnel (`ch.flashproxy.com`).

## What it measures

Per product, per vantage, every workload runs **twice** — through the proxy and
**direct** (no-proxy baseline, suffixed `_direct`) — so the proxy's overhead is
visible. One row per attempt lands in `sla.probe_raw`.

| Scenario | Simulates | Headline metric |
|---|---|---|
| `connect` | upstream connect (`CONNECT → 200`) | median / avg connect-ms |
| `ping` | raw gateway network RTT (ICMP) | gateway ping ms |
| `streaming` | heavy streaming / buffering | sustained throughput (Mbps) |
| `large_object` | large-object download | TTFB / throughput |
| `hifreq_small` | account / credential checkers | connect-ms distribution + setup success |
| `scraping` | broad web scraping (many hosts) | connect-ms spread |
| `long_session` | long-maintained / persistent sessions | hold stability |

The page auto-selects the **best vantage** per product (lowest median connect-ms) as
the default, with a per-product toggle; the latency chart overlays **gateway ping vs
proxy connect** so the network floor and the proxy overhead are side by side.

## Architecture

| Component | What it does |
|---|---|
| `cmd/origin` | Deterministic dual-stack upstream (`/connect`, `/bytes/{n}`, `/small`, `/hold`). Pure SLA signal, no third-party variance. |
| `cmd/worker` | Runs every scenario from one vantage (via proxy + direct), writes results **directly** to ClickHouse as the `sla_writer` role. The only component that touches a real proxy. Run as many as you like; set `"monitor": true` on one to also evaluate SLO and fire Discord alerts. |
| `cmd/website` | **Read-only.** Server-renders the status page and `/sla`, serves the JSON API, and publishes the public read-only key at `/api/meta` — all as the `sla_reader` role; never writes. |
| `internal/{probe,slo,chstore,model}` | Scenario probes, SLO evaluation, a stdlib-only ClickHouse HTTP client, and shared types. |
| `web/` | Framework-free, console-style, server-rendered status + SLA pages. |

Dependency-free Go (standard library only) — builds offline, trivially reproducible.
ClickHouse is accessed over its HTTP interface; the AWS instances build the binaries
from source on first boot (no container registry required).

## ClickHouse roles

`schema/clickhouse.sql` creates the tables; `schema/roles.sql` creates two roles and
three users (concurrency-capped via `max_concurrent_queries_for_user`):

| User | Role | Cap | Use |
|---|---|---|---|
| `flashproxy-status-public` | `sla_reader` | 500 | **published** — anyone can query the raw data and reproduce the page |
| `flashproxy-status-website` | `sla_reader` | 50 | the website renders with this |
| `flashproxy-status-worker` | `sla_writer` | 200 | workers push probe results |

> SQL-created users need a writeable access storage (`<local_directory>` under
> `user_directories`) or ClickHouse errors with "no writeable access storage".

## Pages & API

- **`/`** — server-rendered status page (crawlable; Organization + WebSite + FAQ JSON-LD).
- **`/sla`** — server-rendered Service Level Agreement (crawlable; FAQ JSON-LD).
- **`/api/overview`** — per-product, per-vantage status + 90-bar uptime, with the best vantage marked.
- **`/api/series?package=&vantage=&minutes=`** — gateway-ping and proxy-connect time series.
- **`/api/scenarios?package=&vantage=`** — per-scenario stats (proxy vs `_direct`).
- **`/api/status`**, **`/api/meta`** (public key), **`/api/events`**, **`/healthz`**.
- **`/robots.txt`**, **`/sitemap.xml`**, **`/llms.txt`** — SEO + GEO (AI answer engines welcomed).

## Quick start

```bash
# ClickHouse + origin + website (no real proxy needed):
docker compose up --build
# open http://localhost:8080  (renders; shows "no data" until a worker runs)

# Add a worker against your own proxy to see live numbers:
ISP_PROXY_URL='http://USER:PASS@HOST:30' docker compose --profile demo up --build
```

## Configuration

JSON with `${ENV}` expansion so secrets never live in the file.

- `config/website.example.json` — listen addr, ClickHouse (reader) creds, the public
  key to publish, optional TLS, `site_url`, and SLO thresholds.
- `config/worker.example.json` — vantage id, ClickHouse (writer) creds, the
  origin(s) for payload scenarios, scrape hosts, scenario intervals, and the list of
  packages → `proxy_url` (e.g. `${ISP_PROXY_URL}`), target, and IP version.

## Deploy

- **`deploy/clickhouse-deploy-prompt.md`** — runbook to bring up a self-hosted
  ClickHouse on a bare-metal host: loopback bind + kernel-reserved ports, the three
  roles, exposed only via a Cloudflare tunnel (`ch.flashproxy.com`).
- **`deploy/terraform/`** — two-region AWS (Ashburn + Frankfurt): a dedicated
  **dual-stack VPC**, `t4g.small` instances that **build the binaries from source** in
  cloud-init, the worker + origin (+ website in Ashburn), the security group, and
  Elastic IPs. The website serves `:443` with a Cloudflare Origin Certificate so the
  zone's "Full (strict)" mode works; instances resolve DNS via Cloudflare (`1.1.1.1`).
  Secrets are supplied via env / a gitignored `*.tfvars` — never committed.

## Transparency & the SLA

This project is the measurement instrument behind FlashProxy's SLA. Because **the code
is open source** and **the metrics database is publicly readable**, every availability
figure — and every automatic SLA credit — can be independently reproduced and
challenged. See the full SLA at <https://status.flashproxy.com/sla>.

## Author

Built and maintained by **Alon Levi**, Co-Founder of **[FlashProxy](https://flashproxy.com)**
— `alon@flashproxy.io`.

## License

MIT — © 2026 Alon Levi / FlashProxy. See [LICENSE](LICENSE).
