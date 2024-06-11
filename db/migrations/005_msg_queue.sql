create table if not exists msg_queue (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid not null references users(id),

    msg_type integer not null,

    msg jsonb not null default '{}'::jsonb,

    status integer not null,

    updated integer not null, -- used to send updated rows to user

    data jsonb not null default '{}'::jsonb
);

-- we need to get next message with state 'wait' for each user with lowest id
create index if not exists msg_queue_user_id_status_idx on msg_queue (user_id, msg_type, status, id);

-- we need to let user get new updates based on his last updated row
create index if not exists msg_queue_user_id_updated_idx on msg_queue (user_id, updated);
