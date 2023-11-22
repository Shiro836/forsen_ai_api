create table if not exists char_cards (
    id integer primary key autoincrement null,
    user_id integer not null,
    char_name text not null,
    visibility integer not null default 0, -- 0=public 1=private
    card text not null,

    FOREIGN KEY(user_id) REFERENCES user_data(user_id),

    unique(char_name)
);

create index if not exists char_cards_char_name on char_cards(char_name);

create table if not exists reward_ids (
    id integer primary key autoincrement null,
    user_id integer not null,
    reward_id text not null,
    twitch_reward_id text not null,

    FOREIGN KEY(user_id) REFERENCES user_data(user_id),

    unique(user_id, reward_id)
);

create index if not exists reward_ids_reward_id on reward_ids(reward_id);
create index if not exists reward_ids_twitch_reward_id on reward_ids(twitch_reward_id);
