package scripts

import (
	"app/db"
	"context"
)

type AppAPI interface {
	GetBroadcasterLogin() string
	GetCharCard(ctx context.Context, twitchRewardID string) (*db.CardData, error)
	CallAI(ctx context.Context, charName string) (string, error)
	CallTtsText(ctx context.Context, charName string, text string) error
	GetNextMsg(ctx context.Context) (*db.Message, error)
	FilterText(ctx context.Context, text string) string
	GetSetting(ctx context.Context, settingName string) (string, error)
}

func Process(ctx context.Context, callLayer AppAPI) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		nextEvent, err := callLayer.GetNextMsg(ctx)
		if err != nil {
			return err
		}

		print(nextEvent)
	}
}
