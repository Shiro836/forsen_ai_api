create table if not exists char_cards (
    id integer primary key autoincrement null,
    visibility text not null default 'private',
);
