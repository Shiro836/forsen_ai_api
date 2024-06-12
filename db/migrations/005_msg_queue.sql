create table if not exists msg_queue (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid not null references users(id),

    msg jsonb not null default '{}'::jsonb,

    status integer not null,

    updated integer not null, -- used to send updated rows to user

    data jsonb not null default '{}'::jsonb
);

create index if not exists msg_queue_user_id_idx on msg_queue (user_id);
create index if not exists msg_queue_status_idx on msg_queue (status);
create index if not exists msg_queue_updated_idx on msg_queue (updated);
