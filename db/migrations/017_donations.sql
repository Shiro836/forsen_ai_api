ALTER TABLE donation_providers
    ADD COLUMN IF NOT EXISTS expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS status_error text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS status_updated_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_event_at timestamptz;

CREATE TABLE IF NOT EXISTS donation_rates (
    id         integer     PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    rates      jsonb       NOT NULL,
    fetched_at timestamptz NOT NULL
);
