-- A character or special reward can be bound to a channel-points reward and a
-- bits power-up at once: points stay the free tier, bits sit on top. Key the
-- binding by its Twitch reward id instead of by reward type.
drop index if exists reward_buttons_character_rewards_unique;
drop index if exists reward_buttons_universal_rewards_unique;

create unique index if not exists reward_buttons_character_bindings_unique
    on reward_buttons (user_id, card_id, reward_type, twitch_reward_id)
    where card_id is not null;

create unique index if not exists reward_buttons_universal_bindings_unique
    on reward_buttons (user_id, reward_type, twitch_reward_id)
    where card_id is null;
