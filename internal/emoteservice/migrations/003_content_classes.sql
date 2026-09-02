-- A single "innuendo" boolean could not say what was wrong with an emote, so a
-- channel could only take all of it or none of it. Classes are stored as text[]
-- rather than columns or an enum so a seventh class needs no migration.
--
-- The conversion below is provisional: it preserves what the old booleans knew
-- (nsfw meant explicit, innuendo in practice meant the cream trope) so nothing
-- silently unblocks before the reclassification pass rewrites these rows.
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS classes text[] NOT NULL DEFAULT '{}';

UPDATE emote_moderation
SET classes = ARRAY(SELECT DISTINCT unnest(
        (CASE WHEN nsfw THEN ARRAY['sexual'] ELSE '{}' END)::text[] ||
        (CASE WHEN innuendo THEN ARRAY['cum'] ELSE '{}' END)::text[]
    ) ORDER BY 1)
WHERE nsfw OR innuendo;

ALTER TABLE emote_moderation DROP COLUMN IF EXISTS nsfw;
ALTER TABLE emote_moderation DROP COLUMN IF EXISTS innuendo;

CREATE INDEX IF NOT EXISTS emote_moderation_classes_idx ON emote_moderation USING gin (classes);

-- classifier_version is the prompt/taxonomy generation a row was judged under,
-- orthogonal to model: the same model under a new taxonomy produces answers the
-- old rows cannot be compared to. Rows judged before classes existed are
-- generation 1, which is exactly what marks them stale. 0 means never judged.
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS classifier_version int NOT NULL DEFAULT 0;

UPDATE emote_moderation SET classifier_version = 1 WHERE classified_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS emote_moderation_classifier_version_idx
    ON emote_moderation (classifier_version)
    WHERE classified_at IS NOT NULL;

-- NULL blocked_classes means the channel never chose and inherits the platform
-- default, which is why the column is nullable while the platform one is not.
ALTER TABLE emote_streamer_settings ADD COLUMN IF NOT EXISTS blocked_classes text[];

UPDATE emote_streamer_settings
SET blocked_classes = ARRAY(SELECT DISTINCT unnest(
        (CASE WHEN block_nsfw THEN ARRAY['sexual'] ELSE '{}' END)::text[] ||
        (CASE WHEN block_innuendo THEN ARRAY['cum'] ELSE '{}' END)::text[]
    ) ORDER BY 1);

ALTER TABLE emote_streamer_settings DROP COLUMN IF EXISTS block_nsfw;
ALTER TABLE emote_streamer_settings DROP COLUMN IF EXISTS block_innuendo;

-- One row, pinned by a CHECKed boolean primary key: the platform default every
-- channel falls back to. Seeded blocking all six, so an unconfigured channel is
-- the safe one.
CREATE TABLE IF NOT EXISTS emote_platform_settings (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    blocked_classes text[] NOT NULL DEFAULT '{}',
    updated_at      timestamptz NOT NULL DEFAULT now()
);

INSERT INTO emote_platform_settings (id, blocked_classes)
VALUES (true, ARRAY['cum', 'drugs', 'flashing', 'hate', 'sexual', 'violence'])
ON CONFLICT (id) DO NOTHING;
