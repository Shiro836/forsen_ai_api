create table if not exists user_data (
    id integer primary key autoincrement null,

    login   text not null,
    user_id integer not null,

    refresh_token   text not null,
    access_token    text not null,

    session text,

    settings jsonb not null default '{}'::jsonb,

    unique(user_id)
);
