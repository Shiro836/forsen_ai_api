CREATE TABLE IF NOT EXISTS tts_tracks (
    msg_id         UUID,
    channel_id     UUID,
    idx            UInt16,
    updated        UInt64 CODEC(Delta, ZSTD(1)),
    at             DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
    engine         LowCardinality(String),
    text           String CODEC(ZSTD(3)),
    voice_sha      String,
    audio_key      String,
    audio_sec      Float32,
    played_sec     Float32,
    chunks         UInt16,
    first_chunk_ms UInt32,
    synth_ms       UInt32,
    cut            LowCardinality(String)
) ENGINE = ReplacingMergeTree(updated)
PARTITION BY toYYYYMM(at)
ORDER BY (channel_id, msg_id, idx)
