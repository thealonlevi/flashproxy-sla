-- Roles, settings profiles, and users for flashproxy-status.
-- Run ONCE by an admin after schema/clickhouse.sql is loaded. Passwords are
-- ${ENV} placeholders — substitute at bootstrap (see deploy/bootstrap-roles.sh,
-- which keeps plaintext out of shell history and out of query_log).
--
-- Four users, three roles:
--   sla_reader            -> SELECT on sla.* (incl. the integrity ledger)
--   sla_writer            -> SELECT + INSERT on sla.*
--   sla_statements_writer -> SELECT + INSERT on sla.incident_statements ONLY
--   flashproxy-status-public     (reader, capped) -- PUBLISHED on the site
--   flashproxy-status-website    (reader, capped) -- the site renders with this
--   flashproxy-status-worker     (writer)         -- prober VMs push results + ledger
--   flashproxy-status-statements (narrow writer)  -- internal dashboard publishes
--       official incident statements; intentionally CANNOT write probe_raw/events/
--       ledger, so it can never forge a measurement — only editorial commentary.
--
-- IMPORTANT — system-table exposure: readonly=1 only blocks writes/DDL, NOT
-- SELECTs from the `system` database. Since `flashproxy-status-public` is
-- published to the internet, you MUST also forbid it (and the website user) from
-- reading system tables, or it can read query_log (other users' queries) and
-- potentially user password hashes. Do that BOTH by config and by grant:
--
--   In config.xml (server-wide, the robust fix):
--     <access_control_improvements>
--       <select_from_system_db_requires_grant>true</select_from_system_db_requires_grant>
--       <select_from_information_schema_requires_grant>true</select_from_information_schema_requires_grant>
--     </access_control_improvements>
--   plus a query_masking_rule that redacts  IDENTIFIED ... BY '...'  from logs.
--
-- The grants below scope the published users to sla.* only and never grant
-- system/INFORMATION_SCHEMA, so with the config flags above they cannot read it.

-- ---- roles ----
CREATE ROLE IF NOT EXISTS sla_reader;
GRANT SELECT ON sla.* TO sla_reader;

CREATE ROLE IF NOT EXISTS sla_writer;
GRANT SELECT, INSERT ON sla.* TO sla_writer;

-- Narrow writer: editorial statements ONLY. Never probe_raw/events/ledger.
CREATE ROLE IF NOT EXISTS sla_statements_writer;
GRANT SELECT, INSERT ON sla.incident_statements TO sla_statements_writer;

-- ---- settings profiles ----
-- Public profile is internet-reachable, so it is bounded on EVERY axis: rows,
-- bytes, memory (per query AND per user), execution time, and concurrency. Row
-- caps alone do not bound a cross-join / numbers() / groupArray memory bomb.
CREATE SETTINGS PROFILE IF NOT EXISTS sla_public SETTINGS
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

CREATE SETTINGS PROFILE IF NOT EXISTS sla_website SETTINGS
    readonly = 1,
    allow_ddl = 0,
    allow_introspection_functions = 0,
    max_concurrent_queries_for_user = 50,
    max_execution_time = 30,
    max_memory_usage = 4000000000,
    cancel_http_readonly_queries_on_client_close = 1;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_worker SETTINGS
    max_concurrent_queries_for_user = 200;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_statements SETTINGS
    max_concurrent_queries_for_user = 20;

-- ---- users ----
CREATE USER IF NOT EXISTS 'flashproxy-status-public'
    IDENTIFIED WITH sha256_password BY '${SLA_PUBLIC_PASSWORD}'
    SETTINGS PROFILE 'sla_public';
GRANT sla_reader TO 'flashproxy-status-public';

CREATE USER IF NOT EXISTS 'flashproxy-status-website'
    IDENTIFIED WITH sha256_password BY '${SLA_WEBSITE_PASSWORD}'
    SETTINGS PROFILE 'sla_website';
GRANT sla_reader TO 'flashproxy-status-website';

CREATE USER IF NOT EXISTS 'flashproxy-status-worker'
    IDENTIFIED WITH sha256_password BY '${SLA_WORKER_PASSWORD}'
    SETTINGS PROFILE 'sla_worker';
GRANT sla_writer TO 'flashproxy-status-worker';

-- Internal dashboard: publishes official incident statements only.
CREATE USER IF NOT EXISTS 'flashproxy-status-statements'
    IDENTIFIED WITH sha256_password BY '${SLA_STATEMENTS_PASSWORD}'
    SETTINGS PROFILE 'sla_statements';
GRANT sla_statements_writer TO 'flashproxy-status-statements';
