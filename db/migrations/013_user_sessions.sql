create table if not exists user_sessions (
    id         text        primary key,
    user_id    uuid        not null references users (id) on delete cascade,
    created_at timestamptz not null default now(),
    expires_at timestamptz not null
);

create index if not exists user_sessions_user_id_idx on user_sessions (user_id);

insert into user_sessions (id, user_id, expires_at)
select session, id, now() + interval '1 year'
from users
where session is not null and session <> ''
on conflict (id) do nothing;

-- users.session cannot be dropped: 001 re-creates its index on every apply.
-- It stays nullable and null so a re-apply cannot copy stale ids back in.
alter table users alter column session drop not null;

update users set session = null where session is not null;
