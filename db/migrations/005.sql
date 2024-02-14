create table if not exists custom_chars (
    id integer primary key autoincrement null,
    user_id integer not null,
    char_name text not null,

    state integer not null default 0,

    FOREIGN KEY(user_id) REFERENCES user_data(id),

    unique(user_id, char_name)
);
