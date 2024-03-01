create table if not exists users (
    id bigserial primary key,

    twitch_login   text     not null,
    twitch_user_id integer  not null,

    twitch_refresh_token   text not null,
    twitch_access_token    text not null,

    session text not null, -- browser session id

    data jsonb not null default '{}'::jsonb, -- settings, etc

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique(twitch_user_id)
);

create index if not exists users_session_idx on users (session);
create index if not exists users_twitch_login_idx on users (lower(twitch_login));
create index if not exists users_twitch_user_id_idx on users (twitch_user_id);
