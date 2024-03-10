create table if not exists char_cards (
    id bigserial primary key,

    owner_user_id bigint not null references users(id),

    char_name           text not null,
    char_description    text not null,

    public boolean not null default false,

    redeems int not null default 0,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique(owner_user_id, char_name)
);

create index if not exists char_cards_owner_user_id_idx on char_cards (owner_user_id);

create extension if not exists pg_trgm;
create index if not exists char_cards_char_name_trgm_idx on char_cards using gin (char_name gin_trgm_ops);
create index if not exists char_cards_redeems_idx on char_cards (redeems);
