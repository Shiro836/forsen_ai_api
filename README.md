# forsen_ai_api
forsen ai api

forsen ai - ai entertainment service for twitch streamers. Each streamer has obs browser source. And each streamer has lua code parsing twitch chat, executing commands, and sending audio and text to the browser source in their obs.

Take for example this lua script:
```lua
while true do
	user, msg, reward_id = get_next_event()
    tts("forsen", msg)
end
```
get_next_event is built-in command that waits for next message from twitch chat. tts is also built-in command used for converting text to speech using certain voice(in this case forsen's voice) and sending it streamer's obs browser source.
