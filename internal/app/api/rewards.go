package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nicklaw5/helix/v2"
)

func (api *API) reward(rewardType db.TwitchRewardType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := ctxstore.GetUser(r.Context())
		if user == nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusUnauthorized,
				ErrorMessage: "not authorized",
			})
			return
		}

		characterIDStr := chi.URLParam(r, "character_id")
		characterID, err := uuid.Parse(characterIDStr)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
			})
			return
		}

		char, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "get character card: " + err.Error(),
			})
			return
		}

		// Use the generic reward creation function
		err = api.createReward(r.Context(), w, user, &characterID, char.Name, rewardType, "")
		if err != nil {
			return // Error already handled in createReward
		}
	}
}

type rewardChooseData struct {
	CharacterID   uuid.UUID
	CharacterName string

	RewardType      string // "tts" | "ai"
	RewardTypeLabel string

	Error            string
	ExistingRewardID string
}

func parseRewardTypeStr(s string) (db.TwitchRewardType, string, string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tts":
		return db.TwitchRewardTTS, "tts", "TTS", nil
	case "ai":
		return db.TwitchRewardAI, "ai", "AI", nil
	default:
		return 0, "", "", fmt.Errorf("invalid reward_type")
	}
}

func (api *API) rewardChoose(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
	}

	_, rtStr, rtLabel, err := parseRewardTypeStr("tts")
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "invalid reward_type",
		})
	}

	char, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "get character card: " + err.Error(),
		})
	}

	return getHtml("reward_choose.html", &rewardChooseData{
		CharacterID:     characterID,
		CharacterName:   char.Name,
		RewardType:      rtStr,
		RewardTypeLabel: rtLabel,
	})
}

func (api *API) rewardNew(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      "tts",
			RewardTypeLabel: "TTS",
			Error:           "failed to parse form",
		})
		return
	}

	rtIn := strings.TrimSpace(r.Form.Get("reward_type"))
	if rtIn == "" {
		rtIn = "tts"
	}
	rewardType, rtStr, rtLabel, err := parseRewardTypeStr(rtIn)
	if err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      "tts",
			RewardTypeLabel: "TTS",
			Error:           "invalid reward_type",
		})
		return
	}

	char, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      rtStr,
			RewardTypeLabel: rtLabel,
			Error:           "get character card: " + err.Error(),
		})
		return
	}

	if err := api.createRewardAndUpsert(r.Context(), user, &characterID, char.Name, rewardType, ""); err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   char.Name,
			RewardType:      rtStr,
			RewardTypeLabel: rtLabel,
			Error:           err.Error(),
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

