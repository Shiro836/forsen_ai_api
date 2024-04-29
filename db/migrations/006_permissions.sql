create table if not exists permissions (
    id bigserial primary key,

    -- we don't use user_id from users table, because we need to be able to add permissions for users who don't have an account yet
    twitch_login   text     not null,
    twitch_user_id integer  not null,

    status integer not null,
    permission integer not null,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique (twitch_user_id, permission)
);

create index if not exists permissions_twitch_user_id_idx on permissions (twitch_user_id);
create index if not exists permissions_twitch_login_idx on permissions (lower(twitch_login));

create index if not exists permissions_status_idx on permissions (status);
create index if not exists permissions_permission_idx on permissions (permission);
