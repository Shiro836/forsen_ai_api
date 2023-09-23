package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gempir/go-twitch-irc/v4"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"
	"github.com/microcosm-cc/bluemonday"
	"github.com/nicklaw5/helix/v2"
	"golang.org/x/exp/slices"
)

var p = bluemonday.StrictPolicy()

const (
	ttsUrl = "http://localhost:4111/tts"
	aiUrl  = "http://localhost:8000/generate"
)

func writeFile(fileName string, data []byte) error {
	file, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	_, err = file.Write(data)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}

	return nil
}

var swearFilter *SwearFilter = NewSwearFilter(false, swears...)

//go:embed filtered_obiwan.wav
var filteredObiwanWav []byte

//go:embed filtered_forsen.wav
var filteredForsenWav []byte

const aiContext = "Forsen tries very hard to answer questions very thoroughly. Forsen loves lolis, he thinks they are very cute and funny. Forsen still tries to beat xqc's minecraft record for 137 days. Forsen's fursona is lion."

func onAsk(ctx context.Context, dataCh chan *wsMessage, channelOwner string, event *twitchEvent) error {
	msg := event.message
	if len(msg) == 0 {
		return fmt.Errorf("empty input message")
	}

	if triggered, err := swearFilter.Check(msg); err != nil {
		return fmt.Errorf("failed to check for bad words: %w", err)
	} else if len(triggered) > 0 {
		select {
		case dataCh <- &wsMessage{msgType: websocket.TextMessage, message: []byte(fmt.Sprintf("@%s: filtered", event.userName))}:
		case <-ctx.Done():
			fmt.Println("ret1")
			return nil
		}

		select {
		case dataCh <- &wsMessage{msgType: websocket.BinaryMessage, message: filteredObiwanWav}:
		case <-ctx.Done():
			fmt.Println("ret2")
			return nil
		}

		return nil
	}

	var resp string
	var err error

	attempts := 0

	for {
		switch event.eventType {
		case eventTypeRandom:
			resp, err = reqAI(ctx, "Forsen is a very good story teller.", "Forsen, tell me a story about what you did. It must be interesting and not short.", msg)
		case eventTypeFollow, eventTypeSub, eventTypeGift:
			resp, err = reqAI(ctx, "Forsen likes to thank his followers and subscribers.", msg, "Thank you ")
		default:
			resp, err = reqAI(ctx, aiContext, msg, "")
		}
		if err != nil {
			return err
		}

		respLimit := 20
		if event.eventType == eventTypeRandom {
			respLimit = 100
		}

		if len(resp) > respLimit {
			break
		}

		if attempts > 20 {
			return fmt.Errorf("too much attempts to get good answer")
		}
	}

	fmt.Println("AI response: ", string(resp), len(resp))

	if triggered, err := swearFilter.Check(resp); err != nil {
		return fmt.Errorf("failed to check for bad words: %w", err)
	} else if len(triggered) > 0 {
		resp = "filtered"
	}

	aiRespWav, err := filteredForsenWav, nil
	if resp != "filtered" {
		if event.eventType == eventTypeRandom {
			aiRespWav, err = reqTTS(ctx, msg+resp, "forsen")
		} else {
			aiRespWav, err = reqTTS(ctx, resp, "forsen")
		}
		if err != nil {
			return err
		}
	}

	if event.eventType == eventTypeRandom {
		select {
		case dataCh <- &wsMessage{msgType: websocket.TextMessage, message: []byte(fmt.Sprintf("FORSEN: %s%s", msg, resp))}:
		case <-ctx.Done():
			fmt.Println("ret3")
			return nil
		}

		select {
		case dataCh <- &wsMessage{msgType: websocket.BinaryMessage, message: aiRespWav}:
		case <-ctx.Done():
			fmt.Println("ret4")
			return nil
		}

		return nil
	}

	wavFile, err := reqTTS(ctx, msg, "obiwan")
	if err != nil {
		return err
	}

	sanitizedMsg := p.Sanitize(msg)

	select {
	case dataCh <- &wsMessage{msgType: websocket.TextMessage, message: []byte(fmt.Sprintf("@%s: %s", event.userName, sanitizedMsg))}:
	case <-ctx.Done():
		fmt.Println("ret1")
		return nil
	}

	select {
	case dataCh <- &wsMessage{msgType: websocket.BinaryMessage, message: wavFile}:
	case <-ctx.Done():
		fmt.Println("ret2")
		return nil
	}

	select {
	case dataCh <- &wsMessage{msgType: websocket.TextMessage, message: []byte(fmt.Sprintf("@%s: %s<br>FORSEN: %s", event.userName, sanitizedMsg, resp))}:
	case <-ctx.Done():
		fmt.Println("ret3")
		return nil
	}

	select {
	case dataCh <- &wsMessage{msgType: websocket.BinaryMessage, message: aiRespWav}:
	case <-ctx.Done():
		fmt.Println("ret4")
		return nil
	}

	return nil
}

