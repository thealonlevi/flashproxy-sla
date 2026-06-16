-- Migration 001 — integrity ledger + seq/UTC/TTL changes for the LIVE database.
--
-- Run this as sla_admin ON THE CLICKHOUSE HOST (loopback) BEFORE deploying the new
-- binaries. The new worker writes stream/seq and reads sla.ledger on startup, so it
-- crash-loops against the old schema — migrate first, then rebuild the instances.
--
-- Every statement is ADDITIVE and ONLINE (no table rewrite, no downtime). Existing
-- probe_raw/events rows keep stream='' / seq=0 and are simply not covered by the
-- chain; the ledger starts at the first new batch after deploy.
--
--   clickhouse-client --user sla_admin --password '<pw>' --multiquery \
--     < deploy/migrations/001-add-integrity-ledger.sql
-- (or send one statement per HTTP request to the loopback admin endpoint)

-- NOTE: this migration intentionally does NOT create sla.probe_1m. That rollup is
-- defined in schema/clickhouse.sql for FRESH installs only; no code reads it (all
-- queries hit probe_raw directly), so a migrated prod box simply won't have it. That
-- divergence from the canonical schema is expected and harmless.

-- 1) New append-only ledger tables ------------------------------------------
CREATE TABLE IF NOT EXISTS sla.ledger
(
    stream      LowCardinality(String),
    seq         UInt64,
    kind        LowCardinality(String),
    ts_first    DateTime('UTC'),
    ts_last     DateTime('UTC'),
    row_count   UInt32,
    batch_hash  FixedString(64),
    prev_hash   FixedString(64),
    entry_hash  FixedString(64),
    recorded_at DateTime('UTC') DEFAULT now()
)
ENGINE = MergeTree
ORDER BY (stream, seq)
SETTINGS non_replicated_deduplication_window = 1000;

CREATE TABLE IF NOT EXISTS sla.ledger_checkpoints
(
    stream     LowCardinality(String),
    seq        UInt64,
    entry_hash FixedString(64),
    ts         DateTime('UTC'),
    pubkey_id  LowCardinality(String),
    signature  String,
    signed_at  DateTime('UTC') DEFAULT now()
)
ENGINE = MergeTree
ORDER BY (stream, seq);

-- 2) Tie existing tables into the ledger (additive columns) -----------------
ALTER TABLE sla.probe_raw ADD COLUMN IF NOT EXISTS stream LowCardinality(String) DEFAULT '';
ALTER TABLE sla.probe_raw ADD COLUMN IF NOT EXISTS seq    UInt64 DEFAULT 0;
ALTER TABLE sla.events    ADD COLUMN IF NOT EXISTS stream LowCardinality(String) DEFAULT 'events';
ALTER TABLE sla.events    ADD COLUMN IF NOT EXISTS seq    UInt64 DEFAULT 0;

-- 3) Extend retention to 400 days (was 90 / unbounded) ----------------------
ALTER TABLE sla.probe_raw MODIFY TTL ts + INTERVAL 400 DAY;
ALTER TABLE sla.events    MODIFY TTL ts + INTERVAL 400 DAY;

-- 4) Make retried inserts idempotent (dedup by insert_deduplication_token) ----
ALTER TABLE sla.probe_raw MODIFY SETTING non_replicated_deduplication_window = 1000;

-- 5) OPTIONAL: pin ts to UTC display. The server runs <timezone>UTC</timezone> and
--    columns are DateTime('UTC'), so this is only belt-and-suspenders. NOTE: the app
--    does NOT send session_timezone on reads — reader users are readonly=1 and reject
--    any client-set setting (Code 164); correctness relies on the server being UTC.
--    ts is in probe_raw's sort key; if the server rejects MODIFY on a key column,
--    SKIP these two lines (correctness holds without them).
-- ALTER TABLE sla.probe_raw MODIFY COLUMN ts DateTime('UTC');
-- ALTER TABLE sla.events    MODIFY COLUMN ts DateTime('UTC');

-- NOTE: the new canonical schema also moves probe_raw's sort key to
-- (package, scenario, vantage, ts). ClickHouse cannot reorder an existing table's
-- primary key in place, so the LIVE table keeps (package, scenario, ts). That is
-- only a query-pruning optimization — the application does not depend on it. To
-- adopt the new key you would create a new table and backfill in a maintenance
-- window; not required for this deploy.

-- 6) Harden the published settings profiles (resource caps + no introspection) --
ALTER SETTINGS PROFILE sla_public SETTINGS
    readonly = 1,
    allow_ddl = 0,
    allow_introspection_functions = 0,
    max_concurrent_queries_for_user = 50,
    max_execution_time = 5,
    max_rows_to_read = 100000000,
    max_bytes_to_read = 2000000000,
    max_result_rows = 1000000,
    max_memory_usage = 2000000000,
    max_memory_usage_for_user = 4000000000,
    cancel_http_readonly_queries_on_client_close = 1;

ALTER SETTINGS PROFILE sla_website SETTINGS
    readonly = 1,
    allow_ddl = 0,
    allow_introspection_functions = 0,
    max_concurrent_queries_for_user = 50,
    max_execution_time = 30,
    max_memory_usage = 4000000000,
    cancel_http_readonly_queries_on_client_close = 1;

-- 7) SEPARATE (config.xml + restart, not SQL): pin <timezone>UTC</timezone> and set
--    access_control_improvements.select_from_system_db_requires_grant = true plus a
--    query_masking_rule. See deploy/clickhouse-deploy-prompt.md §3. Do this so the
--    published reader cannot read system.query_log / system.users.

-- Verify:
--   SELECT name FROM system.columns WHERE database='sla' AND table='probe_raw' AND name IN ('stream','seq');  -- 2 rows
--   EXISTS TABLE sla.ledger;  EXISTS TABLE sla.ledger_checkpoints;                                            -- 1, 1
