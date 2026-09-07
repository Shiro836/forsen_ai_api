alter table msg_queue add column if not exists archive jsonb;

create table if not exists export_state (
    watermark bigint not null,
    updated_at timestamptz not null default now()
);

insert into export_state (watermark)
select 0 where not exists (select 1 from export_state);
