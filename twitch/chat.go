package twitch

import (
	"context"
	"time"

	"github.com/gempir/go-twitch-irc/v4"
)

type ChatMessage struct {
	UserName       string
	Message        string
	CustomRewardID string
}

func MessagesFetcher(ctx context.Context, user string) chan *ChatMessage {
	ch := make(chan *ChatMessage, 200)

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

			select {
			case ch <- &ChatMessage{
				UserName:       message.User.DisplayName,
				Message:        message.Message,
				CustomRewardID: message.CustomRewardID,
			}:
			default:
				// queue is full
			}
		})

		client.Join(user)

		client.SendPings = true
		client.IdlePingInterval = 10 * time.Second

		_ = client.Connect()
	}()

	return ch
}
