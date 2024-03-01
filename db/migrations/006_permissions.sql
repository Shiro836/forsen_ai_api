create table if not exists permissions (
    id bigserial primary key,

    twitch_login   text     not null,
    twitch_user_id integer  not null,

    status text not null,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique (twitch_user_id, status)
);

create index if not exists permissions_twitch_user_id_idx on permissions (twitch_user_id);
create index if not exists permissions_twitch_login_idx on permissions (twitch_login);

create index if not exists permissions_status_idx on permissions (status);
