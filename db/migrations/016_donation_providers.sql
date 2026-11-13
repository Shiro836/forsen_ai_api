create table if not exists donation_providers (
    user_id       uuid        not null references users (id) on delete cascade,
    provider      text        not null,
    access_token  text        not null,
    refresh_token text,
    scopes        text[]      not null default '{}',
    updated_at    timestamptz not null default now(),
    primary key (user_id, provider)
);
