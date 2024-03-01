create table if not exists reward_buttons (
    id bigserial primary key,

    user_id bigint  not null references users(id) on delete cascade,
    card_id bigint  not null references char_cards(id) on delete cascade,

    twitch_reward_id text not null,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique (user_id, card_id)
);

create index if not exists reward_buttons_twitch_reward_id_idx on reward_buttons (twitch_reward_id);
