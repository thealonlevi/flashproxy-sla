-- flashproxy-status schema. Loaded automatically by the ClickHouse container on
-- first boot (mounted into /docker-entrypoint-initdb.d). This is the app's OWN,
-- separate ClickHouse — nothing here references any production data source.

CREATE DATABASE IF NOT EXISTS sla;

-- Raw probe results: one row per probe attempt, the system of record. 90d TTL.
CREATE TABLE IF NOT EXISTS sla.probe_raw
(
    ts              DateTime,
    vantage         LowCardinality(String),
    package         LowCardinality(String),
    scenario        LowCardinality(String),
    proto           LowCardinality(String),
    target          String,
    ip_version      UInt8,
    success         UInt8,
    error_type      LowCardinality(String),
    dial_ms         UInt32,
    connect_ms      UInt32,
    ttfb_ms         UInt32,
    total_ms        UInt32,
    bytes           UInt64,
    throughput_mbps Float32
)
ENGINE = MergeTree
ORDER BY (package, scenario, ts)
TTL ts + INTERVAL 90 DAY;

-- Self-recorded markers: deploys, maintenance windows, status changes.
CREATE TABLE IF NOT EXISTS sla.events
(
    ts      DateTime DEFAULT now(),
    type    LowCardinality(String),
    package LowCardinality(String),
    message String
)
ENGINE = MergeTree
ORDER BY ts;

-- Optional 1-minute rollup (query optimization). Phase 1 queries hit probe_raw
-- directly — synthetic volume is tiny — but this is here for when history grows.
CREATE TABLE IF NOT EXISTS sla.probe_1m
(
    bucket              DateTime,
    vantage             LowCardinality(String),
    package             LowCardinality(String),
    scenario            LowCardinality(String),
    attempts            UInt64,
    successes           UInt64,
    connect_ms_state    AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), UInt32),
    connect_ms_avg_st   AggregateFunction(avg, UInt32),
    ttfb_ms_state       AggregateFunction(quantilesTDigest(0.5, 0.95), UInt32),
    throughput_avg_st   AggregateFunction(avg, Float32)
)
ENGINE = AggregatingMergeTree
ORDER BY (package, scenario, vantage, bucket);

CREATE MATERIALIZED VIEW IF NOT EXISTS sla.probe_1m_mv TO sla.probe_1m AS
SELECT
    toStartOfMinute(ts) AS bucket,
    vantage, package, scenario,
    count()       AS attempts,
    sum(success)  AS successes,
    quantilesTDigestState(0.5, 0.95, 0.99)(connect_ms) AS connect_ms_state,
    avgState(connect_ms)                               AS connect_ms_avg_st,
    quantilesTDigestState(0.5, 0.95)(ttfb_ms)          AS ttfb_ms_state,
    avgState(throughput_mbps)                          AS throughput_avg_st
FROM sla.probe_raw
GROUP BY bucket, vantage, package, scenario;
