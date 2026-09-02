-- The vision model cannot judge flashing: it is shown ten frames sampled across
-- the whole animation and asked to infer a frequency, which is far below Nyquist
-- for anything that actually strobes. A photometric pass over every frame owns
-- the class now, and these columns are what it writes.
--
-- The rates are stored rather than only the derived boolean so that re-tuning
-- the hazard cut is a WHERE clause instead of a reprocessing pass over S3.
-- flash_hz is the shipped net-area rule; flash_hz_max is the same animation
-- under WCAG's literal per-direction area gate, which over-blocks a sprite's own
-- footprint but is the number to look at if fast high-contrast shake ever has to
-- count as flashing.
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS flash_hz double precision NOT NULL DEFAULT 0;
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS flash_hz_max double precision NOT NULL DEFAULT 0;
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS red_flash_hz double precision NOT NULL DEFAULT 0;

-- NULL means never measured, which is what tells an unmeasured row apart from
-- one measured as perfectly still.
ALTER TABLE emote_moderation ADD COLUMN IF NOT EXISTS flash_measured_at timestamptz;
