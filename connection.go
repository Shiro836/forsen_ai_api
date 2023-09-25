package main

import (
	"context"
	"fmt"
	"time"

	"app/db"

	"golang.org/x/exp/slog"
)

func (cm *ConnectionManager) processor(user string) error {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("connection panic", "user", user, "r", r)
		}
	}()

	ctx, cancel := context.WithCancel(cm.ctx)

	slog := slog.With("user", user)

	var signalCh chan struct{}
	func() {
		cm.rwMutex.RLock()
		defer cm.rwMutex.RUnlock()

		signalCh = cm.updateEventsCh[user]
	}()

	go func() {
		<-signalCh
		cancel()
	}()

	settings, err := db.GetDbSettings(user)
	if err != nil {
		slog.Info("settings not found, defaulting to Chat=true")
		settings = &db.Settings{
			Chat: true,
		}
	}

	var twitchChatCh, randEventsCh chan *twitchEvent
	var subs, follows, raids, channelPts, twitchUnknown chan *twitchEvent

	chansAreEmpty := true

	if settings.Chat {
		chansAreEmpty = false
		twitchChatCh = messagesFetcher(ctx, user)
	}
	if settings.ChannelPts || settings.Follows || settings.Raids || settings.Subs {
		chansAreEmpty = false
		twitchApiCh, err := eventSubDataStreamBeta(ctx, settings)
		if err != nil {
			return fmt.Errorf("failed to setup twitch api data stream: %w", err)
		}

		subs, follows, raids, channelPts, twitchUnknown = twitchEventsSplitter(twitchApiCh)
	}
	if settings.Events {
		chansAreEmpty = false
		randEventsCh = randEvents(ctx, time.Second*time.Duration(settings.EventsInterval))
	}

	var dataCh chan *twitchEvent

	if chansAreEmpty {
		dataCh = make(chan *twitchEvent)
		go func() {
			defer close(dataCh)

			for {
				select {
				case dataCh <- &twitchEvent{
					eventType: eventTypeInfo,
					userName:  user,
					message:   "No settings enabled you silly goose",
				}:
				case <-ctx.Done():
					break
				}
			}
		}()
	} else {
		dataCh = priorityFanIn(randEventsCh, twitchChatCh, twitchUnknown, channelPts, follows, raids, subs)
	}

	return nil
}
