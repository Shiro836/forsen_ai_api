CREATE TABLE IF NOT EXISTS clanker_queue (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_login TEXT NOT NULL,
    channel_user_id INT NOT NULL,
    sender_login TEXT NOT NULL,
    sender_user_id INT NOT NULL,
    message TEXT NOT NULL,
    status INT NOT NULL DEFAULT 1,
    unique_id TEXT UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_clanker_queue_status ON clanker_queue (status);
