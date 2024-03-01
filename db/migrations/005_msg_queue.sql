create table if not exists msg_queue (
    id bigserial primary key,

    user_id bigint not null references users(id),

    -- twitch chat message data
    user_name           text not null,
	message             text not null,
	twitch_reward_id    text,
    -- end of twitch chat message data

    state text not null, -- wait(waiting to be processed), deleted(deleted by mod), processed, current(currently processed)

    updated integer not null, -- used to send updated rows to user

    data jsonb not null default '{}'::jsonb
);

-- since we need to get next message with state 'wait' for each user with lowest id we need index
create index if not exists msg_queue_user_id_state_idx on msg_queue (user_id, state, id);

-- we need to let user get new updates based on his last updated row
create index if not exists msg_queue_user_id_updated_idx on msg_queue (user_id, updated);
