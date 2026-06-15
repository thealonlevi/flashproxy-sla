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
   - **Pin the server timezone to UTC** (the app writes naive-UTC timestamps and
     compares them against `now()`): `<timezone>UTC</timezone>`.
   - **Lock down system tables** so the published reader can't read `system.query_log`
     (other users' queries, incl. the `CREATE USER … BY '…'` plaintext) or
     `system.users` (password hashes). The grants never include `system.*`, so with
     these flags the public/website users cannot touch it:
     ```xml
     <access_control_improvements>
       <select_from_system_db_requires_grant>true</select_from_system_db_requires_grant>
       <select_from_information_schema_requires_grant>true</select_from_information_schema_requires_grant>
     </access_control_improvements>
     ```
   - **Redact passwords from query_log** (defense in depth for the bootstrap step):
     ```xml
     <query_masking_rules>
       <rule><regexp>IDENTIFIED\s+WITH\s+\w+\s+BY\s+'[^']+'</regexp><replace>IDENTIFIED ... BY '[HIDDEN]'</replace></rule>
     </query_masking_rules>
     ```
4. Start via a systemd unit (`clickhouse-flashproxy.service`). Verify:
   `curl http://127.0.0.1:HTTP_PORT/ -H 'X-ClickHouse-User: sla_admin' -H 'X-ClickHouse-Key: <pw>' -d 'SELECT version()'`
5. Load the **schema** and **roles** from the repo (do not hand-copy — these are the
   canonical, integrity-ledger-aware definitions):
   - Schema: send `schema/clickhouse.sql` to the admin endpoint (it creates
     `sla.probe_raw`, `sla.events`, the append-only `sla.ledger` + `sla.ledger_checkpoints`,
     and the rollup — all UTC, 400-day TTL). The HTTP interface runs one statement per
     call, so split on `;` (or use `clickhouse-client --multiquery < schema/clickhouse.sql`).
   - Roles/users: run `deploy/bootstrap-roles.sh` with the passwords in the environment
     (`SLA_PUBLIC_PASSWORD`, `SLA_WEBSITE_PASSWORD`, `SLA_WORKER_PASSWORD`, plus
     `CH_ADMIN_URL/USER/PASS`). It keeps plaintext out of shell history. Use strong
     random passwords for worker/website; for `flashproxy-status-public` use a readable
     token (e.g. `flashproxy-public-ro`) — it is meant to be published.
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
   - public user reading **system tables must FAIL**: `... -d 'SELECT * FROM system.users'`
     and `... -d 'SELECT count() FROM system.query_log'` must both be denied. If either
     succeeds, the `select_from_system_db_requires_grant` flag (step 3) is not in effect —
     fix before exposing the tunnel.
   - integrity ledger present: `... -d 'SELECT count() FROM sla.ledger'` returns a number.

## Schema & roles

Both are defined in the repo — load them, don't hand-copy (the embedded copies that
used to live here drifted out of date):

- `schema/clickhouse.sql` — tables incl. the integrity ledger (UTC, 400-day TTL).
- `schema/roles.sql` + `deploy/bootstrap-roles.sh` — roles, resource-capped profiles
  (memory/bytes/rows/time + concurrency), and the three users. The public profile is
  hardened against memory-bomb queries; combined with the step-3 config flags it cannot
  read `system.*`.

## Report back
- `ch.flashproxy.com` reachable over HTTPS: yes/no
- `HTTP_PORT`, `NATIVE_PORT`, and confirmation they're in `ip_local_reserved_ports`
- The three users + passwords: **worker** (write, PRIVATE), **website** (read, PRIVATE),
  **public** (read, PUBLISHABLE on the status page)
- Confirmation: public cannot INSERT, worker can; admin only on loopback.
