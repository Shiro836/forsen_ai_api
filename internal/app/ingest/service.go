package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/imagetag"
	"app/pkg/twitch"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/google/uuid"
)

type feed string

const (
	feedChat     feed = "chat"
	feedEventSub feed = "events"
)

// ingestUserConfig is never changed once it is in activeUsers; a sync swaps
// in a new one, so handlers read theirs without the lock.
type ingestUserConfig struct {
	id           uuid.UUID
	twitchUserID int
	settings     db.UserSettings
}

type Service struct {
	logger *slog.Logger
	db     *db.DB
	cfg    *twitch.Config

	chatClient *twitch.ShardedClient
	eventSub   *eventSub
	cheermotes *cheermotes

	activeUsers     map[string]*ingestUserConfig
	activeUsersLock sync.RWMutex
}

func NewService(logger *slog.Logger, database *db.DB, cfg *twitch.Config, eventSubCfg EventSubConfig) *Service {
	app := newAppClient(cfg)

	s := &Service{
		logger:      logger,
		db:          database,
		cfg:         cfg,
		cheermotes:  newCheermotes(logger, app),
		activeUsers: make(map[string]*ingestUserConfig),
	}

	if eventSubCfg.Callback != "" {
		s.eventSub = newEventSub(logger.WithGroup("eventsub"), eventSubCfg, app)
	}

	s.chatClient = twitch.NewShardedClient(
		logger,
		s.handleMessage,
		s.handleUserNotice,
		func() { metrics.ConnectedClients.Inc() },
		func() { metrics.ConnectedClients.Dec() },
		func(channel, reason string) {
			metrics.JoinFailures.WithLabelValues(reason).Inc()
			s.logger.Warn("twitch channel join failed", "channel", channel, "reason", reason)
		},
	)

	return s
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("starting ingest service")

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.pollUsers(ctx)
	}()

	if s.eventSub != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.eventSub.run(ctx, s.handleEventSub)
		}()
	} else {
		s.logger.Warn("eventsub is not configured, ingesting from chat only")
	}

	<-ctx.Done()
	s.logger.Info("stopping ingest service")

	s.chatClient.Close()

	wg.Wait()
	return nil
}

func (s *Service) pollUsers(ctx context.Context) {
	if err := s.syncUsers(ctx); err != nil {
		s.logger.Error("failed to sync users", "err", err)
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.syncUsers(ctx); err != nil {
				s.logger.Error("failed to sync users", "err", err)
			}
		}
	}
}

// A granted streamer we could never act for is not worth an IRC join. Bits is
// not asked: it is on until turned off, so it says nothing about who uses us.
func usesIngest(u *db.IngestUser) bool {
	if u.HasRewardButton {
		return true
	}
	for _, class := range []db.MsgClass{db.MsgClassChat, db.MsgClassSub, db.MsgClassRaid, db.MsgClassStreak, db.MsgClassFollow} {
		if u.Settings.LaneEnabled(class) {
			return true
		}
	}
	return false
}

func (s *Service) syncUsers(ctx context.Context) error {
	users, err := s.db.GetIngestUsers(ctx)
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}

	metrics.TotalGrantedChannels.Set(float64(len(users)))

	desiredUsers := make(map[string]*ingestUserConfig, len(users))
	desiredSubs := make(map[subKey]struct{})
	for _, u := range users {
		if !usesIngest(u) {
			continue
		}

		for _, key := range desiredSubscriptions(u) {
			desiredSubs[key] = struct{}{}
		}

		desiredUsers[strings.ToLower(u.TwitchLogin)] = &ingestUserConfig{
			id:           u.ID,
			twitchUserID: u.TwitchUserID,
			settings:     u.Settings,
		}
	}

	if s.eventSub != nil {
		s.eventSub.setDesired(desiredSubs)
	}

	s.activeUsersLock.Lock()
	defer s.activeUsersLock.Unlock()

	for login, cfg := range desiredUsers {
		if _, ok := s.activeUsers[login]; !ok {
			s.logger.Info("joining channel", "login", login)
			if err := s.joinChannel(login); err != nil {
				s.logger.Error("failed to join channel", "login", login, "err", err)
				continue
			}
		}
		s.activeUsers[login] = cfg
	}

	for login := range s.activeUsers {
		if _, ok := desiredUsers[login]; !ok {
			s.logger.Info("departing channel", "login", login)
			s.departChannel(login)
			delete(s.activeUsers, login)
		}
	}

	metrics.ActiveChannels.Set(float64(len(s.activeUsers)))
	metrics.ShardCount.Set(float64(s.chatClient.ShardCount()))
	metrics.JoinedChannels.Set(float64(s.chatClient.JoinedChannelCount()))

	return nil
}

