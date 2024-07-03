create table if not exists history (
    id uuid default uuid_generate_v7() primary key,

    initiator_user_id uuid not null references users(id),

    target_twitch_username text not null,
    target_user_id bigint, -- TODO: wtf is this

    action integer not null,

    permission integer not null,

    data jsonb not null default '{}'::jsonb
);
