create table if not exists char_cards (
    id bigserial primary key,

    char_name text not null,

    owner_user_id bigint not null references users(id),

    public boolean not null default false,

    data jsonb not null default '{}'::jsonb,

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique(owner_user_id, char_name)
);

create index if not exists char_cards_owner_user_id_idx on char_cards (owner_user_id);
create index if not exists char_cards_public_idx on char_cards (public);
