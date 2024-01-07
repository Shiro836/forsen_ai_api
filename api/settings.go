package api

import (
	"app/db"
	"app/slg"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/nicklaw5/helix/v2"
)

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err == nil && len(cookie.Value) != 0 {
		data, err := os.ReadFile("client/settings.html")
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte("failed to read settings.html"))

			return
		}

		w.Header().Add("Content-Type", "text/html")
		w.Write(data)

		return
	}

	data, err := os.ReadFile("client/settings_login.html")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read settings.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func (api *API) channelPointsRewardCreateHandler(w http.ResponseWriter, r *http.Request) {
	rewardID := chi.URLParam(r, "reward_id")
	if len(rewardID) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty reward_id in path"))

		return
	}

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if !isValidUser(userData.UserLoginData.UserName, w) {
		return
	} else if twitchClient, err := api.twitch.NewHelixClient(userData.AccessToken, userData.RefreshToken); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if resp, err := twitchClient.CreateCustomReward(&helix.ChannelCustomRewardsParams{
		BroadcasterID: strconv.Itoa(userData.UserLoginData.UserId),

		Title:               rewardID,
		Cost:                1,
		Prompt:              "reward_id: \"" + rewardID + "\"",
		IsEnabled:           true,
		BackgroundColor:     "#00FF00",
		IsUserInputRequired: true,
	}); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if len(resp.Data.ChannelCustomRewards) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("No new rewards created"))
	} else if err = db.SaveRewardID(rewardID, resp.Data.ChannelCustomRewards[0].ID, userData.ID); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else {
		// api.connManager.NotifyUpdateSettings(userData.UserLoginData.UserName)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}
}

var DefaultLuaScript = `

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
  msg = filter_text(msg)

  local sentences = splitTextIntoSentences(msg)
 
    total = ""

    for i, sentence in ipairs(sentences) do
      total=total..sentence
      text(total)
      if #sentence > 2 then
            tts(voice, sentence)
        end
    end
  text(" ")
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
forsen2 = get_char_card("forsen2")
kazuma = get_char_card("kazuma")
megumin = get_char_card("megumin")
neuro = get_char_card("neuro")
gordon = get_char_card("gordon")
biden = get_char_card("biden")
trump = get_char_card("trump")
daphne_greengrass = get_char_card("daphne_greengrass")
harry_potter = get_char_card("harry_potter")
jesus = get_char_card("jesus")
adolf = get_char_card("adolf4")
horse_cock = get_char_card("horse_cock")
gura = get_char_card("gura")
wiz = get_char_card("wiz")
aqua2 = get_char_card("aqua2")
darkness = get_char_card("darkness")

custom_chars = {get_custom_chars()}
custom_cards = {}
for i=1, #custom_chars do
  table.insert(custom_cards, get_char_card(broadcaster.."_"..custom_chars[i]))
end

while true do
  user, msg, reward_id = get_next_event()
  if reward_id == "tts forsen" then
    set_image("/static/images/forsen.png")
    gradual_tts("forsen", msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask forsen" then
    set_image("/static/images/forsen.png")
    ask("forsen", forsen2, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask neuro" then
    set_image("/static/images/neuro.png")
    ask("neuro", neuro, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask megumin" then
    set_image("/static/images/megumin.png")
    ask("megumin", megumin, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask kazuma" then
    set_image("/static/images/kazuma.png")
    ask("kazuma", kazuma, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask gordon" then
    set_image("/static/images/gordon.png")
    ask("gordon", gordon, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask trump" then
    set_image("/static/images/trump.png")
    ask("trump", trump, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask biden" then
    set_image("/static/images/biden.png")
    ask("biden", biden, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask daphne_greengrass" then
    ask("daphne_greengrass", daphne_greengrass, msg)
  elseif reward_id == "ask harry_potter" then
    set_image("/static/images/harry.png")
    ask("harry_potter", harry_potter, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask jesus" then
    set_image("/static/images/jesus.png")
    ask("jesus", jesus, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask gura" then
    set_image("/static/images/gura.png")
    ask("gura", gura, msg)
    set_image("/static/images/empty.png")
  elseif reward_id == "ask adolf" then
    set_image("/static/images/adolf.png")
    ask("adolf2", adolf, msg)
    set_image("/static/images/empty.png")
  else
    for i = 1, #custom_chars do
      if reward_id == custom_chars[i] then
        ask(broadcaster.."_"..custom_chars[i], custom_cards[i], msg)
        break
      end
    end
  end
end
`

func getSettings(w http.ResponseWriter, r *http.Request) {
	var settings *db.Settings

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get settings"))

		return
	} else if settings, err = db.GetDbSettings(userData.UserLoginData.UserName); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get settings"))

		return
	}

	if len(settings.LuaScript) == 0 {
		settings.LuaScript = DefaultLuaScript
	}

	if data, err := json.Marshal(settings); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to marshal settings"))
	} else {
		w.Write(data)
	}
}

func (api *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	var settings *db.Settings

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if data, err := io.ReadAll(r.Body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to read request body"))
	} else if err = json.Unmarshal(data, &settings); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to unmarshal request json body"))
	} else if err = db.UpdateDbSettings(settings, userData.UserLoginData.UserName); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to update settings in db"))
	} else {
		api.connManager.NotifyUpdateSettings(userData.UserLoginData.UserName)
		w.Write([]byte("success"))
	}
}

func (api *API) GetFilters(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	} else if filters, err := db.GetFilters(userData.ID); err != nil {
		w.Write([]byte(""))

		// w.WriteHeader(http.StatusInternalServerError)
		// w.Write([]byte(err.Error()))

		return
	} else {
		w.Write([]byte(filters))
	}
}

func (api *API) UpdateFilters(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	} else if r = r.WithContext(slg.WithSlog(r.Context(), slog.With("user", userData.UserLoginData.UserName))); false {
	} else if filters, err := io.ReadAll(r.Body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read body: " + err.Error()))

		return
	} else if slg.GetSlog(r.Context()).Info("", "filters", filters); false {
	} else if err := db.UpdateFilters(userData.ID, strings.ReplaceAll(string(filters), " ", "")); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))

		return
	} else {
		// api.connManager.NotifyUpdateSettings(userData.UserLoginData.UserName)
		w.Write([]byte(filters))
	}
}
