package main

import (
	"context"
	"fmt"
	"time"

	"app/db"

	"golang.org/x/exp/slog"
)

func (cm *ConnectionManager) eventsParser(user string) error {
	return nil
}

func (cm *ConnectionManager) hasConsumers(user string) bool {
	cm.rwMutex.RLock()
	defer cm.rwMutex.RUnlock()

	return cm.subCount[user] > 0
}

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
		slog.Info("processor signal recieved")
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
	var subsCh, followsCh, raidsCh, channelPtsCh, twitchUnknownCh chan *twitchEvent

	if settings.Chat {
		twitchChatCh = messagesFetcher(ctx, user)
	}
	if settings.ChannelPts || settings.Follows || settings.Raids || settings.Subs {
		twitchApiCh, err := eventSubDataStreamBeta(ctx, settings, user)
		if err != nil {
			return fmt.Errorf("failed to setup twitch api data stream: %w", err)
		}

		subsCh, followsCh, raidsCh, channelPtsCh, twitchUnknownCh = twitchEventsSplitter(twitchApiCh)
	}
	if settings.Events {
		randEventsCh = randEvents(ctx, time.Second*time.Duration(settings.EventsInterval))
	}

	inChans := make([]chan *twitchEvent, 0, 7)
	add := func(ch chan *twitchEvent) {
		if ch != nil {
			inChans = append(inChans, ch)
		}
	}
	add(randEventsCh)
	add(twitchChatCh)
	add(twitchUnknownCh)
	add(channelPtsCh)
	add(followsCh)
	add(raidsCh)
	add(subsCh)

	closingChans := closingProxy(inChans...)

	var dataCh chan *twitchEvent

	if len(closingChans) == 0 {
		dataCh = make(chan *twitchEvent)
		go func() {
			defer close(dataCh)

			for {
				select {
				case dataCh <- &twitchEvent{
					eventType: eventTypeInfo,
					userName:  user,
					message:   "No settings enabled you sillE goose",
				}:
				case <-ctx.Done():
					break
				}
			}
		}()
	} else {
		dataCh = priorityFanIn(nil, closingChans...)
	}

	slog.Info("processor is closing")

	return nil
}
