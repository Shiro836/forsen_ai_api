package main

import (
	"context"
	"encoding/json"
	"fmt"
)

func processTwitchEvent(ctx context.Context, twitchEvent *twitchEvent) (*DataEvent, error) {
	text := &Text{
		Text: "test text",
	}

	data, err := json.Marshal(text)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal text: %w", err)
	}

	return &DataEvent{
		EventType: EventTypeText,
		EventData: data,
	}, nil
}

// input channel must be drained in order not to leak goroutines
func processTwitchEvents(ctx context.Context, events chan *twitchEvent) chan *DataEvent {
	ch := make(chan *DataEvent)

	go func() {
		defer close(ch)

		for twitchEvent := range events {
			select {
			case <-ctx.Done():
				continue
			default:
			}

			dataEvent, err := processTwitchEvent(ctx, twitchEvent)
			if err != nil {
				GetSlog(ctx).Error("failed to process twitch event", "err", err, "twitch_event", twitchEvent)
			}

			select {
			case <-ctx.Done():
				continue
			default:
			}

			select {
			case ch <- dataEvent:
			case <-ctx.Done():
				continue
			}
		}
	}()

	return ch
}