func (s *Service) joinChannel(channel string) error {
	s.chatClient.Join(channel)
	return nil
}

func (s *Service) departChannel(channel string) {
	s.chatClient.Depart(channel)
}

// chatLine is a chat message the way either feed delivers it. Both feeds are
// turned into one and handled by the same code, under the id Twitch gave the
// message, which is the same on both.
type chatLine struct {
	channel  string
	id       string
	viewerID int
	viewer   string
	text     string
	rewardID string
	// cheered is the bits of a cheer, and zero for anything else that costs bits.
	cheered int
}

func ircChatLine(msg gempir.PrivateMessage) chatLine {
	viewerID, _ := strconv.Atoi(msg.User.ID)
	bits, _ := cheerBits(msg)

	return chatLine{
		channel:  msg.Channel,
		id:       msg.ID,
		viewerID: viewerID,
		viewer:   msg.User.Name,
		text:     msg.Message,
		rewardID: msg.CustomRewardID,
		cheered:  bits,
	}
}

func (s *Service) handleMessage(msg gempir.PrivateMessage) {
	metrics.MessagesIngested.Inc()

	line := ircChatLine(msg)
	if line.cheered > 0 {
		s.logger.Info("chat cheer", "user", msg.Channel, "bits", line.cheered, "raw", msg.Raw)
	}

	s.handleChatLine(line, feedChat)
}

func (s *Service) handleChatLine(line chatLine, from feed) {
	s.activeUsersLock.RLock()
	userCfg, ok := s.activeUsers[strings.ToLower(line.channel)]
	s.activeUsersLock.RUnlock()

	if !ok {
		return
	}

	if len(line.text) == 0 || len(line.viewer) == 0 {
		return
	}

	// Handle ^^voice command before any other processing
	if voiceName, ok := parseVoiceCommand(line.text); ok {
		s.handleVoiceCommand(line.viewerID, line.viewer, voiceName)
		return
	}

	// Route ^^ commands (except ^^voice) to clanker queue
	if strings.HasPrefix(line.text, "^^") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.db.PushClankerMsg(ctx, line.channel, userCfg.twitchUserID, line.viewer, line.viewerID, line.text, line.id)
		if err != nil {
			s.logger.Error("failed to push clanker message", "err", err, "user", line.channel)
		} else {
			s.logger.Info("routed clanker message", "user", line.channel, "msg_id", line.id)
		}
		return
	}

	in := arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  line.viewer,
			TwitchUserID: line.viewerID,
			Message:      line.text,
			RewardID:     line.rewardID,
		},
		uniqueID: line.id,
	}

	switch {
	case len(line.rewardID) != 0:
	case line.cheered > 0 && userCfg.settings.LaneEnabled(db.MsgClassBits):
		in.msg.Event = &db.EventMeta{Kind: db.EventKindCheer, Bits: line.cheered, USD: float64(line.cheered) / db.BitsPerUSD}
	case !userCfg.settings.LaneEnabled(db.MsgClassChat):
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.push(ctx, line.channel, userCfg, in, from)
}

// cheerBits is the bits of a cheer: a plain chat message that spends them. A
// Power-up's message carries a notice id and is never one.
func cheerBits(msg gempir.PrivateMessage) (int, bool) {
	if msg.Bits == 0 || msg.Tags["msg-id"] != "" || fromAnotherChannel(msg.Tags) {
		return 0, false
	}
	return msg.Bits, true
}

// In a shared chat the other channels' messages arrive here too, and what was
// cheered or subscribed there did not happen here.
func fromAnotherChannel(tags map[string]string) bool {
	source := tags["source-room-id"]
	return source != "" && source != tags["room-id"]
}

