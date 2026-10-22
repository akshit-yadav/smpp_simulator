-- MO traffic from the per-gateway generator and "Send test MO" (M7).
-- Same layout and retention as the other raw events and roll-ups.

CREATE TABLE IF NOT EXISTS mo_events
(
    ts             DateTime64(3, 'UTC'),
    worker_id      UInt8,
    gateway_id     UInt32,
    session_id     UInt64, -- 0 when no session was involved (queued, dropped, expired)
    message_id     UInt64, -- assigned by PSG, one per part
    source_addr    String, -- the subscriber (MSISDN)
    dest_addr      String, -- the short code or long number
    result         Enum8('acknowledged' = 1, 'retried' = 2, 'failed' = 3, 'queued' = 4, 'expired' = 5, 'dropped' = 6),
    test           UInt8,
    encoding       LowCardinality(String),
    part           UInt8,
    parts          UInt8,
    sm_length      UInt16,
    text           String, -- start of the whole message
    attempt        UInt8,
    resp_ms        UInt32,
    command_status UInt32, -- of the deliver_sm_resp
    INDEX idx_message_id message_id TYPE minmax GRANULARITY 1,
    INDEX idx_source_addr source_addr TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toDate(ts)
ORDER BY (gateway_id, ts)
TTL toDateTime(ts) + INTERVAL 3 DAY
SETTINGS non_replicated_deduplication_window = 1000, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS mo_minute
(
    minute     DateTime('UTC'),
    gateway_id UInt32,
    result     Enum8('acknowledged' = 1, 'retried' = 2, 'failed' = 3, 'queued' = 4, 'expired' = 5, 'dropped' = 6),
    test       UInt8,
    messages   UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (gateway_id, minute, result, test)
TTL minute + INTERVAL 180 DAY
SETTINGS non_replicated_deduplication_window = 1000;

CREATE MATERIALIZED VIEW IF NOT EXISTS mo_minute_mv TO mo_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    test,
    count() AS messages
FROM mo_events
GROUP BY minute, gateway_id, result, test;
