create table if not exists filters (
    id integer primary key autoincrement null,
    user_id integer not null,
    filters text not null,

    unique(user_id)
);
