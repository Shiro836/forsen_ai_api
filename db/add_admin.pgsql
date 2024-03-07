insert into permissions ("twitch_login", "twitch_user_id", "status", "permission") values ('shiro836_', 82054454, 1, 2);
delete from permissions where lower("twitch_login") = 'shiro836_' and "permission" = 1;
