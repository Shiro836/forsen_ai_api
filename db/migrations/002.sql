create table if not exists char_cards (
    id integer primary key autoincrement null,
    user_id 
    visibility integer not null default 0, -- 0=public,1=private
    card text not null
);
