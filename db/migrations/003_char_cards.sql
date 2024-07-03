create table if not exists char_cards (
    id uuid default uuid_generate_v7() primary key,

    owner_user_id uuid not null references users(id),

    name        text not null,
    description text not null,

    public boolean not null default false,

    redeems bigint not null default 0,

    data jsonb not null default '{}'::jsonb,

    updated_at timestamp not null default now(),

    unique(owner_user_id, name)
);

create index if not exists char_cards_owner_user_id_idx on char_cards (owner_user_id);
create index if not exists char_cards_name_trgm_idx on char_cards using gin (name gin_trgm_ops);
create index if not exists char_cards_redeems_idx on char_cards (redeems);
