package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"app/conns"
	"app/db"
	"app/slg"
	"app/tools"
)

type DefaultProcessor struct {
	cm *conns.Manager
}

func (cm *DefaultProcessor) eventsParser(user string) error {
	return nil
}

func (conn *DefaultProcessor) Process(ctx context.Context, user string) error {
	ctx, cancel := context.WithCancel(ctx)

	defer func() {
		if r := recover(); r != nil {
			slg.GetSlog(ctx).Error("connection panic", "user", user, "r", r, "stack", string(debug.Stack()))
		}
	}()

	signalCh := conn.cm.Signal(user)

	go func() {
		<-signalCh
		slg.GetSlog(ctx).Info("processor signal recieved")
		cancel()
	}()

	settings, err := db.GetDbSettings(user)
	if err != nil {
		slg.GetSlog(ctx).Info("settings not found, defaulting to Chat=true")
		settings = &db.Settings{
			Chat: true,
		}
	}

	slg.GetSlog(ctx).Info("Settings fetched", "settings", settings)

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

	slg.GetSlog(ctx).Info("starting processing")

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

			conn.cm.Write(user, dataEvent)
		}
	}

	slg.GetSlog(ctx).Info("processor is closing")

	return nil
}
