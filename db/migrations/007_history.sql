create table if not exists history (
    id bigserial primary key,

    user_id_initiator bigint not null references users(id),
    target_twitch_username text not null,
    target_user_id bigint,

    action integer not null,

    permission integer not null,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now()
);

create index if not exists history_created_at_idx on history (created_at);
