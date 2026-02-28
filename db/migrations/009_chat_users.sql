CREATE TABLE IF NOT EXISTS chat_users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    twitch_user_id INTEGER NOT NULL UNIQUE,
    twitch_login TEXT NOT NULL,
    voice TEXT,
    reward_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now()
);
