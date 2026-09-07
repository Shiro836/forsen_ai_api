CREATE TABLE IF NOT EXISTS llm_calls (
    msg_id            UUID,
    channel_id        UUID,
    idx               UInt16,
    updated           UInt64 CODEC(Delta, ZSTD(1)),
    at                DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    kind              LowCardinality(String),
    model             LowCardinality(String),
    endpoint          LowCardinality(String),
    request           String CODEC(ZSTD(3)),
    response          String CODEC(ZSTD(3)),
    prompt_tokens     UInt32,
    completion_tokens UInt32,
    latency_ms        UInt32,
    error             String CODEC(ZSTD(1))
) ENGINE = ReplacingMergeTree(updated)
PARTITION BY toYYYYMM(at)
ORDER BY (channel_id, msg_id, idx)
