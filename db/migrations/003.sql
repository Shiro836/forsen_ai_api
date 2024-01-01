create table if not exists live2dmodels (
    id integer primary key autoincrement null,
    char_name text not null,
    model text not null,

    unique(char_name)
);

create index if not exists live2dmodels_char_name on live2dmodels(char_name);
