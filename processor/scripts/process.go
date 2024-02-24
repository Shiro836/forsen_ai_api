package scripts

import (
	"app/pkg/char"
	"context"
)

type Event struct {
	MsgID int

	UserName       string
	Message        string
	CustomRewardID string
}

type CallLayer interface {
	GetBroadcaster() string
	GetCharCard(ctx context.Context, charName string) (*char.Card, error)
	CallAI(ctx context.Context, charName string) (string, error)
	CallTtsText(ctx context.Context, charName string, text string) error
	GetNextEvent(ctx context.Context) (*Event, error)
	FilterText(text string) string
}

func Process(ctx context.Context, callLayer CallLayer) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		nextEvent, err := callLayer.GetNextEvent(ctx)
		if err != nil {
			return err
		}

		print(nextEvent)
	}
}
