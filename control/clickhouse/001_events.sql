-- Raw events written by workers (batched, RowBinary over HTTP) and their
-- per-minute roll-ups. Raw events are kept 3 days, roll-ups 180 days.
--
-- non_replicated_deduplication_window (on the raw tables and the roll-ups)
-- lets a worker resend a batch whose reply was lost, with the same
-- insert_deduplication_token, without duplicating rows or roll-up counts.

CREATE TABLE IF NOT EXISTS submit_events
(
    ts                  DateTime64(3, 'UTC'),
    worker_id           UInt8,
    gateway_id          UInt32,
    session_id          UInt64,
    system_id           LowCardinality(String),
    message_id          UInt64, -- 0 unless accepted
    source_addr         String,
    dest_addr           String,
    result              Enum8('accepted' = 1, 'rejected' = 2, 'throttled' = 3, 'invalid' = 4, 'licence_throttled' = 5),
    command_status      UInt32,
    outcome             LowCardinality(String),
    dlr_status          LowCardinality(String),
    dlr_error           LowCardinality(String),
    registered_delivery UInt8,
    data_coding         UInt8,
    sm_length           UInt16,
    INDEX idx_message_id message_id TYPE minmax GRANULARITY 1,
    INDEX idx_dest_addr dest_addr TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toDate(ts)
ORDER BY (gateway_id, ts)
TTL toDateTime(ts) + INTERVAL 3 DAY
SETTINGS non_replicated_deduplication_window = 1000, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS dlr_events
(
    ts             DateTime64(3, 'UTC'),
    worker_id      UInt8,
    gateway_id     UInt32,
    session_id     UInt64, -- 0 when no session was involved (queued, expired)
    message_id     UInt64,
    source_addr    String, -- of the original submit_sm
    dest_addr      String, -- of the original submit_sm
    result         Enum8('acknowledged' = 1, 'retried' = 2, 'failed' = 3, 'queued' = 4, 'expired' = 5),
    dlr_status     LowCardinality(String),
    dlr_error      LowCardinality(String),
    attempt        UInt8,
    submit_ts      DateTime64(3, 'UTC'),
    delay_ms       UInt32, -- submit_sm accepted -> deliver_sm sent; 0 if not sent
    resp_ms        UInt32, -- deliver_sm sent -> deliver_sm_resp; 0 if none
    command_status UInt32, -- of the deliver_sm_resp
    INDEX idx_message_id message_id TYPE minmax GRANULARITY 1,
    INDEX idx_dest_addr dest_addr TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toDate(ts)
ORDER BY (gateway_id, ts)
TTL toDateTime(ts) + INTERVAL 3 DAY
SETTINGS non_replicated_deduplication_window = 1000, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS session_events
(
    ts         DateTime64(3, 'UTC'),
    worker_id  UInt8,
    gateway_id UInt32,
    session_id UInt64,
    event      Enum8('bound' = 1, 'bind_failed' = 2, 'closed' = 3, 'killed' = 4),
    bind_type  LowCardinality(String),
    system_id  LowCardinality(String),
    remote     String,
    status     UInt32, -- bind_resp command_status for bind_failed
    submits    UInt64  -- submit_sm received, on closed
)
ENGINE = MergeTree
PARTITION BY toDate(ts)
ORDER BY (gateway_id, ts)
TTL toDateTime(ts) + INTERVAL 3 DAY
SETTINGS non_replicated_deduplication_window = 1000, ttl_only_drop_parts = 1;

CREATE TABLE IF NOT EXISTS submit_minute
(
    minute         DateTime('UTC'),
    gateway_id     UInt32,
    result         Enum8('accepted' = 1, 'rejected' = 2, 'throttled' = 3, 'invalid' = 4, 'licence_throttled' = 5),
    outcome        LowCardinality(String),
    command_status UInt32,
    messages       UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (gateway_id, minute, result, outcome, command_status)
TTL minute + INTERVAL 180 DAY
SETTINGS non_replicated_deduplication_window = 1000;

CREATE MATERIALIZED VIEW IF NOT EXISTS submit_minute_mv TO submit_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    outcome,
    command_status,
    count() AS messages
FROM submit_events
GROUP BY minute, gateway_id, result, outcome, command_status;

-- delay_bucket is the lower bound (ms) of the submit -> deliver_sm delay.
CREATE TABLE IF NOT EXISTS dlr_minute
(
    minute       DateTime('UTC'),
    gateway_id   UInt32,
    result       Enum8('acknowledged' = 1, 'retried' = 2, 'failed' = 3, 'queued' = 4, 'expired' = 5),
    dlr_status   LowCardinality(String),
    delay_bucket UInt32,
    dlrs         UInt64,
    delay_ms_sum UInt64
)
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (gateway_id, minute, result, dlr_status, delay_bucket)
TTL minute + INTERVAL 180 DAY
SETTINGS non_replicated_deduplication_window = 1000;

CREATE MATERIALIZED VIEW IF NOT EXISTS dlr_minute_mv TO dlr_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    dlr_status,
    toUInt32(roundDown(delay_ms, [0, 100, 250, 500, 1000, 2000, 5000, 10000, 20000, 30000, 60000, 120000, 300000])) AS delay_bucket,
    count() AS dlrs,
    sum(delay_ms) AS delay_ms_sum
FROM dlr_events
GROUP BY minute, gateway_id, result, dlr_status, delay_bucket;
