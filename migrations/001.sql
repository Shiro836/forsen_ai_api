create table if not exists user_data (
    id integer primary key autoincrement null,

    login   text not null,
    user_id integer not null,

    refresh_token   text not null,
    access_token    text not null,

    session text,

    reward_id text,

    chat            boolean not null default true,
    chan_pts        boolean not null default true,
    follows         boolean not null default true,
    subs            boolean not null default true,
    raids           boolean not null default true,
    random_events   boolean not null default true,
    events_interval text    not null default 60, -- in seconds
    custom_context  text    not null default 'Forsen tries to answer every question thoroughly. ###PROMPT: How did you get into streaming? ###FORSEN: My goal was always to get rich. I always tried to get sponsors and shit. And I saw that opportunity in streaming.',

    unique(user_id)
);

create index if not exists user_data_user_id on user_data(user_id);
create index if not exists user_data_login on user_data(login);
create index if not exists user_data_session on user_data(session);

create table if not exists whitelist (
    id integer primary key autoincrement null,

    login text not null,
    is_mod boolean not null default false,
    added_by text not null,
    banned_by text,

    unique(login)
);

create index if not exists whitelist_banned_by on whitelist(banned_by);