func messagesFetcher(ctx context.Context, user string) chan *twitchEvent {
	ch := make(chan *twitchEvent, 50)

	go func() {
		defer close(ch)

		client := twitch.NewAnonymousClient()

		client.OnPrivateMessage(func(message twitch.PrivateMessage) {
			select {
			case <-ctx.Done():
				client.Disconnect()

				return
			default:
			}

			if len(message.Message) <= 4 || message.Message[:5] != "!ask " {
				fmt.Println("skipped: ", message.Message)
				return
			}

			fmt.Println("adding to channel")
			select {
			case ch <- &twitchEvent{
				eventType: eventTypeChat,
				userName:  message.User.DisplayName,
				message:   message.Message[5:],
			}:
			default:
				fmt.Println("queue is full for", user)
			}
		})

		client.OnConnect(func() {
			fmt.Println("connected for", user)
		})

		client.Join(user)

		client.SendPings = true
		client.IdlePingInterval = 10 * time.Second

		err := client.Connect()
		if err != nil {
			fmt.Println("connect err:", err)
		}
	}()

	return ch
}

func processMessages(ctx context.Context, user string, inputCh chan *twitchEvent) chan *wsMessage {
	ch := make(chan *wsMessage)

	go func() {
		defer close(ch)
		defer func() {
			for range inputCh {
			}
		}()

		for {
			select {
			case event, ok := <-inputCh:
				if !ok {
					return
				}

				fmt.Println("querying AI: ", event.message)
				if err := onAsk(ctx, ch, user, event); err != nil {
					fmt.Println(err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

func jsFileHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/webrtc.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read webrtc.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func jsWhitelistHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/whitelist.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read whitelist.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func isValidUser(user string, w http.ResponseWriter) bool {
	if whitelist, err := getDbWhitelist(); err != nil {
		fmt.Println(err)
		return false
	} else if !slices.ContainsFunc(whitelist.List, func(h *human) bool {
		return strings.EqualFold(h.Login, user) && h.BannedBy == nil
	}) {
		w.WriteHeader(http.StatusForbidden)
		data, _ := os.ReadFile("client/whitelist.html")
		w.Write(data)
		return false
	} else {
		return true
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	data, err := os.ReadFile("client/index.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read index.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func descriptionHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/description.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read description.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func webSocketHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("failed to upgrade ws:", err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to upgrade ws"))

		return
	}

	wsClient, done := newWsClient(c)

	defer func() {
		fmt.Println("close ws")
		wsClient.close()
	}()

	fmt.Println("ws connected")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-ctx.Done()
		wsClient.close()
	}()

	go func() {
		<-done
		cancel()
	}()

	// send heartbeats
	go func() {
		for {
			if err := wsClient.send(&wsMessage{
				msgType: websocket.TextMessage,
				message: []byte("heartbeat"),
			}); err != nil {
				fmt.Println("stopping heartbeats")

				return
			}

			time.Sleep(10 * time.Second)
		}
	}()

	finishCh := make(chan struct{})

	// onmessage handler
	go func() {
		for {
			msg, err := wsClient.read()
			if err != nil {
				close(finishCh)

				return
			}

			if msg.msgType != websocket.TextMessage {
				panic("not text not supported")
			}

			switch string(msg.message) {
			case "heartbeat":
			case "finish":
				finishCh <- struct{}{}
			default:
				panic("unexpected message")
			}
		}
	}()

	chatEnabled := true

	var randomEvents chan *twitchEvent = nil

	randEventsFunc := func(ctx context.Context, interval time.Duration) chan *twitchEvent {
		randomEvents = make(chan *twitchEvent)

		go func() {
			defer close(randomEvents)

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					randomEvents <- &twitchEvent{
						eventType: eventTypeRandom,
						userName:  "random",
						message:   storyStarters[rand.Int()%len(storyStarters)],
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		return randomEvents
	}

	var eventsStream chan *twitchEvent = nil
	if sessionId, err := r.Cookie("session_id"); err == nil && len(sessionId.Value) != 0 {
		settings, err := getDbSettings(sessionId.Value)
		if err != nil {
			fmt.Println(fmt.Println("failed to read settings:", err))
			return
		}

		chatEnabled = settings.Chat

		if settings.ChannelPts || settings.Follows || settings.Subs || settings.Raids {
			eventsStream, err = eventSubDataStream(ctx, cancel, w, sessionId.Value, settings)
			if err != nil {
				fmt.Println(err)
				return
			}
		}

		if settings.Events && settings.EventsInterval >= 10 {
			randomEvents = randEventsFunc(ctx, time.Second*time.Duration(settings.EventsInterval))
		}
	}

	var chatMsgs chan *twitchEvent = nil
	if chatEnabled {
		chatMsgs = messagesFetcher(ctx, user)
	}

	if chatMsgs == nil && eventsStream == nil && randomEvents == nil {
		fmt.Println("everything is disabled LULE")

		return
	}

	eventsStream = priorityFanIn(chatMsgs, eventsStream, randomEvents)

	dataStream := processMessages(ctx, user, eventsStream)
	defer func() {
		for range dataStream {
		}
	}()

	c.SetCloseHandler(func(code int, text string) error {
		fmt.Println("close recieved:", code, text)

		cancel()

		return nil
	})

	for {
		if msg, ok := <-dataStream; !ok {
			fmt.Println("not ok")
			return
		} else if err := wsClient.send(msg); err != nil {
			fmt.Println("send err")
			return
		} else if msg, ok := <-dataStream; !ok {
			fmt.Println("not ok")
			return
		} else if err := wsClient.send(msg); err != nil {
			fmt.Println("send err")
			return
		} else if _, ok := <-finishCh; !ok {
			fmt.Println("finish ch close")
			return
		}
	}
}

func GenFiltered(voice string) error {
	data, err := reqTTS(context.Background(), "filtered", voice)
	if err != nil {
		return err
	}
	err = writeFile("filtered_"+voice+".wav", data)
	if err != nil {
		return err
	}

	return nil
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err == nil && len(cookie.Value) != 0 {
		data, err := os.ReadFile("client/settings.html")
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte("failed to read settings.html"))
		}

		w.Header().Add("Content-Type", "text/html")
		w.Write(data)

		return
	}

	data, err := os.ReadFile("client/settings_login.html")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read settings.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

var httpClient = &http.Client{
	Timeout: 5 * time.Second,
}

func channelPointsRewardCreateHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("add")

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if userData, err := getUserDataBySessionId(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if !isValidUser(userData.UserLoginData.UserName, w) {
		return
	} else if twitchClient, err := helix.NewClientWithContext(r.Context(), &helix.Options{
		ClientID:        twitchClientID,
		ClientSecret:    twitchSecret,
		UserAccessToken: userData.AccessToken,
		RefreshToken:    userData.RefreshToken,
	}); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if resp, err := twitchClient.CreateCustomReward(&helix.ChannelCustomRewardsParams{
		BroadcasterID: strconv.Itoa(userData.UserLoginData.UserId),

		Title:               "Forsen AI",
		Cost:                1,
		Prompt:              "No !ask needed. Forsen will react to this message",
		IsEnabled:           true,
		BackgroundColor:     "#00FF00",
		IsUserInputRequired: true,
	}); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if len(resp.Data.ChannelCustomRewards) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("No new rewards created"))
	} else if err = saveRewardID(resp.Data.ChannelCustomRewards[0].ID, userData.Session); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}
}

func getSettings(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if settings, err := getDbSettings(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get settings"))
	} else if data, err := json.Marshal(settings); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to marshal settings"))
	} else {
		w.Write(data)
	}
}

func updateSettings(w http.ResponseWriter, r *http.Request) {
	var settings *Settings

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if data, err := io.ReadAll(r.Body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to read request body"))
	} else if err = json.Unmarshal(data, &settings); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to unmarshal request json body"))
	} else if err = updateDbSettings(settings, cookie.Value); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to update settings in db"))
	} else {
		w.Write([]byte("success"))
	}
}

func main() {
	defer db.Close()

	// if err := GenFiltered("obiwan"); err != nil {
	// 	log.Fatal(err)
	// }

	// os.Exit(0)

	router := chi.NewRouter()

	router.Use(middleware.Recoverer)
	router.Use(middleware.StripSlashes)
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	router.Get("/webrtc.js", jsFileHandler)
	router.Get("/whitelist.js", jsWhitelistHandler)

	router.Get("/", descriptionHandler)
	router.Get("/{user}", homeHandler)
	router.Get("/ws/{user}", webSocketHandler)

	router.Get("/settings", settingsHandler)
	router.Get("/add_channel_point_reward", channelPointsRewardCreateHandler)

	router.Get("/twitch_token_handler", twitchTokenHandler)

	router.Get("/get_settings", getSettings)
	router.Post("/update_settings", updateSettings)

	router.Get("/get_whitelist", getWhitelist)
	router.Post("/update_whitelist", updateWhitelist)

	fmt.Println("starting server")

	if err := http.ListenAndServe(":8080", router); err != nil {
		log.Fatal(err)
	}
}
