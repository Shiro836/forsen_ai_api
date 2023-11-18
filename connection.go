package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"app/db"
	"app/tools"
)

func (cm *ConnectionManager) eventsParser(user string) error {
	return nil
}

func (cm *ConnectionManager) hasConsumers(user string) bool {
	cm.rwMutex.RLock()
	defer cm.rwMutex.RUnlock()

	return cm.subCount[user] > 0
}

func (cm *ConnectionManager) processor(ctx context.Context, user string) error {
	ctx, cancel := context.WithCancel(ctx)

	defer func() {
		if r := recover(); r != nil {
			GetSlog(ctx).Error("connection panic", "user", user, "r", r, "stack", string(debug.Stack()))
		}
	}()

	var signalCh chan struct{}
	func() {
		cm.rwMutex.RLock()
		defer cm.rwMutex.RUnlock()

		signalCh = cm.updateEventsCh[user]
	}()

	go func() {
		<-signalCh
		GetSlog(ctx).Info("processor signal recieved")
		cancel()
	}()

	settings, err := db.GetDbSettings(user)
	if err != nil {
		GetSlog(ctx).Info("settings not found, defaulting to Chat=true")
		settings = &db.Settings{
			Chat: true,
		}
	}

	GetSlog(ctx).Info("Settings fetched", "settings", settings)

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

	closingChans := tools.CloseAndDrainOnAnyClose(inChans...)

	var twitchEventsCh chan *twitchEvent

	if len(closingChans) == 0 {
		twitchEventsCh = make(chan *twitchEvent)
		go func() {
			defer close(twitchEventsCh)

			for {
				select {
				case twitchEventsCh <- &twitchEvent{
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
		twitchEventsCh = tools.PriorityFanIn(nil, closingChans...)
	}

	dataCh := processTwitchEvents(ctx, twitchEventsCh)
	defer func() {
		for range dataCh {
		}
	}()

	GetSlog(ctx).Info("starting processing")

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		default:
		}

		for dataEvent := range dataCh {
			select {
			case <-ctx.Done():
				break loop
			default:
			}

			if err := cm.Write(user, dataEvent); err != nil {
				GetSlog(ctx).Error("failed to send event", "err", err, "dataEvent", dataEvent)
			}
		}
	}

	GetSlog(ctx).Info("processor is closing")

	return nil
}
