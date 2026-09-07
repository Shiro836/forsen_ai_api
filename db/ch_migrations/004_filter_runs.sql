CREATE TABLE IF NOT EXISTS filter_runs (
    msg_id      UUID,
    channel_id  UUID,
    idx         UInt16,
    updated     UInt64 CODEC(Delta, ZSTD(1)),
    at          DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    target      LowCardinality(String),
    regex_spans String CODEC(ZSTD(1)),
    llm_spans   String CODEC(ZSTD(1)),
    regex_hits  UInt16,
    llm_hits    UInt16,
    latency_ms  UInt32,
    skipped     Bool
) ENGINE = ReplacingMergeTree(updated)
PARTITION BY toYYYYMM(at)
ORDER BY (channel_id, msg_id, idx)
