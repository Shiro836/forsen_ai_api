# forsen_ai_api
forsen ai api

Forsen AI is an AI entertainment service for Twitch streamers. Each streamer has an OBS browser source. And each streamer has a lua code parsing twitch chat, executing commands, and sending audio and text to the browser source in their obs.

Take, for example, this Lua script:
```lua
while true do
  user, msg, reward_id = get_next_event()
  tts("forsen", msg)
end
```
get_next_event is a built-in function that waits for the next message from Twitch chat. tts is also a built-in function used for converting text to speech using a certain voice (in this case, Forsen's voice) and sending it to the streamer's obs browser source.

https://github.com/Shiro836/forsen_ai_api/assets/17294277/d19a6a0a-2a4c-4192-b9d5-f5645329c0ea

The third most important built-in function is ai(msg). And you can use text(txt) function to show text in an OBS browser source.

```lua
while true do
  user, msg, reward_id = get_next_event()

  ai_response = ai(msg)

  text(user.." asked me: "..msg)
  tts("forsen", user.." asked me: "..msg)

  text(ai_response)
  tts("forsen", ai_response)
end
```

https://github.com/Shiro836/forsen_ai_api/assets/17294277/c7e13eb1-dcfd-4f42-9aaa-7d038c2c4b90

As you can see, ai function is just querying LLM to finish the text. For a "funny" result, you have to build context or personality yourself.

For that purpose, there is another built-in function to query character cards that can be obtained from [chub.ai](https://chub.ai)(!!NSFW WARNING!!).

Here is the complete script to make two characters, Megumin and Kazuma from Konosuba, have a rant about a theme that was requested from a Twitch chat:
```lua
function splitTextIntoSentences(text)
    local sentences = {}
    local start = 1
    local sentenceEnd = nil

    repeat
        local _, e = text:find('[%.%?!]', start)
        if e then
            sentenceEnd = e
            local sentence = text:sub(start, sentenceEnd)
            if text:sub(sentenceEnd, sentenceEnd) ~= ' ' then
                sentence = sentence .. ' '
            end
            table.insert(sentences, sentence)
            start = sentenceEnd + 2
        else
            table.insert(sentences, text:sub(start))
            break
        end
    until start > #text

    return sentences
end

function startswith(text, prefix)
    if #prefix > #text then
      return false
    end
  return text:find(prefix, 1, true) == 1
end

function prep(s, char_name, user)
    return s:gsub("{{char}}", "###"..char_name):gsub("{{user}}", "###"..user)
end

function prep_card(card, user)
  return card.name .. " - description: "
    .. prep(card.description, card.name, user) .. " personality: "
    .. prep(card.personality, card.name, user) .. " message examples: " 
    .. prep(card.message_example, card.name, user) .. " scenario: "
    .. prep(card.scenario, card.name, user) .. "<START>"
    .. prep(card.first_message, card.name, user) .. "###" .. user
    .. ": How is your day?" .. "###" .. card.name
    .. "It was very good, thx for asking. I did a lot of things today and I feel very good."
end

function gradual_tts(voice, msg)
  local sentences = splitTextIntoSentences(msg)
 
    total = ""
  
    for i, sentence in ipairs(sentences) do
      total=total..sentence
      text(total)
      if #sentence > 2 then
            tts(voice, sentence)
        end
    end
end

function ask(voice, card, request)
    prefix = prep_card(card, user)

  local ai_resp = ai(prefix .. " ###" .. user.. ": " .. request .. " ###" .. card.name .. ": ")
  local say1 = user .. " asked me: " .. request

  gradual_tts(voice, say1)
  gradual_tts(voice, ai_resp)
end

function discuss(card1, card2, voice1, voice2, theme, times)
    prefix1 = prep_card(card1, card2.name)
    prefix2 = prep_card(card2, card1.name)

    mem = "Let's discuss " .. theme .. "."
    gradual_tts(voice1, mem)
    mem = "###".. card1.name .. ": " .. mem

    for i=1,times do
      ai_resp = ai(prefix2..mem.." ###"..card2.name..": ")
      gradual_tts(voice2, ai_resp)
      mem = mem .. ai_resp
      ai_resp = ai(prefix1..mem.."###"..card1.name..": ")
      gradual_tts(voice1, ai_resp)
      mem = mem .. ai_resp
    end
end

forsen = get_char_card("forsen")
kazuma = get_char_card("kazuma")
megumin = get_char_card("megumin")

while true do
  user, msg, reward_id = get_next_event()
  if reward_id == "tts" then
    gradual_tts("forsen", msg)
  elseif reward_id == "ask" then
    ask("forsen", forsen, msg)
  elseif reward_id == "discuss" then
    discuss(kazuma, megumin, "kazuma", "megumin", msg, 4)
  elseif startswith(msg, "!tts ") then
    gradual_tts("forsen", string.sub(msg, #"!tts "+1, #msg))
  elseif startswith(msg, "!ask ") then
    ask("forsen", forsen, string.sub(msg, #"!ask "+1, #msg))
  elseif startswith(msg, "!discuss ") then
    discuss(kazuma, megumin, "kazuma", "megumin", string.sub(msg, #"!discuss "+1, #msg), 4)
  end
end
```


https://github.com/Shiro836/forsen_ai_api/assets/17294277/e201a624-d1e2-4aa2-819d-95044411249d


