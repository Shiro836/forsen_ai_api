package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"app/pkg/twitch"

	"github.com/Its-donkey/kappopher/helix"
)

type EventSubConfig struct {
	// Callback is the public https URL Twitch delivers to; empty turns EventSub off.
	Callback string `yaml:"callback"`
	Secret   string `yaml:"secret"`
}

const (
	eventSubSyncInterval   = 10 * time.Second
	eventSubRelistInterval = 10 * time.Minute
	eventSubRetryDelay     = 10 * time.Minute

	scopeBitsRead = "bits:read"
)

type subKey struct {
	eventType     string
	broadcasterID string
}

type eventSub struct {
	logger *slog.Logger
	cfg    EventSubConfig

	auth   *helix.AuthClient
	client *helix.Client

	conduitID string

	lock    sync.Mutex
	desired map[subKey]struct{}
	live    map[subKey]string
	retryAt map[subKey]time.Time

	notifications chan *helix.EventSubWebhookMessage
}

func newEventSub(logger *slog.Logger, cfg EventSubConfig, twitchCfg *twitch.Config) *eventSub {
	auth := helix.NewAuthClient(helix.AuthConfig{
		ClientID:     twitchCfg.ClientID,
		ClientSecret: twitchCfg.Secret,
	})

	return &eventSub{
		logger:        logger,
		cfg:           cfg,
		auth:          auth,
		client:        helix.NewClient(twitchCfg.ClientID, auth),
		desired:       make(map[subKey]struct{}),
		live:          make(map[subKey]string),
		retryAt:       make(map[subKey]time.Time),
		notifications: make(chan *helix.EventSubWebhookMessage, 1024),
	}
}

func desiredSubscriptions(twitchUserID int, scopes []string) []subKey {
	broadcasterID := strconv.Itoa(twitchUserID)

	keys := []subKey{{helix.EventSubTypeChannelPointsRedemptionAdd, broadcasterID}}
	if slices.Contains(scopes, scopeBitsRead) {
		keys = append(keys, subKey{helix.EventSubTypeChannelCustomPowerUpRedemptionAdd, broadcasterID})
	}
	return keys
}

func (e *eventSub) setDesired(desired map[subKey]struct{}) {
	e.lock.Lock()
	defer e.lock.Unlock()
	e.desired = desired
}

func (e *eventSub) handler() http.Handler {
	return helix.NewEventSubWebhookHandler(
		helix.WithWebhookSecret(e.cfg.Secret),
		// Twitch revokes subscriptions of slow responders, so the work happens off the request.
		helix.WithNotificationHandler(func(msg *helix.EventSubWebhookMessage) {
			select {
			case e.notifications <- msg:
			default:
				e.logger.Error("eventsub notification dropped, worker is behind", "type", msg.SubscriptionType, "message_id", msg.MessageID)
			}
		}),
		helix.WithRevocationHandler(func(msg *helix.EventSubWebhookMessage) {
			key := subKey{msg.Subscription.Type, msg.Subscription.Condition["broadcaster_user_id"]}
			e.logger.Warn("eventsub subscription revoked", "type", key.eventType, "broadcaster", key.broadcasterID, "status", msg.Subscription.Status)

			e.lock.Lock()
			defer e.lock.Unlock()
			delete(e.live, key)
			e.retryAt[key] = time.Now().Add(eventSubRetryDelay)
		}),
	)
}

func (e *eventSub) run(ctx context.Context, handle func(context.Context, *helix.EventSubWebhookMessage)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-e.notifications:
				metrics.EventSubNotifications.WithLabelValues(msg.SubscriptionType).Inc()
				handle(ctx, msg)
			}
		}
	}()

	syncTicker := time.NewTicker(eventSubSyncInterval)
	defer syncTicker.Stop()

	var relisted time.Time
	for {
		if time.Since(relisted) >= eventSubRelistInterval {
			if err := e.relist(ctx); err != nil {
				e.logger.Error("eventsub relist failed", "err", err)
			} else {
				relisted = time.Now()
			}
		}
		if !relisted.IsZero() {
			e.sync(ctx)
		}

		select {
		case <-ctx.Done():
			return
		case <-syncTicker.C:
		}
	}
}

// App tokens carry no refresh token; an expired one is simply requested again.
func (e *eventSub) call(ctx context.Context, fn func() error) error {
	if e.auth.GetToken() == nil {
		if _, err := e.auth.GetAppAccessToken(ctx); err != nil {
			return fmt.Errorf("get app token: %w", err)
		}
	}

	err := fn()

	var apiErr *helix.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		if _, err := e.auth.GetAppAccessToken(ctx); err != nil {
			return fmt.Errorf("renew app token: %w", err)
		}
		return fn()
	}
	return err
}

// The client id is shared between deployments, so a conduit is recognised as
// ours by the callback its shard delivers to.
func (e *eventSub) findConduit(ctx context.Context) (conduitID string, enabled bool, err error) {
	var conduits *helix.Response[helix.Conduit]
	if err := e.call(ctx, func() (err error) {
		conduits, err = e.client.GetConduits(ctx)
		return err
	}); err != nil {
		return "", false, fmt.Errorf("get conduits: %w", err)
	}

	for _, conduit := range conduits.Data {
		var shards *helix.GetConduitShardsResponse
		if err := e.call(ctx, func() (err error) {
			shards, err = e.client.GetConduitShards(ctx, &helix.GetConduitShardsParams{ConduitID: conduit.ID})
			return err
		}); err != nil {
			return "", false, fmt.Errorf("get shards of conduit %s: %w", conduit.ID, err)
		}

		for _, shard := range shards.Data {
			if shard.Transport.Callback == e.cfg.Callback {
				return conduit.ID, shard.Status == "enabled", nil
			}
		}
	}

	return "", false, nil
}

