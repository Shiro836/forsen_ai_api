create table if not exists twitch_tokens (
    user_id       uuid        primary key references users (id) on delete cascade,
    access_token  text        not null,
    refresh_token text        not null,
    scopes        text[]      not null default '{}',
    updated_at    timestamptz not null default now()
);

insert into twitch_tokens (user_id, access_token, refresh_token)
select id, twitch_access_token, twitch_refresh_token
from users
where twitch_access_token is not null and twitch_refresh_token is not null
on conflict (user_id) do nothing;

alter table users alter column twitch_access_token drop not null;
alter table users alter column twitch_refresh_token drop not null;

update users
set twitch_access_token = null, twitch_refresh_token = null
where twitch_access_token is not null or twitch_refresh_token is not null;
