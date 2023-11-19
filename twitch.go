package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"app/db"
	"app/slg"
	"app/ws"

	"github.com/gorilla/websocket"
	"github.com/nicklaw5/helix/v2"
)

const (
	eventTypeChat = iota
	eventTypeChannelpoint
	eventTypeFollow
	eventTypeSub
	eventTypeGift
	eventTypeRandom
	eventTypeInfo
	eventTypeRaid
	eventTypeUnknown
)

type twitchEvent struct {
	eventType int
	userName  string
	message   string
}

const eventSubUrl = "wss://eventsub.wss.twitch.tv/ws"

func twitchEventsSplitter(inputCh chan *twitchEvent) (subs chan *twitchEvent, follows chan *twitchEvent, raids chan *twitchEvent, channelPtsRedeems, unknown chan *twitchEvent) {
	subs, follows, raids, channelPtsRedeems, unknown = make(chan *twitchEvent),
		make(chan *twitchEvent), make(chan *twitchEvent), make(chan *twitchEvent), make(chan *twitchEvent)

	go func() {
		defer close(subs)
		defer close(follows)
		defer close(raids)
		defer close(channelPtsRedeems)
		defer close(unknown)

		for event := range inputCh {
			switch event.eventType {
			case eventTypeFollow:
				follows <- event
			case eventTypeGift, eventTypeSub:
				subs <- event
			case eventTypeChannelpoint:
				channelPtsRedeems <- event
			case eventTypeRaid:
				raids <- event
			default:
				unknown <- event
			}
		}
	}()

	return
}

func eventSubDataStreamBeta(ctx context.Context, settings *db.Settings, user string) (chan *twitchEvent, error) {
	userData, err := db.GetUserData(user)
	if err != nil {
		return nil, err
	}

	twitchClient, err := helix.NewClientWithContext(ctx, &helix.Options{
		ClientID:        twitchClientID,
		ClientSecret:    twitchSecret,
		UserAccessToken: userData.AccessToken,
		RefreshToken:    userData.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create twitch client: %w", err)
	}

	twitchClient.OnUserAccessTokenRefreshed(func(newAccessToken, newRefreshToken string) {
		userData.AccessToken = newAccessToken
		userData.RefreshToken = newRefreshToken

		if err := db.UpdateUserData(userData); err != nil {
			slg.GetSlog(ctx).Error("failed to update user data")
		}
	})

	c, _, err := websocket.DefaultDialer.DialContext(ctx, eventSubUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ws to twitch: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	wsClient, done := ws.NewWsClient(c)

	go func() {
		<-ctx.Done()
		wsClient.Close()
	}()

	go func() {
		<-done
		cancel()
	}()

	var keepaliveTimeout time.Duration
	var wsSessionId string

	if msg, err := wsClient.Read(); err != nil {
		return nil, fmt.Errorf("failed to read welcome message: %w", err)
	} else {
		resp := &struct {
			Payload struct {
				Session struct {
					ID string `json:"id"`

					KeepaliveTimeoutSeconds int `json:"keepalive_timeout_seconds"`
				} `json:"session"`
			} `json:"payload"`
		}{}

		if err = json.Unmarshal(msg.Message, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal welcome message: %w", err)
		}

		wsSessionId = resp.Payload.Session.ID
		keepaliveTimeout = time.Second * time.Duration(resp.Payload.Session.KeepaliveTimeoutSeconds)
	}

	if settings.Follows {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.follow",
			Version: "2",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
				ModeratorUserID:   strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create follow event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscribe",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create subscribe event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscription.message",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create subscribe message event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscription.gift",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create sub gift message event sub: %w", err)
		}
	}

	if settings.Raids {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.raid",
			Version: "1",
			Condition: helix.EventSubCondition{
				ToBroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create raid event sub: %w", err)
		}
	}

	if settings.ChannelPts {
		if rewardID, err := db.GetRewardID(user); err != nil {
			fmt.Println("failed to get reward id from db:", err)
		} else if len(rewardID) == 0 {
			fmt.Println("empty reward id")
		} else if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.channel_points_custom_reward_redemption.add",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
				RewardID:          rewardID,
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create raid event sub: %w", err)
		}
	}

	ch := make(chan *twitchEvent)

	slg.GetSlog(ctx).Info("twitch ws connected")

	lastMsgTime := time.Now()

	// onmessage
	go func() {
		defer close(ch)
		defer cancel()
		defer func() {
			slg.GetSlog(ctx).Info("close twitch ws")
			wsClient.Close()
		}()

	loop:
		for {
			msg, err := wsClient.Read()
			if err != nil {
				slg.GetSlog(ctx).Error("ws read error", "err", err)
				break
			}

			lastMsgTime = time.Now()

			meta := &struct {
				Metadata struct {
					MessageType string `json:"message_type"`
				} `json:"metadata"`
			}{}

			if err := json.Unmarshal(msg.Message, &meta); err != nil {
				slg.GetSlog(ctx).Error("meta unmarshal error", "err", err)
				break
			}

			switch meta.Metadata.MessageType {
			case "notification":
				payload := &struct {
					Payload struct {
						Subscription struct {
							Type string `json:"type"`
						} `json:"subscription"`
						Event struct {
							UserName  string  `json:"user_name"`
							UserInput *string `json:"user_input"` // channel point input
							Message   *struct {
								Text string `json:"text"`
							} `json:"message"` // resub msg
							CumulativeMonths *int `json:"cumulative_months"` // resub
							Viewers          *int `json:"viewers"`           // raiders
							Total            *int `json:"total"`             // gifted
							Reward           *struct {
								Prompt string `json:"prompt"`
							} `json:"reward"`
						} `json:"event"`
					} `json:"payload"`
				}{}

				if err := json.Unmarshal(msg.Message, &payload); err != nil {
					slg.GetSlog(ctx).Error("payload unmarshal error", "err", err)
					break loop
				}

				switch payload.Payload.Subscription.Type {
				case "channel.follow":
					ch <- &twitchEvent{
						eventType: eventTypeFollow,
						userName:  payload.Payload.Event.UserName,
						message:   payload.Payload.Event.UserName + " just followed this channel",
					}
				case "channel.subscribe":
					ch <- &twitchEvent{
						eventType: eventTypeSub,
						userName:  payload.Payload.Event.UserName,
						message:   payload.Payload.Event.UserName + " just subscribed to this channel",
					}
				case "channel.subscription.message":
					msg := ""
					if payload.Payload.Event.Message != nil {
						msg = payload.Payload.Event.Message.Text
					}

					ch <- &twitchEvent{
						eventType: eventTypeSub,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just resubbed with %d months and says - %s", payload.Payload.Event.UserName, *payload.Payload.Event.CumulativeMonths, msg),
					}
				case "channel.subscription.gift":
					ch <- &twitchEvent{
						eventType: eventTypeGift,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just gifted %d subs", payload.Payload.Event.UserName, *payload.Payload.Event.Total),
					}
				case "channel.raid":
					ch <- &twitchEvent{
						eventType: eventTypeRaid,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just raided with %d people", payload.Payload.Event.UserName, *payload.Payload.Event.Viewers),
					}
				case "channel.channel_points_custom_reward_redemption.add":
					ch <- &twitchEvent{
						eventType: eventTypeChannelpoint,
						userName:  payload.Payload.Event.UserName,
						message:   *payload.Payload.Event.UserInput,
					}
				}
			}
		}
	}()

	go func() {
		for {
			if time.Since(lastMsgTime) > keepaliveTimeout {
				slg.GetSlog(ctx).Info("keepalive timeout passed Aware")
				wsClient.Close()
				return
			}

			time.Sleep(1 * time.Second)
		}
	}()

	return ch, nil
}

