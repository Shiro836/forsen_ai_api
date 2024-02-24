package api

import (
	"app/db"
	"app/pkg/slg"
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
function prep(s, char_name, user)
    return s:gsub("{{char}}", "###"..char_name):gsub("{{user}}", "###"..user)
end

function prep_card(card, user)
  return card.name .. " - description: "
    .. prep(card.description, card.name, user) 
    .. " personality: "
    .. prep(card.personality, card.name, user) 
    .. " scenario: "
    .. prep(card.scenario, card.name, user) 
    .. " message examples: " 
    .. prep(card.message_example, card.name, user) 
    .. "<START>"
    .. prep(card.first_message, card.name, user)
	.. "###" .. user .. ": How is your day?"
	.. "###" .. card.name .. "It was very good, thx for asking. I did a lot of things today and I feel very good."
end

function ask(msg_id, voice, card, request, img_link)
  prefix = prep_card(card, user)

  local ai_resp = ai(prefix .. " ###" .. user.. ": " .. request .. " ###" .. card.name .. ": ")
  local say1 = user .. " asked me: " .. request

  if img_link ~= nil then
    set_image(msg_id, img_link)
  end

  say1 = filter_text(say1)
  ai_resp = filter_text(ai_resp)

  tts_text(msg_id, voice, say1)
  tts_text(msg_id, voice, ai_resp)
  
  -- clear the text
  text(msg_id, " ")

  -- clear the image
  set_image(msg_id, "")
end

forsen = get_char_card("forsen2")
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
gura = get_char_card("gura")

while true do
  msg_id, user, msg, reward_id = get_next_event()
  if reward_id == "tts forsen" then
    tts_text(msg_id, "forsen", msg)
    text(msg_id, " ")
  elseif reward_id == "ask forsen" then
    ask(msg_id, "forsen", forsen, msg, "/static/images/forsen.png")
  elseif reward_id == "ask neuro" then
    ask(msg_id, "neuro", neuro, msg, "/static/images/neuro.png")
  elseif reward_id == "ask megumin" then
    ask(msg_id, "megumin", megumin, msg, "/static/images/megumin.png")
  elseif reward_id == "ask kazuma" then
    ask(msg_id, "kazuma", kazuma, msg, "/static/images/kazuma.png")
  elseif reward_id == "ask gordon" then
    ask(msg_id, "gordon", gordon, msg, "/static/images/gordon.png")
  elseif reward_id == "ask trump" then
    ask(msg_id, "trump", trump, msg, "/static/images/trump.png")
  elseif reward_id == "ask biden" then
    ask(msg_id, "biden", biden, msg, "/static/images/biden.png")
  elseif reward_id == "ask harry_potter" then
    ask(msg_id, "harry_potter", harry_potter, msg, "/static/images/harry.png")
  elseif reward_id == "ask jesus" then
    ask(msg_id, "jesus", jesus, msg, "/static/images/jesus.png")
  elseif reward_id == "ask gura" then
    ask(msg_id, "gura", gura, msg, "/static/images/gura.png")
  elseif reward_id == "ask adolf" then
    ask(msg_id, "adolf2", adolf, msg, "/static/images/adolf.png")
  elseif #reward_id ~= 0 then
    local char_name = broadcaster.."_"..reward_id
    card = {get_char_card(char_name)}
    if #card ~= 0 then
      ask(msg_id, char_name, card[1], msg, card.img_link)
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
