create table if not exists msg_queue (
    id integer primary key autoincrement null,
    user_id integer not null,

    user_name           text not null,
	message             text not null,
	custom_reward_id    text not null,

    state text not null,

    updated integer,

    FOREIGN KEY(user_id) REFERENCES user_data(user_id)
);

create index if not exists msg_queue_updated_state_user_id on msg_queue(updated, state, user_id, id);

create unique index if not exists user_data_lower_login on user_data(lower(login));
create unique index if not exists char_cards_lower_char_name on char_cards(lower(char_name));
create unique index if not exists voices_lower_char_name on voices(lower(char_name));
create unique index if not exists custom_chars_user_id_lower_char_name on custom_chars(user_id, lower(char_name));
