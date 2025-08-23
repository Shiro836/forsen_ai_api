package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"net/http"
	"strconv"

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

		client, err := api.twitchClient.NewHelixClient(user.TwitchAccessToken, user.TwitchRefreshToken)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "create helix client: " + err.Error(),
			})
			return
		}

		resp, err := client.CreateCustomReward(&helix.ChannelCustomRewardsParams{
			BroadcasterID:                     strconv.Itoa(user.TwitchUserID),
			Title:                             char.Name + " " + rewardType.String(),
			Cost:                              10,
			Prompt:                            "",
			IsEnabled:                         true,
			BackgroundColor:                   "#A970FF",
			IsUserInputRequired:               true,
			ShouldRedemptionsSkipRequestQueue: false,
		})
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "helix - create custom reward: " + err.Error(),
			})
			return
		}

		if len(resp.Data.ChannelCustomRewards) == 0 {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "helix - create custom reward: no custom reward created" + resp.Error + ", " + resp.ErrorMessage,
			})
			return
		}

		err = api.db.UpsertTwitchReward(r.Context(), user.ID, characterID, resp.Data.ChannelCustomRewards[0].ID, rewardType)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
			return
		}

		_, _ = w.Write([]byte("success"))
	}
}
