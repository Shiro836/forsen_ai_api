package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"context"
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
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
			CharacterID:     characterID,
			CharacterName:   "Unknown character",
			RewardType:      rtStr,
			RewardTypeLabel: rtLabel,
			Error:           "get character card: " + err.Error(),
		})
		return
	}

	if err := api.createRewardAndUpsert(r.Context(), user, &characterID, char.Name, rewardType, ""); err != nil {
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
		_ = html.ExecuteTemplate(w, "reward_choose.html", &rewardChooseData{
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
	client, err := api.twitchClient.NewHelixClient(user.TwitchAccessToken, user.TwitchRefreshToken)
	if err != nil {
		return fmt.Errorf("create helix client: %w", err)
	}

	if prompt == "" {
		prompt = ""
	}

	if len(titlePrefix) > 0 {
		titlePrefix = titlePrefix + " "
	}

	resp, err := client.CreateCustomReward(&helix.ChannelCustomRewardsParams{
		BroadcasterID:                     strconv.Itoa(user.TwitchUserID),
		Title:                             titlePrefix + rewardType.String(),
		Cost:                              10,
		Prompt:                            prompt,
		IsEnabled:                         true,
		BackgroundColor:                   "#A970FF",
		IsUserInputRequired:               true,
		ShouldRedemptionsSkipRequestQueue: false,
	})
	if err != nil {
		return fmt.Errorf("helix - create custom reward: %w", err)
	}

	if len(resp.Data.ChannelCustomRewards) == 0 {
		return fmt.Errorf("helix - create custom reward: no custom reward created (%s, %s)", resp.Error, resp.ErrorMessage)
	}

	rewardID := resp.Data.ChannelCustomRewards[0].ID

	switch rewardType {
	case db.TwitchRewardUniversalTTS:
		return api.db.UpsertUniversalTTSReward(ctx, user.ID, rewardID)
	case db.TwitchRewardAgentic:
		return api.db.UpsertAgenticReward(ctx, user.ID, rewardID)
	default:
		return api.db.UpsertTwitchReward(ctx, user.ID, cardID, rewardID, rewardType)
	}
}