// noticeArrival is false for a notice that is not an event we play.
func noticeArrival(msg gempir.UserNoticeMessage) (in arrival, ok bool) {
	if fromAnotherChannel(msg.Tags) {
		return arrival{}, false
	}

	param := func(name string) int {
		n, _ := strconv.Atoi(msg.MsgParams[name])
		return n
	}

	viewerID, _ := strconv.Atoi(msg.User.ID)
	in = arrival{
		// A notice comes from Twitch, not from the viewer, so the login is a tag.
		msg:      db.TwitchMessage{TwitchLogin: msg.Tags["login"], TwitchUserID: viewerID, Message: msg.Message},
		uniqueID: msg.ID,
	}

	tier := subTier(msg.MsgParams["msg-param-sub-plan"])

	// The event feed names nobody behind an anonymous gift; chat names a
	// stand-in account, which must not pass for the gifter.
	if in.msg.TwitchLogin == anonymousGifterLogin {
		in.msg.TwitchLogin, in.msg.TwitchUserID = anonymousLogin, 0
	}

	switch {
	// Going from a Prime or a gifted sub to a paid one is announced by this
	// notice alone, with no sub notice beside it.
	case msg.MsgID == "sub", msg.MsgID == "primepaidupgrade", msg.MsgID == "giftpaidupgrade", msg.MsgID == "anongiftpaidupgrade":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindSub, Tier: tier}
	case msg.MsgID == "resub":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindResub, Tier: tier, Months: param("msg-param-cumulative-months")}
	case msg.MsgID == "submysterygift":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindGiftSubs, Tier: tier, GiftCount: param("msg-param-mass-gift-count")}
	// Every recipient of a community gift gets a notice of their own; the
	// gift was announced once already, by submysterygift. Paying a gift
	// forward is announced on top of the gift's own notices, never instead.
	case msg.MsgID == "subgift" && msg.MsgParams["msg-param-community-gift-id"] == "":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindGiftSubs, Tier: tier, GiftCount: 1}
	case msg.MsgID == "raid":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindRaid, Viewers: param("msg-param-viewerCount")}
	case msg.MsgID == "viewermilestone" && msg.MsgParams["msg-param-category"] == "watch-streak":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindStreak, Streak: param("msg-param-value")}
	default:
		return arrival{}, false
	}

	return in, true
}

func (s *Service) handleUserNotice(msg gempir.UserNoticeMessage) {
	// Chat announces more kinds than the queue plays, and the queue keeps only
	// what it plays, so the line itself is the record of the rest.
	s.logger.Info("chat notice", "user", msg.Channel, "notice", msg.MsgID, "raw", msg.Raw)

	s.activeUsersLock.RLock()
	userCfg, ok := s.activeUsers[strings.ToLower(msg.Channel)]
	s.activeUsersLock.RUnlock()
	if !ok {
		return
	}

	in, ok := noticeArrival(msg)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.push(ctx, msg.Channel, userCfg, in, feedChat)
}

// push queues one feed's arrival under Twitch's id for the message. The other
// feed brings the same id, so the queue itself turns its arrival into a no-op.
func (s *Service) push(ctx context.Context, channel string, userCfg *ingestUserConfig, in arrival, from feed) {
	logger := s.logger.With("user", channel, "unique_id", in.uniqueID, "feed", from)
	if lane, ok := in.msg.EventLane(); ok {
		logger = logger.With("lane", lane)
		if !userCfg.settings.LaneEnabled(lane) {
			logger.Info("event not queued", "reason", "lane is off")
			return
		}
	}

	var key string
	if in.msg.RewardID != "" {
		key = redeemKey(in.msg.TwitchUserID, in.msg.RewardID, in.msg.Message)
	}

	if in.msg.Event != nil && in.msg.Event.Kind == db.EventKindCheer {
		in.msg.Message = s.cheermotes.strip(ctx, userCfg.twitchUserID, in.msg.Message)
		if in.msg.Message == "" {
			logger.Info("event not queued", "reason", "nothing to say but the cheermotes")
			return
		}
	}

	showImages := false
	data := &db.MessageData{
		ImageIDs:   imagetag.ExtractIDs(in.msg.Message, 2),
		ShowImages: &showImages,
	}

	queueID, created, err := s.db.PushIngestMsg(ctx, userCfg.id, in.msg, data, in.uniqueID, key)
	if err != nil {
		logger.Error("failed to push message", "err", err)
		return
	}

	if created {
		logger.Info("ingested message", "queue_id", queueID)
	} else {
		logger.Info("message was queued already", "queue_id", queueID)
	}
}

func parseVoiceCommand(message string) (string, bool) {
	if !strings.HasPrefix(message, "^^voice ") {
		return "", false
	}

	voiceName := strings.TrimPrefix(message, "^^voice ")
	voiceName = strings.Trim(voiceName, "\" ")

	if len(voiceName) == 0 {
		return "", false
	}

	return voiceName, true
}

func (s *Service) handleVoiceCommand(twitchUserID int, twitchLogin, voiceName string) {
	if twitchUserID == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exists, err := s.db.VoiceShortNameExists(ctx, voiceName)
	if err != nil {
		s.logger.Error("failed to check voice existence", "err", err, "voice", voiceName)
		return
	}

	if !exists {
		s.logger.Info("voice not found", "voice", voiceName, "user", twitchLogin)
		return
	}

	if err := s.db.SetChatUserVoice(ctx, twitchUserID, twitchLogin, voiceName); err != nil {
		s.logger.Error("failed to set chat user voice", "err", err, "user", twitchLogin, "voice", voiceName)
		return
	}

	s.logger.Info("voice set", "user", twitchLogin, "voice", voiceName)
}
