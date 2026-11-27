create table if not exists char_card_daily_stats (
    card_id     uuid not null references char_cards(id) on delete cascade,
    day         date not null,
    ai_redeems  integer not null default 0,
    tts_redeems integer not null default 0,
    primary key (card_id, day)
);

create index if not exists char_card_daily_stats_day_idx on char_card_daily_stats (day);
