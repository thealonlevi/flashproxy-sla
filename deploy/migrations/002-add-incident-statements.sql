-- Migration 002 — operator-authored incident statements for the LIVE database.
--
-- Run as sla_admin ON THE CLICKHOUSE HOST (loopback). ADDITIVE and ONLINE: a new
-- table plus one narrow role/user; nothing existing is touched, so it is safe to
-- run before OR after deploying the website (the website degrades gracefully when
-- the table is absent — it just shows incidents with no statement attached).
--
--   clickhouse-client --user sla_admin --password '<pw>' --multiquery \
--     < deploy/migrations/002-add-incident-statements.sql
-- (or send one statement per HTTP request to the loopback admin endpoint)
--
-- The new flashproxy-status-statements user lets the internal dashboard
-- (flash-staff-dash) publish statements WITHOUT the broad worker credential (which
-- can write probe_raw/events/ledger and thus forge measurements). It can only
-- SELECT/INSERT this one editorial table. Substitute ${SLA_STATEMENTS_PASSWORD}.

-- 1) Editorial statements table (NOT part of the integrity ledger) -----------
CREATE TABLE IF NOT EXISTS sla.incident_statements
(
    dedupe_key   String,                    -- stable id from the dashboard: incident:<pkg>:<type>:<start>
    package      LowCardinality(String),
    event_type   LowCardinality(String),    -- 'down' | 'degraded'
    period_start DateTime('UTC'),
    period_end   DateTime('UTC'),
    body         String,                    -- the official statement (empty = retracted)
    author       String,
    published_at DateTime('UTC'),
    updated_at   DateTime('UTC') DEFAULT now()
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY dedupe_key
TTL period_start + INTERVAL 400 DAY;

-- 2) Reader access is automatic: sla_reader already has GRANT SELECT ON sla.*,
--    so flashproxy-status-public and -website can read this table with no change.

-- 3) Narrow writer for the dashboard -----------------------------------------
CREATE ROLE IF NOT EXISTS sla_statements_writer;
GRANT SELECT, INSERT ON sla.incident_statements TO sla_statements_writer;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_statements SETTINGS
    max_concurrent_queries_for_user = 20;

CREATE USER IF NOT EXISTS 'flashproxy-status-statements'
    IDENTIFIED WITH sha256_password BY '${SLA_STATEMENTS_PASSWORD}'
    SETTINGS PROFILE 'sla_statements';
GRANT sla_statements_writer TO 'flashproxy-status-statements';
