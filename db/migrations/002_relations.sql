create table if not exists relations (
    id uuid default uuid_generate_v7() primary key,

    twitch_login_1   text       not null,
    twitch_user_id_1 integer    not null,

    twitch_login_2   text       not null,
    twitch_user_id_2 integer    not null,

    relation_type integer not null,

    data jsonb not null default '{}'::jsonb,

    updated_at timestamp not null default now(),

    unique (twitch_user_id_1, twitch_user_id_2, relation_type)
);

create index if not exists relations_twitch_user_id_1_idx on relations (twitch_user_id_1);
create index if not exists relations_twitch_user_id_2_idx on relations (twitch_user_id_2);
create index if not exists relations_relation_type_idx on relations (relation_type);
create index if not exists relations_data_idx on relations using gin (data);
