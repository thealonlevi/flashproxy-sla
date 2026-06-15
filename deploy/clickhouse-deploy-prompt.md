# Prompt: deploy a dedicated ClickHouse for flashproxy-status (bare metal)

Paste everything below to the Claude running on the bare-metal box.

---

You are deploying a small, dedicated, self-hosted **ClickHouse** on this bare-metal
server. It is the datastore for an SLA status dashboard (flashproxy-status). Data
volume is tiny — a few hundred rows/minute. **Other critical production services run
on this box, so not interfering with anything is requirement #1.**

## CRITICAL SAFETY RULES — read before doing anything

A previous attempt caused a production outage: ClickHouse was bound to `0.0.0.0` on a
port that a co-located proxy was using as an **ephemeral egress source port**, and then
the wrong PID (the proxy) was killed while chasing the port conflict. Do NOT repeat this.

1. **Bind ClickHouse to `127.0.0.1` ONLY.** Never `0.0.0.0`, never a public IP. All
   external access is via a Cloudflare tunnel (step 6), not an open port.
2. **Pick TWO random high ports** (one HTTP, one native TCP) in the 20000–60000 range.
   Verify both are free: `ss -ltnp | grep -E ':<port>'` (must be empty). Then **reserve
   them from ephemeral allocation** so no other process can ever grab them as a source
   port — create `/etc/sysctl.d/99-clickhouse-reserved.conf`:
   ```
   net.ipv4.ip_local_reserved_ports = <HTTP_PORT>,<NATIVE_PORT>
   ```
   Apply: `sysctl --system` and confirm `sysctl net.ipv4.ip_local_reserved_ports`.
3. **Never kill a process you found via `fuser`/`lsof` by port** without first confirming
   it is your ClickHouse: `ps -o cmd= -p <pid>` MUST contain your ClickHouse config path.
   If it shows anything else (a proxy, a service), STOP and ask a human.
4. **Data dir on a disk with space** — check `df -h`, pick a path with room (e.g.
   `/var/lib/clickhouse-flashproxy/`). Do NOT use `/tmp`.
5. ClickHouse runs under a **watchdog that respawns it**. To stop/restart, use the
   systemd unit (`systemctl stop`), never `kill`.
6. If a distro ClickHouse already exists on this box, run yours as a **separate instance**
   with its own config file, data dir, ports, and systemd unit name — do not touch theirs.

## Steps

1. Choose + reserve ports (rule 2). Record `HTTP_PORT` and `NATIVE_PORT`.
2. Install ClickHouse (official apt/yum repo, or the single static binary).
3. Write a dedicated config (`/etc/clickhouse-flashproxy/config.xml`) with:
   - `<listen_host>127.0.0.1</listen_host>`
   - `<http_port>HTTP_PORT</http_port>`, `<tcp_port>NATIVE_PORT</tcp_port>`
   - `<path>/var/lib/clickhouse-flashproxy/</path>`
   - a **writeable access storage** so SQL-created users persist across restarts:
     ```xml
     <user_directories>
       <users_xml><path>/etc/clickhouse-flashproxy/users.xml</path></users_xml>
       <local_directory><path>/var/lib/clickhouse-flashproxy/access/</path></local_directory>
     </user_directories>
     ```
   - In `users.xml`, ONE admin user `sla_admin` with a **strong 32+ char random password**,
     `<access_management>1</access_management>`, and `<networks><ip>::1</ip><ip>127.0.0.1</ip></networks>`
     so the admin is reachable only locally.
4. Start via a systemd unit (`clickhouse-flashproxy.service`). Verify:
   `curl http://127.0.0.1:HTTP_PORT/ -H 'X-ClickHouse-User: sla_admin' -H 'X-ClickHouse-Key: <pw>' -d 'SELECT version()'`
