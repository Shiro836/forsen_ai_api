package processor

import (
	"app/db"
	"app/internal/app/processor/scripts"
	"app/pkg/ai"
	"app/pkg/swearfilter"
	"app/pkg/tools"
	"context"
	"slices"
	"strings"
)

var _ scripts.AppAPI = &appApiImpl{}

type appApiImpl struct {
	broadcaster *db.User

	llm      *ai.VLLMClient
	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	rvc      *ai.RVCClient
	whisper  *ai.WhisperClient

	db DB
}

func (api *appApiImpl) GetBroadcasterLogin() string {
	return api.broadcaster.TwitchLogin
}

func (api *appApiImpl) GetCharCard(ctx context.Context, twitchRewardID string) (*db.CardData, error) {
	card, err := api.db.GetCharCardByTwitchReward(ctx, api.broadcaster.ID, twitchRewardID)
	if err != nil {
		return nil, err
	}

	return card.Data, nil
}

func (api *appApiImpl) CallAI(ctx context.Context, prompt string) (string, error) {
	return api.llm.Ask(ctx, prompt)
}

func (api *appApiImpl) CallTtsText(ctx context.Context, charName string, text string) error {
	panic("not implemented")
}

func (api *appApiImpl) GetNextMsg(ctx context.Context) (*db.Message, error) {
	return api.db.GetNextMsg(ctx, api.broadcaster.ID)
}

func (api *appApiImpl) FilterText(ctx context.Context, text string) string {
	var swears []string

	filters, err := api.db.GetFilters(ctx, api.broadcaster.ID)
	if err == nil {
		slices.Concat(swears, strings.Split(filters, ","))
	}

	slices.Concat(swears, swearfilter.Swears)

	swearFilterObj := swearfilter.NewSwearFilter(false, swears...)

	filtered := text

	tripped, _ := swearFilterObj.Check(text)
	for _, word := range tripped {
		filtered = tools.IReplace(filtered, word, strings.Repeat("*", len(word)))
	}

	return filtered
}

func (api *appApiImpl) GetSetting(ctx context.Context, settingName string) (string, error) {
	panic("not implemented")
}
