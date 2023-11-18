package main

import (
	"app/conns"
	"app/db"
	"app/memory"
	"app/slg"
	"app/tools"
	"app/ws"

	"context"
	_ "embed"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gempir/go-twitch-irc/v4"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/microcosm-cc/bluemonday"
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

var memLimitChars = 600

func onAsk(ctx context.Context, dataCh chan *ws.Message, settings *db.Settings, event *twitchEvent, userMemory *memory.Memory) error {
	msg := event.message
	if len(msg) == 0 {
		return fmt.Errorf("empty input message")
	}

	if triggered, err := swearFilter.Check(msg); err != nil {
		return fmt.Errorf("failed to check for bad words: %w", err)
	} else if len(triggered) > 0 {
		select {
		case dataCh <- &ws.Message{MsgType: websocket.TextMessage, Message: []byte(fmt.Sprintf("@%s: filtered", event.userName))}:
		case <-ctx.Done():
			fmt.Println("ret1")
			return nil
		}

		select {
		case dataCh <- &ws.Message{MsgType: websocket.BinaryMessage, Message: filteredObiwanWav}:
		case <-ctx.Done():
			fmt.Println("ret2")
			return nil
		}

		return nil
	}

	var resp string
	var err error

	respLimit := 20
	if event.eventType == eventTypeRandom {
		respLimit = 100
	}

	for attempts := 0; attempts < 20; attempts++ {
		switch event.eventType {
		case eventTypeRandom:
			resp, err = ReqAI(ctx, "Forsen only tells the truth but tries not to get banned", "", "Forsen, what is your opinion on this?", msg)
		case eventTypeFollow, eventTypeSub, eventTypeGift:
			resp, err = ReqAI(ctx, "Forsen likes to thank his followers and subscribers.", "", msg, "Thank you ")
		default:
			memoryEntries := userMemory.GetCombinedMem(event.userName)
			resultMem := ""
			startInd := len(memoryEntries)

			fullLen := 0

			for i := len(memoryEntries) - 1; i >= 0; i-- {
				if fullLen+len(memoryEntries[i].Msg) < memLimitChars {
					startInd = i
					fullLen += len(memoryEntries[i].Msg)
				} else {
					break
				}
			}

			for i := startInd; i < len(memoryEntries); i++ {
				resultMem += " " + memoryEntries[i].Msg + " "
			}

			resp, err = ReqAI(ctx, settings.CustomContext, resultMem, msg, "")
		}
		if err != nil {
			return err
		}

		if len(resp) > respLimit {
			break
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
		aiRespWav, err = reqTTS(ctx, resp, "forsen")
		if err != nil {
			return err
		}
	}

	if event.eventType == eventTypeRandom {
		select {
		case dataCh <- &ws.Message{MsgType: websocket.TextMessage, Message: []byte(fmt.Sprintf("FORSEN: %s%s", msg, resp))}:
		case <-ctx.Done():
			fmt.Println("ret3")
			return nil
		}

		select {
		case dataCh <- &ws.Message{MsgType: websocket.BinaryMessage, Message: aiRespWav}:
		case <-ctx.Done():
			fmt.Println("ret4")
			return nil
		}

		return nil
	}

	if event.eventType == eventTypeChat {
		userMemory.Push(event.userName, "###PROMPT: "+msg+" ###FORSEN: "+resp)
	}

	wavFile, err := reqTTS(ctx, msg, "obiwan")
	if err != nil {
		return err
	}

	sanitizedMsg := p.Sanitize(msg)

	select {
	case dataCh <- &ws.Message{MsgType: websocket.TextMessage, Message: []byte(fmt.Sprintf("@%s: %s", event.userName, sanitizedMsg))}:
	case <-ctx.Done():
		fmt.Println("ret1")
		return nil
	}

	select {
	case dataCh <- &ws.Message{MsgType: websocket.BinaryMessage, Message: wavFile}:
	case <-ctx.Done():
		fmt.Println("ret2")
		return nil
	}

	select {
	case dataCh <- &ws.Message{MsgType: websocket.TextMessage, Message: []byte(fmt.Sprintf("@%s: %s<br>FORSEN: %s", event.userName, sanitizedMsg, resp))}:
	case <-ctx.Done():
		fmt.Println("ret3")
		return nil
	}

	select {
	case dataCh <- &ws.Message{MsgType: websocket.BinaryMessage, Message: aiRespWav}:
	case <-ctx.Done():
		fmt.Println("ret4")
		return nil
	}

	return nil
}

func messagesFetcher(ctx context.Context, user string) chan *twitchEvent {
	ch := make(chan *twitchEvent, 3)

	slog := slog.With("user", user)

	go func() {
		defer close(ch)

		client := twitch.NewAnonymousClient()

		client.OnPrivateMessage(func(message twitch.PrivateMessage) {
			select {
			case <-ctx.Done():
				if err := client.Disconnect(); err != nil {
					slog.Error("disconnect error", "err", err)
				}

				return
			default:
			}

			if len(message.Message) <= 4 || message.Message[:5] != "!ask " {
				slog.Info("skipped", "chat_message", message.Message)
				return
			}

			slog.Info("adding to channel")
			select {
			case ch <- &twitchEvent{
				eventType: eventTypeChat,
				userName:  message.User.DisplayName,
				message:   message.Message[5:],
			}:
			default:
				slog.Info("queue is full")
			}
		})

		client.OnConnect(func() {
			slog.Info("connected")
		})

		client.Join(user)

		client.SendPings = true
		client.IdlePingInterval = 10 * time.Second

		err := client.Connect()
		if err != nil {
			slog.Error("connect error", "err", err)
		}
	}()

	return ch
}

func processMessages(ctx context.Context, settings *db.Settings, inputCh chan *twitchEvent, userMemory *memory.Memory) chan *ws.Message {
	ch := make(chan *ws.Message)

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
				if err := onAsk(ctx, ch, settings, event, userMemory); err != nil {
					fmt.Println(err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

func isValidUser(user string, w http.ResponseWriter) bool {
	if whitelist, err := db.GetDbWhitelist(); err != nil {
		slog.Error("failed to get whitelist", "err", err, "user", user)
		return false
	} else if !slices.ContainsFunc(whitelist.List, func(h *db.Human) bool {
		return strings.EqualFold(h.Login, user) && h.BannedBy == nil
	}) {
		w.WriteHeader(http.StatusForbidden)
		data, _ := os.ReadFile("client/whitelist.html")
		w.Write(data)

		slog.Info("whitelist rejected", "user", user)

		return false
	} else {
		return true
	}
}

func randEvents(ctx context.Context, interval time.Duration) chan *twitchEvent {
	randomEvents := make(chan *twitchEvent)

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

	settings, err := db.GetDbSettings(user)
	if err != nil {
		fmt.Println("failed to get settings:", err)
		settings = &db.Settings{
			Chat: true,
		}
	}

	wsClient, done := ws.NewWsClient(c)

	defer func() {
		fmt.Println("close ws")
		wsClient.Close()
	}()

	fmt.Println("ws connected")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-ctx.Done()
		wsClient.Close()
	}()

	go func() {
		<-done
		cancel()
	}()

	// send heartbeats
	go func() {
		for {
			if err := wsClient.Send(&ws.Message{
				MsgType: websocket.TextMessage,
				Message: []byte("heartbeat"),
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
			msg, err := wsClient.Read()
			if err != nil {
				close(finishCh)

				return
			}

			if msg.MsgType != websocket.TextMessage {
				panic("not text not supported")
			}

			switch string(msg.Message) {
			case "heartbeat":
			case "finish":
				finishCh <- struct{}{}
			default:
				panic("unexpected message")
			}
		}
	}()

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

	if settings.ChannelPts || settings.Follows || settings.Subs || settings.Raids {
		eventsStream, err = eventSubDataStream(ctx, cancel, w, user, settings)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	if settings.Events && settings.EventsInterval >= 10 {
		randomEvents = randEventsFunc(ctx, time.Second*time.Duration(settings.EventsInterval))
	}

	var chatMsgs chan *twitchEvent = nil
	if settings.Chat {
		chatMsgs = messagesFetcher(ctx, user)
	}

	if chatMsgs == nil && eventsStream == nil && randomEvents == nil {
		fmt.Println("everything is disabled LULE")

		return
	}

	eventsStream = tools.PriorityFanIn(chatMsgs, eventsStream, randomEvents)

	userMemory := memory.New(10, 5)

	dataStream := processMessages(ctx, settings, eventsStream, userMemory)
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
		} else if err := wsClient.Send(msg); err != nil {
			fmt.Println("send err")
			return
		} else if msg, ok := <-dataStream; !ok {
			fmt.Println("not ok")
			return
		} else if err := wsClient.Send(msg); err != nil {
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

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

var httpClient = &http.Client{
	Timeout: 5 * time.Second,
}

func main() {
	db.InitDB()

	defer db.Close()

	logFile, err := os.OpenFile("logs/log.txt", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		log.Fatal(err)
	}

	slogLogger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	slog.SetDefault(slogLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = slg.WithSlog(ctx, slogLogger)

	connManager := conns.NewConnectionManager(ctx, &DefaultProcessor{})

	api := &API{
		connectionManager: connManager,
	}

	router := NewRouter(api)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		slog.Info("Starting server")

		if err := srv.ListenAndServe(); err != nil {
			slog.Error("ListenAndServe finished", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		slog.Info("Starting connections loop")

		if err := ProcessingLoop(ctx, connManager); err != nil {
			slog.Error("Processing loop error", "err", err)
		}

		slog.Info("Connections loop finished")
	}()

	select {
	case <-ctx.Done():
	case <-stop:
		slog.Info("Interrupt triggerred")
		cancel()
	}

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}

	wg.Wait()
}
