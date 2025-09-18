create sequence if not exists updated_seq start 1;

create table if not exists msg_queue (
    id uuid default uuid_generate_v7() primary key,

    user_id uuid not null references users(id),

    msg jsonb not null default '{}'::jsonb,

    status integer not null,

    updated bigint not null default nextval('updated_seq'), -- used to send updated rows to user

    data jsonb not null default '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS msg_queue_user_status_updated_id_idx
ON msg_queue (user_id, status, updated, id);

CREATE INDEX IF NOT EXISTS msg_queue_user_updated_idx
ON msg_queue (user_id, updated);
