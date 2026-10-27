-- M10: traffic from PSG's own client simulator (psg-tester) is tagged with
-- simulator = 1 so reports can leave it out. The roll-ups keep it in their
-- key (a SummingMergeTree would otherwise add it up), and their views are
-- recreated to group by it; rows inserted while a view is being recreated
-- are not rolled up. (A UInt8 column is 0 by default; an explicit DEFAULT
-- would keep it out of the sorting key.)

ALTER TABLE submit_events ADD COLUMN IF NOT EXISTS simulator UInt8 DEFAULT 0;
ALTER TABLE dlr_events ADD COLUMN IF NOT EXISTS simulator UInt8 DEFAULT 0;
ALTER TABLE mo_events ADD COLUMN IF NOT EXISTS simulator UInt8 DEFAULT 0;
ALTER TABLE session_events ADD COLUMN IF NOT EXISTS simulator UInt8 DEFAULT 0;

ALTER TABLE submit_minute
    ADD COLUMN simulator UInt8,
    MODIFY ORDER BY (gateway_id, minute, result, outcome, command_status, simulator);
ALTER TABLE dlr_minute
    ADD COLUMN simulator UInt8,
    MODIFY ORDER BY (gateway_id, minute, result, dlr_status, delay_bucket, simulator);
ALTER TABLE mo_minute
    ADD COLUMN simulator UInt8,
    MODIFY ORDER BY (gateway_id, minute, result, test, simulator);

DROP VIEW IF EXISTS submit_minute_mv;
CREATE MATERIALIZED VIEW submit_minute_mv TO submit_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    outcome,
    command_status,
    simulator,
    count() AS messages
FROM submit_events
GROUP BY minute, gateway_id, result, outcome, command_status, simulator;

DROP VIEW IF EXISTS dlr_minute_mv;
CREATE MATERIALIZED VIEW dlr_minute_mv TO dlr_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    dlr_status,
    toUInt32(roundDown(delay_ms, [0, 100, 250, 500, 1000, 2000, 5000, 10000, 20000, 30000, 60000, 120000, 300000])) AS delay_bucket,
    simulator,
    count() AS dlrs,
    sum(delay_ms) AS delay_ms_sum
FROM dlr_events
GROUP BY minute, gateway_id, result, dlr_status, delay_bucket, simulator;

DROP VIEW IF EXISTS mo_minute_mv;
CREATE MATERIALIZED VIEW mo_minute_mv TO mo_minute AS
SELECT
    toStartOfMinute(ts) AS minute,
    gateway_id,
    result,
    test,
    simulator,
    count() AS messages
FROM mo_events
GROUP BY minute, gateway_id, result, test, simulator;