func eventSubDataStream(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter, user string, settings *db.Settings) (chan *twitchEvent, error) {
	userData, err := db.GetUserData(user)
	if err != nil {
		return nil, err
	}

	if !isValidUser(userData.UserLoginData.UserName, w) {
		return nil, fmt.Errorf("forbidden user")
	}

	twitchClient, err := helix.NewClientWithContext(ctx, &helix.Options{
		ClientID:        twitchClientID,
		ClientSecret:    twitchSecret,
		UserAccessToken: userData.AccessToken,
		RefreshToken:    userData.RefreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create twitch client: %w", err)
	}

	twitchClient.OnUserAccessTokenRefreshed(func(newAccessToken, newRefreshToken string) {
		userData.AccessToken = newAccessToken
		userData.RefreshToken = newRefreshToken

		if err := db.UpdateUserData(userData); err != nil {
			fmt.Println(err)
		}
	})

	c, _, err := websocket.DefaultDialer.DialContext(ctx, eventSubUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial ws to twitch: %w", err)
	}

	wsClient, done := ws.NewWsClient(c)

	go func() {
		<-ctx.Done()
		wsClient.Close()
	}()

	go func() {
		<-done
		cancel()
	}()

	var keepaliveTimeout time.Duration
	var wsSessionId string

	if msg, err := wsClient.Read(); err != nil {
		return nil, fmt.Errorf("failed to read welcome message: %w", err)
	} else {
		resp := &struct {
			Payload struct {
				Session struct {
					ID string `json:"id"`

					KeepaliveTimeoutSeconds int `json:"keepalive_timeout_seconds"`
				} `json:"session"`
			} `json:"payload"`
		}{}

		if err = json.Unmarshal(msg.Message, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal welcome message: %w", err)
		}

		wsSessionId = resp.Payload.Session.ID
		keepaliveTimeout = time.Second * time.Duration(resp.Payload.Session.KeepaliveTimeoutSeconds)
	}

	if settings.Follows {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.follow",
			Version: "2",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
				ModeratorUserID:   strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create follow event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscribe",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create subscribe event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscription.message",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create subscribe message event sub: %w", err)
		}
	}

	if settings.Subs {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.subscription.gift",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create sub gift message event sub: %w", err)
		}
	}

	if settings.Raids {
		if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.raid",
			Version: "1",
			Condition: helix.EventSubCondition{
				ToBroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create raid event sub: %w", err)
		}
	}

	if settings.ChannelPts {
		if rewardID, err := db.GetRewardID(user); err != nil {
			fmt.Println("failed to get reward id from db:", err)
		} else if len(rewardID) == 0 {
			fmt.Println("empty reward id")
		} else if _, err := twitchClient.CreateEventSubSubscription(&helix.EventSubSubscription{
			Type:    "channel.channel_points_custom_reward_redemption.add",
			Version: "1",
			Condition: helix.EventSubCondition{
				BroadcasterUserID: strconv.Itoa(userData.UserLoginData.UserId),
				RewardID:          rewardID,
			},
			Transport: helix.EventSubTransport{
				Method:    "websocket",
				SessionID: wsSessionId,
			},
		}); err != nil {
			return nil, fmt.Errorf("failed to create raid event sub: %w", err)
		}
	}

	ch := make(chan *twitchEvent)

	fmt.Println("twitch ws connected")

	if keepaliveTimeout == 0 {
		fmt.Println(wsSessionId)
	}

	lastMsgTime := time.Now()

	// onmessage
	go func() {
		defer close(ch)
		defer cancel()
		defer func() {
			fmt.Println("close twitch ws")
			wsClient.Close()
		}()

	loop:
		for {
			msg, err := wsClient.Read()
			if err != nil {
				fmt.Println(err)
				break
			}

			lastMsgTime = time.Now()

			meta := &struct {
				Metadata struct {
					MessageType string `json:"message_type"`
				} `json:"metadata"`
			}{}

			if err := json.Unmarshal(msg.Message, &meta); err != nil {
				fmt.Println(err)
				break
			}

			switch meta.Metadata.MessageType {
			case "notification":
				payload := &struct {
					Payload struct {
						Subscription struct {
							Type string `json:"type"`
						} `json:"subscription"`
						Event struct {
							UserName  string  `json:"user_name"`
							UserInput *string `json:"user_input"` // channel point input
							Message   *struct {
								Text string `json:"text"`
							} `json:"message"` // resub msg
							CumulativeMonths *int `json:"cumulative_months"` // resub
							Viewers          *int `json:"viewers"`           // raiders
							Total            *int `json:"total"`             // gifted
							Reward           *struct {
								Prompt string `json:"prompt"`
							} `json:"reward"`
						} `json:"event"`
					} `json:"payload"`
				}{}

				if err := json.Unmarshal(msg.Message, &payload); err != nil {
					fmt.Println(err)
					break loop
				}

				switch payload.Payload.Subscription.Type {
				case "channel.follow":
					ch <- &twitchEvent{
						eventType: eventTypeFollow,
						userName:  payload.Payload.Event.UserName,
						message:   payload.Payload.Event.UserName + " just followed this channel",
					}
				case "channel.subscribe":
					ch <- &twitchEvent{
						eventType: eventTypeSub,
						userName:  payload.Payload.Event.UserName,
						message:   payload.Payload.Event.UserName + " just subscribed to this channel",
					}
				case "channel.subscription.message":
					msg := ""
					if payload.Payload.Event.Message != nil {
						msg = payload.Payload.Event.Message.Text
					}

					ch <- &twitchEvent{
						eventType: eventTypeSub,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just resubbed with %d months and says - %s", payload.Payload.Event.UserName, *payload.Payload.Event.CumulativeMonths, msg),
					}
				case "channel.subscription.gift":
					ch <- &twitchEvent{
						eventType: eventTypeGift,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just gifted %d subs", payload.Payload.Event.UserName, *payload.Payload.Event.Total),
					}
				case "channel.raid":
					ch <- &twitchEvent{
						eventType: eventTypeRaid,
						userName:  payload.Payload.Event.UserName,
						message:   fmt.Sprintf("%s just raided with %d people", payload.Payload.Event.UserName, *payload.Payload.Event.Viewers),
					}
				case "channel.channel_points_custom_reward_redemption.add":
					ch <- &twitchEvent{
						eventType: eventTypeChannelpoint,
						userName:  payload.Payload.Event.UserName,
						message:   *payload.Payload.Event.UserInput,
					}
				}
			}
		}
	}()

	go func() {
		for {
			if time.Since(lastMsgTime) > keepaliveTimeout {
				fmt.Println("keepalive timeout passed Aware")
				wsClient.Close()
				return
			}

			time.Sleep(5 * time.Second)
		}
	}()

	return ch, nil
}
