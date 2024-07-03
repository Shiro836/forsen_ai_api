create table if not exists reward_buttons (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid not null references users(id) on delete cascade,
    card_id uuid not null references char_cards(id) on delete cascade,

    twitch_reward_id text not null,

    reward_type integer not null,

    data jsonb not null default '{}'::jsonb,

    updated_at timestamp not null default now(),

    unique (user_id, card_id, reward_type)
);

create index if not exists reward_buttons_twitch_reward_id_idx on reward_buttons (twitch_reward_id);