5. Load the **schema** then the **roles** (SQL below). Generate strong random passwords for
   `flashproxy-status-worker` and `flashproxy-status-website`. For `flashproxy-status-public`
   use a readable token (e.g. `flashproxy-public-ro`) — it is meant to be published.
   Send each statement as a separate HTTP request (the HTTP interface = one statement per call).
6. Install `cloudflared` and expose ONLY the HTTP port publicly as `ch.flashproxy.com`:
   ```
   cloudflared tunnel login
   cloudflared tunnel create flashproxy-ch
   # ingress: ch.flashproxy.com -> http://127.0.0.1:HTTP_PORT  (config.yml)
   cloudflared tunnel route dns flashproxy-ch ch.flashproxy.com
   ```
   Run cloudflared as a service. (This is the only thing that makes CH reachable — no
   inbound firewall ports, TLS handled by Cloudflare, origin IP hidden.)
7. End-to-end verification (from any machine):
   - `curl https://ch.flashproxy.com/ -H 'X-ClickHouse-User: flashproxy-status-public' -H 'X-ClickHouse-Key: <public-pw>' -d 'SELECT 1'` → `1`
   - public user INSERT must FAIL; worker user INSERT must SUCCEED; admin must be
     unreachable via the tunnel (only on loopback).

## Schema SQL

```sql
CREATE DATABASE IF NOT EXISTS sla;

CREATE TABLE IF NOT EXISTS sla.probe_raw (
  ts DateTime, vantage LowCardinality(String), package LowCardinality(String),
  scenario LowCardinality(String), proto LowCardinality(String), target String,
  ip_version UInt8, success UInt8, error_type LowCardinality(String),
  dial_ms UInt32, connect_ms UInt32, ttfb_ms UInt32, total_ms UInt32,
  bytes UInt64, throughput_mbps Float32
) ENGINE = MergeTree ORDER BY (package, scenario, ts) TTL ts + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS sla.events (
  ts DateTime DEFAULT now(), type LowCardinality(String),
  package LowCardinality(String), message String
) ENGINE = MergeTree ORDER BY ts;
```

## Roles SQL  (substitute the 3 passwords)

```sql
CREATE ROLE IF NOT EXISTS sla_reader;
GRANT SELECT ON sla.* TO sla_reader;
CREATE ROLE IF NOT EXISTS sla_writer;
GRANT SELECT, INSERT ON sla.* TO sla_writer;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_public  SETTINGS max_concurrent_queries_for_user = 500, readonly = 1, max_execution_time = 15, max_result_rows = 1000000, max_rows_to_read = 500000000;
CREATE SETTINGS PROFILE IF NOT EXISTS sla_website SETTINGS max_concurrent_queries_for_user = 50,  readonly = 1, max_execution_time = 30;
CREATE SETTINGS PROFILE IF NOT EXISTS sla_worker  SETTINGS max_concurrent_queries_for_user = 200;

CREATE USER IF NOT EXISTS 'flashproxy-status-public'  IDENTIFIED WITH sha256_password BY '<PUBLIC_PW>'  SETTINGS PROFILE 'sla_public';
GRANT sla_reader TO 'flashproxy-status-public';
CREATE USER IF NOT EXISTS 'flashproxy-status-website' IDENTIFIED WITH sha256_password BY '<WEBSITE_PW>' SETTINGS PROFILE 'sla_website';
GRANT sla_reader TO 'flashproxy-status-website';
CREATE USER IF NOT EXISTS 'flashproxy-status-worker'  IDENTIFIED WITH sha256_password BY '<WORKER_PW>'  SETTINGS PROFILE 'sla_worker';
GRANT sla_writer TO 'flashproxy-status-worker';
```

## Report back
- `ch.flashproxy.com` reachable over HTTPS: yes/no
- `HTTP_PORT`, `NATIVE_PORT`, and confirmation they're in `ip_local_reserved_ports`
- The three users + passwords: **worker** (write, PRIVATE), **website** (read, PRIVATE),
  **public** (read, PUBLISHABLE on the status page)
- Confirmation: public cannot INSERT, worker can; admin only on loopback.