func (api *API) rewardExisting(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      "tts",
			RewardTypeLabel: "TTS",
			Error:           "failed to parse form",
		})
		return
	}

	rtIn := strings.TrimSpace(r.Form.Get("reward_type"))
	if rtIn == "" {
		rtIn = "tts"
	}
	rewardType, rtStr, rtLabel, err := parseRewardTypeStr(rtIn)
	if err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      "tts",
			RewardTypeLabel: "TTS",
			Error:           "invalid reward_type",
		})
		return
	}

	existingID := strings.TrimSpace(r.Form.Get("twitch_reward_id"))
	if existingID == "" {
		char, _ := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
		charName := "Unknown character"
		if char != nil {
			charName = char.Name
		}
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:      characterID,
			CharacterName:    charName,
			RewardType:       rtStr,
			RewardTypeLabel:  rtLabel,
			Error:            "Reward ID is required",
			ExistingRewardID: existingID,
		})
		return
	}

	char, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:      characterID,
			CharacterName:    "Unknown character",
			RewardType:       rtStr,
			RewardTypeLabel:  rtLabel,
			Error:            "get character card: " + err.Error(),
			ExistingRewardID: existingID,
		})
		return
	}

	if err := api.db.UpsertTwitchReward(r.Context(), user.ID, &characterID, existingID, rewardType); err != nil {
		submitTab(w, "reward_choose.html", &rewardChooseData{
			CharacterID:      characterID,
			CharacterName:    char.Name,
			RewardType:       rtStr,
			RewardTypeLabel:  rtLabel,
			Error:            err.Error(),
			ExistingRewardID: existingID,
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

// createReward is a generic function to create Twitch rewards for both characters and universal rewards
func (api *API) createReward(ctx context.Context, w http.ResponseWriter, user *db.User, cardID *uuid.UUID, titlePrefix string, rewardType db.TwitchRewardType, prompt string) error {
	if err := api.createRewardAndUpsert(ctx, user, cardID, titlePrefix, rewardType, prompt); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
		return err
	}

	_, _ = w.Write([]byte("success"))
	return nil
}

func (api *API) createRewardAndUpsert(ctx context.Context, user *db.User, cardID *uuid.UUID, titlePrefix string, rewardType db.TwitchRewardType, prompt string) error {
	client, err := api.helixForUser(ctx, user)
	if err != nil {
		return err
	}

	if len(titlePrefix) > 0 {
		titlePrefix = titlePrefix + " "
	}
	title := titlePrefix + rewardType.String()

	resp, err := client.CreateCustomReward(&helix.ChannelCustomRewardsParams{
		BroadcasterID:                     strconv.Itoa(user.TwitchUserID),
		Title:                             title,
		Cost:                              10,
		Prompt:                            prompt,
		IsEnabled:                         true,
		BackgroundColor:                   "#A970FF",
		IsUserInputRequired:               true,
		ShouldRedemptionsSkipRequestQueue: false,
	})
	if err != nil || len(resp.Data.ChannelCustomRewards) == 0 {
		return api.rewardCreateError(user, title, resp, err)
	}

	rewardID := resp.Data.ChannelCustomRewards[0].ID
	api.logger.Info("twitch custom reward created", "user", user.TwitchLogin, "title", title, "reward_id", rewardID)

	switch rewardType {
	case db.TwitchRewardUniversalTTS:
		err = api.db.UpsertUniversalTTSReward(ctx, user.ID, rewardID)
	case db.TwitchRewardAgentic:
		err = api.db.UpsertAgenticReward(ctx, user.ID, rewardID)
	default:
		err = api.db.UpsertTwitchReward(ctx, user.ID, cardID, rewardID, rewardType)
	}
	if err != nil {
		api.logger.Error("failed to store created reward", "user", user.TwitchLogin, "reward_id", rewardID, "err", err)
		return fmt.Errorf("the reward %q was created on Twitch but saving it failed: %w", title, err)
	}

	return nil
}

// rewardCreateError logs the raw Twitch failure and returns the message the
// streamer sees. Twitch's own wording is kept only for cases not mapped here.
func (api *API) rewardCreateError(user *db.User, title string, resp *helix.ChannelCustomRewardResponse, err error) error {
	logger := api.logger.With("user", user.TwitchLogin, "twitch_user_id", user.TwitchUserID, "title", title)

	if err != nil {
		logger.Error("twitch create custom reward failed", "err", err)
		return errors.New("Twitch returned an unexpected response while creating the reward. Try again; if it keeps failing, log out and log in with Twitch again.")
	}

	logger.Error("twitch refused to create custom reward", "status", resp.StatusCode, "twitch_error", resp.Error, "twitch_message", resp.ErrorMessage)

	msg := strings.ToLower(resp.ErrorMessage)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("Twitch no longer accepts this login. Log out and log in with Twitch again.")
	case strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique"):
		return fmt.Errorf("A reward named %q already exists on your channel. Delete it in the Twitch dashboard, or link it with \"Use existing reward\".", title)
	case strings.Contains(msg, "maximum"):
		return errors.New("Your channel already has 50 custom rewards, which is Twitch's limit. Delete unused rewards in the Twitch dashboard and try again.")
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("Channel Points are not available on this channel. Twitch only enables them for Affiliates and Partners.")
	}

	if resp.ErrorMessage == "" {
		return fmt.Errorf("Twitch returned an unexpected response (HTTP %d) while creating the reward. Try again in a minute.", resp.StatusCode)
	}

	return fmt.Errorf("Twitch refused to create the reward (%d %s): %s", resp.StatusCode, resp.Error, resp.ErrorMessage)
}
