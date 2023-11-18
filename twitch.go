package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"app/db"
	"app/slg"
	"app/ws"

	"github.com/dchest/uniuri"
	"github.com/gorilla/websocket"
	"github.com/nicklaw5/helix/v2"
)

const twitchClientID = "zi6vy3y3iq38svpmlub5fd26uwsee8"

const getUsersUrl = "https://api.twitch.tv/helix/users"

type getUsersDataEntry struct {
	Id    string `json:"id"`
	Login string `json:"login"`
}

type getUsersResp struct {
	Data []getUsersDataEntry `json:"data"`
}

func getUsers(accessToken string) (*db.UserLoginData, error) {
	req, err := http.NewRequest(http.MethodGet, getUsersUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create getUsers request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Client-Id", twitchClientID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do getUsers request: %w", err)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read getUsers response body: %w", err)
	}

	respJson := &getUsersResp{}

	if err = json.Unmarshal(respData, &respJson); err != nil {
		return nil, fmt.Errorf("failed to unmarshal getUsers response body: %w", err)
	}

	if len(respJson.Data) == 0 {
		return nil, fmt.Errorf("no users found")
	}

	intId, err := strconv.Atoi(respJson.Data[0].Id)
	if err != nil {
		return nil, fmt.Errorf("failed to convert userId to int; %w", err)
	}

	return &db.UserLoginData{
		UserId:   intId,
		UserName: respJson.Data[0].Login,
	}, nil
}

type userTokenData struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

func codeHandler(code string) (*db.UserData, error) {
	data := url.Values{}
	data.Set("client_id", twitchClientID)
	data.Set("client_secret", twitchSecret)
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", "https://forsen.fun/twitch_token_handler")

	encodedData := data.Encode()

	resp, err := http.Post("https://id.twitch.tv/oauth2/token", "application/x-www-form-urlencoded", strings.NewReader(encodedData))
	if err != nil {
		respData, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("failed to send post request to token url: %w| body: %s", err, respData)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read the token response body: %w", err)
	}

	if resp.StatusCode > 299 {
		return nil, fmt.Errorf("status code (%d) of post request to token url is invalid: %s", resp.StatusCode, string(respData))
	}

	respJson := &userTokenData{}
	if err = json.Unmarshal(respData, &respJson); err != nil {
		return nil, fmt.Errorf("failed to unmarshal json: %w", err)
	}

	userData, err := getUsers(respJson.AccessToken)
	if err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(userData)
	}

	return &db.UserData{
		RefreshToken:  respJson.RefreshToken,
		AccessToken:   respJson.AccessToken,
		UserLoginData: userData,
	}, nil
}

func onUserData(userData *db.UserData) error {
	session := uniuri.New()

	userData.Session = session
	if err := db.UpsertUserData(userData); err != nil {
		return fmt.Errorf("failed to upsert user data: %w", err)
	}

	return nil
}

func twitchTokenHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		fmt.Println(r.URL.Query().Get("error"), r.URL.Query().Get("error_description"))
	} else if userData, err := codeHandler(code); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else if err := onUserData(userData); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else {
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: userData.Session})

		http.Redirect(w, r, "https://forsen.fun/settings", http.StatusSeeOther)
	}
}

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
