create table if not exists custom_chars (
    id integer primary key autoincrement null,
    user_id integer not null,
    char_name text not null,

    FOREIGN KEY(user_id) REFERENCES user_data(user_id),

    unique(user_id, char_name)
);
