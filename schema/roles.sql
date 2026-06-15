-- Roles, settings profiles, and users for flashproxy-status.
-- Run ONCE by an admin after schema/clickhouse.sql is loaded. Passwords are
-- ${ENV} placeholders — substitute at bootstrap (see deploy/bootstrap-roles.sh)
-- or replace for a self-hosted instance.
--
-- Three users, two roles:
--   sla_reader  -> SELECT on sla.*
--   sla_writer  -> SELECT + INSERT on sla.*
--   flashproxy-status-public  (reader, 500 concurrent queries) -- PUBLISHED on the site
--   flashproxy-status-website (reader,  50 concurrent queries) -- the site renders with this
--   flashproxy-status-worker  (writer)                         -- prober VMs push results
--
-- ClickHouse caps concurrent QUERIES per user (max_concurrent_queries_for_user),
-- which is the practical equivalent of a per-user connection limit.

-- ---- roles ----
CREATE ROLE IF NOT EXISTS sla_reader;
GRANT SELECT ON sla.* TO sla_reader;

CREATE ROLE IF NOT EXISTS sla_writer;
GRANT SELECT, INSERT ON sla.* TO sla_writer;

-- ---- settings profiles ----
CREATE SETTINGS PROFILE IF NOT EXISTS sla_public SETTINGS
    max_concurrent_queries_for_user = 500,
    readonly = 1,
    max_execution_time = 15,
    max_result_rows = 1000000,
    max_rows_to_read = 500000000;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_website SETTINGS
    max_concurrent_queries_for_user = 50,
    readonly = 1,
    max_execution_time = 30;

CREATE SETTINGS PROFILE IF NOT EXISTS sla_worker SETTINGS
    max_concurrent_queries_for_user = 200;

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
