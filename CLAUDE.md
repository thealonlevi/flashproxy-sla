# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`flashproxy-status` is an open-source, independently-reproducible SLA monitor for proxy
services (the instrument behind FlashProxy's public SLA). Synthetic probes simulate real
proxy-user workloads, run each **through the proxy and direct (no-proxy baseline)**, and
write results to a ClickHouse that is itself **publicly readable** — so every number on
the status page can be reproduced by anyone. See `README.md` for the product framing and
the public DB credentials.

Note the naming split: the Go module is `github.com/flashproxy/flashproxy-status`, but the
published source URL referenced in code/docs is `github.com/thealonlevi/flashproxy-sla`.

## Build, test, run

```bash
make build                  # builds bin/{origin,worker,website,verify,keygen} (CGO_ENABLED=0, -trimpath)
make origin|worker|website|verify|keygen   # one binary
make vet                    # go vet ./...
make test                   # go test ./...  (ledger, slo, probe have tests)
go test ./internal/slo -run TestEvaluator  # single test
go run ./cmd/keygen         # generate the Ed25519 ledger-signing keypair

make up                     # docker compose: own ClickHouse + origin + website (no real proxy)
make demo                   # adds a worker; needs ISP_PROXY_URL='http://user:pass@host:port'
```

Dependency-free Go 1.25 (stdlib only — no `go.sum`, builds offline; the integrity ledger
uses `crypto/sha256` + `crypto/ed25519` from the stdlib, so the no-deps property holds).
ClickHouse is reached over its HTTP interface, never a driver. No lint config beyond `go vet`.

Tests do **not** need a live proxy or a live ClickHouse — `internal/ledger` and
`internal/slo` are pure logic (tamper-detection and the SLA verdict/uptime math), and
`internal/probe` exercises the probe logic directly. The worker measures network RTT with a
stdlib TCP dial (`internal/probe/ping.go` → `NetRTT`, scenario `net_rtt`); there is no
`ping`/ICMP shell-out, so it runs unprivileged in the distroless image.

## Architecture: a one-directional, role-split pipeline

```
origin ──▶ [proxy under test] ──▶ worker ──▶ ClickHouse ──▶ website ──▶ status page + /sla
           (+ direct baseline)   (writer)   (sla.probe_raw)  (reader)
```

Three binaries, strictly separated by what they touch:

- **`cmd/worker`** — the only component that touches a real proxy. Runs every scenario from
  one vantage, **twice per cycle** (through the proxy, and direct with `proxy==nil` → the
  `_direct` baseline), writes rows as the **writer** role, and appends one **integrity-ledger
  entry per flush batch**. Each scenario runs in its own ctx-aware goroutine (`runTarget`);
  `emit` is **non-blocking** (sheds + counts probes if the channel is full, so cadence stays
  accurate during a store outage). The `flusher` tags rows with `(vantage, seq)`, inserts
  rows then the ledger entry (dedup-token-idempotent), and only then advances the chain;
  it **retains the batch on error** and **drains on SIGTERM** so deploys/outages don't lose
  data. **Exactly one worker per vantage** (vantage = ledger stream key → single-writer keeps
  the chain fork-free). `"monitor": true` on one worker runs `monitorLoop` (SLO eval every
  30s, debounced status-change events + Discord) and, if `ledger_signing_key` is set, the
  `checkpointLoop` that signs all chains' heads.
- **`cmd/website`** — strictly **read-only** (reader role; never writes). Hardened
  `http.Server` (timeouts, panic recovery, security headers, TLS 1.2 min); server-renders
  `/` and `/sla`; serves the JSON API; `/healthz` is a real ClickHouse readiness check,
  `/livez` is liveness; publishes the public CH key + SLO thresholds + ledger public key at
  `/api/meta`; `/api/ledger` exposes chain heads + checkpoints. Generic errors only (never
  echoes ClickHouse error text to clients). `/` falls through to a static server otherwise.
- **`cmd/origin`** — deterministic upstream (`/connect`, `/bytes/{n}`, `/small`, `/hold`) so
  payload metrics are pure SLA signal with no third-party variance. Bind dual-stack (`:8080`
  serves v4+v6) so IPv6 packages exercise v6 egress. The headline `connect` probe targets
  this origin (not a third-party site) so the SLA number has no external dependency.
- **`cmd/verify`** — standalone public auditor: recomputes every batch/entry hash and checks
  every checkpoint signature using only public read access. **`cmd/keygen`** mints the keypair.

Shared `internal/` packages:

- **`internal/probe`** — the scenarios. `openTunnel` is the core: with a proxy it does an
  HTTP `CONNECT` tunnel (`connect_ms` = proxy upstream establishment); with `proxy==nil` it
  does a direct TCP dial (the baseline). Connect and body phases have separate timeouts;
  throughput is measured over the transfer window (first→last byte).
- **`internal/slo`** — implements the **published SLA contract exactly**: best-vantage
  **average** connect-ms > `degraded_avg_ms` (50) for `degraded_for_min` (5) **consecutive**
  minutes ⇒ Degraded; success < `down_success_pct` ⇒ Down; Availability% = (withData − Down −
  ½·Degraded)/withData. `Rollup()` is the single shared implementation (bars + current status
  + uptime); the website renders it and `Fetch`/`FetchByVantage` reduce it for the monitor.
  Thresholds are published at `/api/meta` so the verdict is reproducible from data.
- **`internal/chstore`** — stdlib-only ClickHouse HTTP client. Pins `session_timezone=UTC`,
  bounds response size, returns errors (no silent truncation). `Num`/`Str`/**`NumU64`**
  helpers: 64-bit ints arrive as JSON **strings** (precision) — use `NumU64` for UInt64,
  never `Num`. Ledger inserts take a dedup token.
- **`internal/model`** — `ProbeResult` + `Event` and their **frozen `Canonical()`**
  serialization (the byte-exact form the ledger hashes and `cmd/verify` reproduces). Changing
  field order/format breaks verification of all historical data.
- **`internal/ledger`** — the hash chain (`Chain.Build`/`Commit`, `BatchHash`, `EntryHash`,
  `VerifyEntry`) and Ed25519 checkpoint sign/verify. Pure, stdlib-only, fully unit-tested
  (incl. tamper detection).

## ClickHouse roles (the trust boundary)

`schema/clickhouse.sql` creates `sla.probe_raw` + `sla.events` (both UTC, **400-day TTL**,
carrying `stream`/`seq`), the append-only **`sla.ledger`** + **`sla.ledger_checkpoints`**, and
the `probe_1m` rollup. probe_raw/ledger set `non_replicated_deduplication_window` so retried
inserts are idempotent. `schema/roles.sql` creates `sla_reader` (SELECT) and `sla_writer`
(SELECT+INSERT); the **public** user is published (resource-capped on every axis) so anyone
can reproduce the page, the **website** user renders, the **worker**
user writes. The read/write split is the mechanism that makes the data trustworthy — keep it.

Passwords are `${ENV}` placeholders in the SQL; substitute at bootstrap. The Docker demo
shortcuts this with a single `sla` admin user — production uses the three scoped roles.

## Conventions that matter

- **Config is JSON with `${ENV}` expansion** (`os.ExpandEnv` on the file bytes), so proxy
  credentials and DB passwords never live in the file. `config/*.docker.json` are the
  committed demo configs; `config/*.example.json` document the full shape. Both binaries
  **validate required fields and fail fast** (an unset `${ENV}` expands to `""`, which would
  otherwise silently mean an empty password) — keep that validation when adding fields.
- **Never log proxy credentials.** `worker` runs `redactURL` (`url.Redacted()`) on any proxy
  URL before logging. Preserve this in any new log line that includes a proxy URL.
- **Scenario naming:** the direct baseline of scenario `X` is stored as `X_direct` (via
  `scn()`). Queries and the UI rely on this suffix to pair proxy vs baseline.
- **The integrity ledger constrains the write path.** Rows carry `(stream=vantage, seq)`; the
  flusher inserts rows then the ledger entry (dedup-token idempotent) and only then advances
  the chain (`Chain.Build` → write → `Commit`). One writer per vantage. `model.Canonical()`
  is **frozen** — changing field order/format invalidates every historical hash.
- **Timezone is UTC end-to-end.** Schema columns are `DateTime('UTC')` and the client pins
  `session_timezone=UTC`; keep new timestamp columns/queries UTC.
- **Numeric helpers:** keep most SQL outputs <64-bit (`toUInt32`, `round`) so they render as
  JSON numbers; for genuine UInt64 (`seq`, `bytes`) use `chstore.NumU64`, never `Num` (float
  precision loss past 2^53).
- **No new dependencies.** The dependency-free property is load-bearing (offline, reproducible
  builds; cloud-init builds from source). The ledger crypto is stdlib (`crypto/ed25519`,
  `crypto/sha256`) precisely to preserve this.

## Deploy

- **`deploy/terraform/`** — two-region AWS (Ashburn `us-east-1` + Frankfurt `eu-central-1`):
  a dual-stack VPC, `t4g.small` instances that **build the binaries from source in cloud-init**
  (no registry), worker + origin everywhere and website in Ashburn on `:443` with a Cloudflare
  Origin Certificate. Instances enforce IMDSv2 (`metadata_options`), verify the Go toolchain
  SHA-256, and build from a pinnable `git_ref` (use an immutable commit SHA). Secrets come via
  env / a gitignored `*.tfvars`; `terraform.tfstate*`, `*.tfvars`, and `.terraform/` are
  gitignored and must never be committed (state would leak resource attributes).
- **`deploy/clickhouse-deploy-prompt.md`** + **`deploy/bootstrap-roles.sh`** — runbook + script
  for the self-hosted ClickHouse: loopback bind, UTC timezone, `select_from_system_db_requires_grant`
  + query masking (so the published reader can't read `system.*`), the schema incl. the ledger,
  the scoped roles, exposed only via a Cloudflare tunnel at `ch.flashproxy.com`.

## Sibling repos

This repo is one of several under `~/dev/` (see `/root/CLAUDE.md` for the workspace map).
It is independent of `flash-riptide-isp` / `riptide-autodeploy` — it *monitors* a proxy
endpoint as an external input, and shares no code with them.
