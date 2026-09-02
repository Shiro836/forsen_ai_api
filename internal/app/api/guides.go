package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"time"
)

type guideStep struct {
	Number       int
	Title        string
	PreviousPath string
	NextPath     string
}

type guidePage struct {
	Step guideStep
	URL  string
}

var obsGuideSteps = map[string]guideStep{
	"/": {
		Number:   1,
		Title:    "Add a Browser Source",
		NextPath: "/guide/browser-source",
	},
	"/guide/browser-source": {
		Number:       2,
		Title:        "Choose Browser Source",
		PreviousPath: "/",
		NextPath:     "/guide/source-name",
	},
	"/guide/source-name": {
		Number:       3,
		Title:        "Create Source",
		PreviousPath: "/guide/browser-source",
		NextPath:     "/guide/browser-properties",
	},
	"/guide/browser-properties": {
		Number:       4,
		Title:        "Configure the Browser Source",
		PreviousPath: "/guide/source-name",
		NextPath:     "/guide/verify",
	},
	"/guide/verify": {
		Number:       5,
		Title:        "Verify the Source",
		PreviousPath: "/guide/browser-properties",
	},
}

var audioGuideSteps = map[string]guideStep{
	"/guide/audio": {
		Number:   1,
		Title:    "Install the Audio Monitor Plugin",
		NextPath: "/guide/audio/filters",
	},
	"/guide/audio/filters": {
		Number:       2,
		Title:        "Open Source Filters",
		PreviousPath: "/guide/audio",
		NextPath:     "/guide/audio/add-filter",
	},
	"/guide/audio/add-filter": {
		Number:       3,
		Title:        "Add an Audio Filter",
		PreviousPath: "/guide/audio/filters",
		NextPath:     "/guide/audio/choose-monitor",
	},
	"/guide/audio/choose-monitor": {
		Number:       4,
		Title:        "Choose Audio Monitor",
		PreviousPath: "/guide/audio/add-filter",
		NextPath:     "/guide/audio/filter-name",
	},
	"/guide/audio/filter-name": {
		Number:       5,
		Title:        "Name the Filter",
		PreviousPath: "/guide/audio/choose-monitor",
		NextPath:     "/guide/audio/settings",
	},
	"/guide/audio/settings": {
		Number:       6,
		Title:        "Configure the Monitor",
		PreviousPath: "/guide/audio/filter-name",
	},
}

var bitsGuideSteps = map[string]guideStep{
	"/guide/bits": {
		Number:   1,
		Title:    "Open the Power-ups Dashboard",
		NextPath: "/guide/bits/create",
	},
	"/guide/bits/create": {
		Number:       2,
		Title:        "Create the Custom Power-up",
		PreviousPath: "/guide/bits",
		NextPath:     "/guide/bits/reward-id",
	},
	"/guide/bits/reward-id": {
		Number:       3,
		Title:        "Catch the Reward ID",
		PreviousPath: "/guide/bits/create",
		NextPath:     "/guide/bits/connect",
	},
	"/guide/bits/connect": {
		Number:       4,
		Title:        "Connect It to a Character",
		PreviousPath: "/guide/bits/reward-id",
	},
}

func (api *API) obsGuide(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	step, ok := obsGuideSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("obs_guide.html", &guidePage{
		Step: step,
		URL:  r.Host + "/" + user.TwitchLogin,
	})
}

func (api *API) audioGuide(r *http.Request) template.HTML {
	step, ok := audioGuideSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("audio_guide.html", &guidePage{Step: step})
}

type bitsGuidePage struct {
	Step         guideStep
	DashboardURL string
}

func (api *API) bitsGuide(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	step, ok := bitsGuideSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("bits_guide.html", &bitsGuidePage{
		Step:         step,
		DashboardURL: "https://dashboard.twitch.tv/u/" + user.TwitchLogin + "/viewer-rewards/channel-points/rewards",
	})
}

const bitsRewardDetectLimit = 10

type bitsRewardCandidate struct {
	RewardID   string `json:"reward_id"`
	Message    string `json:"message"`
	AgoSeconds int    `json:"ago_seconds"`
}

func (api *API) bitsGuideDetect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not authorized"})

		return
	}

	api.bitsDetector.Observe(user.ID, user.TwitchLogin)

	candidates, err := api.bitsRewardCandidates(r, user)
	if err != nil {
		api.logger.Error("failed to detect reward ids", "error", err, "user", user.TwitchLogin)

		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to look up reward ids"})

		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"rewards": candidates})
}

func (api *API) bitsRewardCandidates(r *http.Request, user *db.User) ([]bitsRewardCandidate, error) {
	stored, err := api.db.GetUnboundRewardIDs(r.Context(), user.ID, bitsRewardDetectLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to get unbound reward ids: %w", err)
	}

	newest := make(map[string]seenReward, len(stored))
	for _, reward := range api.bitsDetector.Seen(user.ID) {
		if existing, ok := newest[reward.RewardID]; ok && !existing.SeenAt.Before(reward.SeenAt) {
			continue
		}

		newest[reward.RewardID] = reward
	}
	for _, reward := range stored {
		if existing, ok := newest[reward.RewardID]; ok && !existing.SeenAt.Before(reward.SeenAt) {
			continue
		}

		newest[reward.RewardID] = seenReward{RewardID: reward.RewardID, Message: reward.Message, SeenAt: reward.SeenAt}
	}

	merged := make([]seenReward, 0, len(newest))
	for _, reward := range newest {
		merged = append(merged, reward)
	}

	slices.SortFunc(merged, func(a, b seenReward) int {
		return b.SeenAt.Compare(a.SeenAt)
	})

	now := time.Now()

	candidates := make([]bitsRewardCandidate, 0, bitsRewardDetectLimit)
	for _, reward := range merged {
		if len(candidates) >= bitsRewardDetectLimit {
			break
		}

		bound, err := api.db.IsRewardBound(r.Context(), reward.RewardID)
		if err != nil {
			return nil, fmt.Errorf("failed to check reward binding: %w", err)
		}
		if bound {
			continue
		}

		ago := int(now.Sub(reward.SeenAt).Seconds())
		if ago < 0 {
			ago = 0
		}

		candidates = append(candidates, bitsRewardCandidate{
			RewardID:   reward.RewardID,
			Message:    reward.Message,
			AgoSeconds: ago,
		})
	}

	return candidates, nil
}

func (api *API) legacyHome(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	return getHtml("legacy_home.html", &guidePage{URL: r.Host + "/" + user.TwitchLogin})
}
