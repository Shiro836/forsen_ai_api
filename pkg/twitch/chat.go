package twitch

import (
	"context"
	"time"

	"github.com/gempir/go-twitch-irc/v4"
)

type ChatMessage struct {
	TwitchLogin string
	Message     string
	RewardID    string
}

func MessagesFetcher(ctx context.Context, twitchLogin string, skipNonRewards bool) chan *ChatMessage {
	ch := make(chan *ChatMessage, 1000)

	go func() {
		defer close(ch)

		client := twitch.NewAnonymousClient()

		client.OnPrivateMessage(func(message twitch.PrivateMessage) {
			select {
			case <-ctx.Done():
				_ = client.Disconnect()
				return
			default:
			}

			if skipNonRewards && len(message.CustomRewardID) == 0 {
				return
			}

			select {
			case ch <- &ChatMessage{
				TwitchLogin: message.User.Name,
				Message:     message.Message,
				RewardID:    message.CustomRewardID,
			}:
			default:
				// queue is full
			}
		})

		client.Join(twitchLogin)

		client.SendPings = true
		client.IdlePingInterval = 10 * time.Second

		_ = client.Connect()
	}()

	return ch
}