func (e *eventSub) ensureConduit(ctx context.Context) error {
	conduitID, enabled, err := e.findConduit(ctx)
	if err != nil {
		return err
	}

	if conduitID == "" {
		var conduit *helix.Conduit
		if err := e.call(ctx, func() (err error) {
			conduit, err = e.client.CreateConduit(ctx, 1)
			return err
		}); err != nil {
			return fmt.Errorf("create conduit: %w", err)
		}
		conduitID = conduit.ID
		e.logger.Info("eventsub conduit created", "conduit", conduitID)
	}
	e.conduitID = conduitID

	if enabled {
		return nil
	}

	var updated *helix.UpdateConduitShardsResponse
	if err := e.call(ctx, func() (err error) {
		updated, err = e.client.UpdateConduitShards(ctx, &helix.UpdateConduitShardsParams{
			ConduitID: conduitID,
			Shards: []helix.UpdateConduitShardParams{{
				ID:        "0",
				Transport: helix.UpdateConduitShardTransport{Method: "webhook", Callback: e.cfg.Callback, Secret: e.cfg.Secret},
			}},
		})
		return err
	}); err != nil {
		return fmt.Errorf("update conduit shard: %w", err)
	}
	if len(updated.Errors) > 0 {
		return fmt.Errorf("update conduit shard: %s (%s)", updated.Errors[0].Message, updated.Errors[0].Code)
	}

	e.logger.Info("eventsub shard pointed at webhook", "conduit", conduitID, "callback", e.cfg.Callback)
	return nil
}

func (e *eventSub) relist(ctx context.Context) error {
	if err := e.ensureConduit(ctx); err != nil {
		return err
	}

	var subs []helix.EventSubSubscription
	if err := e.call(ctx, func() (err error) {
		subs, err = e.client.GetAllSubscriptions(ctx, &helix.GetEventSubSubscriptionsParams{ConduitID: e.conduitID})
		return err
	}); err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}

	live := make(map[subKey]string, len(subs))
	for _, sub := range subs {
		if sub.Transport.ConduitID != e.conduitID {
			continue
		}

		key := subKey{sub.Type, sub.Condition["broadcaster_user_id"]}
		if _, duplicate := live[key]; sub.Status != "enabled" || duplicate {
			e.logger.Warn("eventsub removing subscription", "type", sub.Type, "broadcaster", key.broadcasterID, "status", sub.Status)
			e.unsubscribe(ctx, sub.ID)
			continue
		}
		live[key] = sub.ID
	}

	e.lock.Lock()
	e.live = live
	e.lock.Unlock()

	return nil
}

func (e *eventSub) unsubscribe(ctx context.Context, subscriptionID string) {
	if err := e.call(ctx, func() error {
		return e.client.DeleteEventSubSubscription(ctx, subscriptionID)
	}); err != nil {
		e.logger.Error("eventsub unsubscribe failed", "subscription", subscriptionID, "err", err)
	}
}

func (e *eventSub) sync(ctx context.Context) {
	now := time.Now()

	e.lock.Lock()
	var missing []subKey
	for key := range e.desired {
		if _, ok := e.live[key]; !ok && now.After(e.retryAt[key]) {
			missing = append(missing, key)
		}
	}
	orphaned := make(map[subKey]string)
	for key, id := range e.live {
		if _, ok := e.desired[key]; !ok {
			orphaned[key] = id
		}
	}
	e.lock.Unlock()

	for _, key := range missing {
		var sub *helix.EventSubSubscription
		err := e.call(ctx, func() (err error) {
			sub, err = e.client.CreateEventSubSubscription(ctx, &helix.CreateEventSubSubscriptionParams{
				Type:      key.eventType,
				Version:   helix.GetEventSubVersion(key.eventType),
				Condition: map[string]string{"broadcaster_user_id": key.broadcasterID},
				Transport: helix.CreateEventSubTransport{Method: "conduit", ConduitID: e.conduitID},
			})
			return err
		})

		e.lock.Lock()
		if err != nil {
			metrics.EventSubSubscribeFailures.WithLabelValues(key.eventType).Inc()
			e.logger.Error("eventsub subscribe failed", "type", key.eventType, "broadcaster", key.broadcasterID, "err", err)
			e.retryAt[key] = now.Add(eventSubRetryDelay)
		} else {
			e.logger.Info("eventsub subscribed", "type", key.eventType, "broadcaster", key.broadcasterID, "status", sub.Status)
			e.live[key] = sub.ID
		}
		e.lock.Unlock()
	}

	for key, id := range orphaned {
		e.logger.Info("eventsub unsubscribing", "type", key.eventType, "broadcaster", key.broadcasterID)
		e.unsubscribe(ctx, id)

		e.lock.Lock()
		delete(e.live, key)
		e.lock.Unlock()
	}

	e.lock.Lock()
	metrics.EventSubSubscriptions.Set(float64(len(e.live)))
	e.lock.Unlock()
}
