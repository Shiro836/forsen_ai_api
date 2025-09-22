create table if not exists reward_buttons (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid not null references users(id) on delete cascade,
    card_id uuid references char_cards(id) on delete cascade,

    twitch_reward_id text not null,

    reward_type integer not null,

    data jsonb not null default '{}'::jsonb,

    updated_at timestamp not null default now()
);

-- Create separate unique constraints for character-based and universal rewards
-- Character-based rewards: (user_id, card_id, reward_type) must be unique where card_id IS NOT NULL
create unique index if not exists reward_buttons_character_rewards_unique 
    on reward_buttons (user_id, card_id, reward_type) 
    where card_id is not null;

-- Universal rewards: (user_id, reward_type) must be unique where card_id IS NULL
create unique index if not exists reward_buttons_universal_rewards_unique 
    on reward_buttons (user_id, reward_type) 
    where card_id is null;

create index if not exists reward_buttons_twitch_reward_id_idx on reward_buttons (twitch_reward_id);
