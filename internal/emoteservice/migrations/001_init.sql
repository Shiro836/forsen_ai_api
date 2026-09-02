CREATE EXTENSION IF NOT EXISTS vector;

-- provider is in every emote key. Only 7TV is implemented, but a real channel
-- was measured to have no 7TV set at all and to run entirely on BTTV, so the
-- schema must not assume one provider.
--
-- flags is 7TV's v3 bitfield, kept as a raw fact. Bit 16 (ContentSexual) is set
-- on 2 of 1.36M emotes, so listed/deleted are the only usable layer-1 signals.
--
-- The embedding columns start dimensionless; 002 pins them once the model was
-- chosen.
CREATE TABLE IF NOT EXISTS emote_moderation (
    provider        text NOT NULL DEFAULT '7tv',
    emote_id        text NOT NULL,
    name            text NOT NULL,
    listed          boolean NOT NULL DEFAULT false,
    animated        boolean NOT NULL DEFAULT false,
    deleted         boolean NOT NULL DEFAULT false,
    flags           int NOT NULL DEFAULT 0,
    tags            text[] NOT NULL DEFAULT '{}',
    description     text NOT NULL DEFAULT '',
    nsfw            boolean NOT NULL DEFAULT false,
    innuendo        boolean NOT NULL DEFAULT false,
    image_embedding vector,
    desc_embedding  vector,
    classified_at   timestamptz,
    model           text NOT NULL DEFAULT '',
    original_key    text NOT NULL DEFAULT '',
    grid_key        text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, emote_id)
);

CREATE INDEX IF NOT EXISTS emote_moderation_name_idx ON emote_moderation (lower(name));

CREATE TABLE IF NOT EXISTS emote_queue (
    provider    text NOT NULL DEFAULT '7tv',
    emote_id    text NOT NULL,
    status      text NOT NULL DEFAULT 'pending',
    priority    int NOT NULL DEFAULT 0,
    attempts    int NOT NULL DEFAULT 0,
    last_error  text NOT NULL DEFAULT '',
    enqueued_at timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, emote_id)
);

CREATE INDEX IF NOT EXISTS emote_queue_pending_idx
    ON emote_queue (priority DESC, enqueued_at)
    WHERE status = 'pending';

-- streamer_id is the main database's users.id; the nil uuid is the global scope.
-- verdict is part of the key so a channel can hold conflicting rows; the
-- precedence chain, not the schema, resolves them.
CREATE TABLE IF NOT EXISTS emote_overrides (
    streamer_id uuid NOT NULL,
    provider    text NOT NULL DEFAULT '7tv',
    emote_id    text NOT NULL,
    verdict     text NOT NULL CHECK (verdict IN ('allow', 'block')),
    reason      text NOT NULL DEFAULT '',
    author      text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (streamer_id, provider, emote_id, verdict)
);

CREATE INDEX IF NOT EXISTS emote_overrides_emote_idx ON emote_overrides (provider, emote_id);

CREATE TABLE IF NOT EXISTS emote_streamer_settings (
    streamer_id    uuid PRIMARY KEY,
    block_nsfw     boolean NOT NULL DEFAULT true,
    block_innuendo boolean NOT NULL DEFAULT true,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- threshold overrides the configured cosine cutoff for this rule alone. NULL
-- means "use the service default".
CREATE TABLE IF NOT EXISTS emote_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    streamer_id uuid NOT NULL,
    rule_text   text NOT NULL,
    threshold   real,
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS emote_rules_streamer_idx ON emote_rules (streamer_id);

-- Materialized result of a rule the streamer confirmed. Rows exist only for
-- emotes a human ticked in the confirm-preview, so runtime never scores vectors.
CREATE TABLE IF NOT EXISTS emote_rule_matches (
    rule_id      uuid NOT NULL REFERENCES emote_rules (id) ON DELETE CASCADE,
    provider     text NOT NULL DEFAULT '7tv',
    emote_id     text NOT NULL,
    confirmed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, provider, emote_id)
);

CREATE INDEX IF NOT EXISTS emote_rule_matches_emote_idx ON emote_rule_matches (provider, emote_id);
