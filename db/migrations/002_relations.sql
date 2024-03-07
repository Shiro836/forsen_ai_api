create table if not exists relations (
    id bigserial primary key,

    user_id_1 bigint not null references users(id) ON DELETE CASCADE,
    user_id_2 bigint not null references users(id) ON DELETE CASCADE,

    relation_type integer not null,

    data jsonb not null default '{}'::jsonb, -- extra data

    created_at timestamp not null default now(),
    updated_at timestamp not null default now(),

    unique (user_id_1, user_id_2, relation_type)
);

create index if not exists relations_user_id_1_idx on relations (user_id_1);
create index if not exists relations_user_id_2_idx on relations (user_id_2);
create index if not exists relations_relation_type_idx on relations (relation_type);