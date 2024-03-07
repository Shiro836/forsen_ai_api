create table if not exists msg_queue (
    id bigserial primary key,

    user_id bigint not null references users(id),

    -- twitch chat message data
    twitch_user_name    text not null,
	twitch_message      text not null,
	twitch_reward_id    text,
    -- end of twitch chat message data

    status integer not null,

    updated integer not null, -- used to send updated rows to user

    data jsonb not null default '{}'::jsonb
);

-- since we need to get next message with state 'wait' for each user with lowest id we need index
create index if not exists msg_queue_user_id_status_idx on msg_queue (user_id, status, id);

-- we need to let user get new updates based on his last updated row
create index if not exists msg_queue_user_id_updated_idx on msg_queue (user_id, updated);
