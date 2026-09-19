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
	correlator *correlator
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
		correlator:  newCorrelator(),
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

func (s *Service) handleMessage(msg gempir.PrivateMessage) {
	metrics.MessagesIngested.Inc()

	s.activeUsersLock.RLock()
	userCfg, ok := s.activeUsers[strings.ToLower(msg.Channel)]
	s.activeUsersLock.RUnlock()

	if !ok {
		return
	}

	if len(msg.Message) == 0 || len(msg.User.Name) == 0 {
		return
	}

	twitchUserID, _ := strconv.Atoi(msg.User.ID)

	// Handle ^^voice command before any other processing
	if voiceName, ok := parseVoiceCommand(msg.Message); ok {
		s.handleVoiceCommand(twitchUserID, msg.User.Name, voiceName)
		return
	}

	// Route ^^ commands (except ^^voice) to clanker queue
	if strings.HasPrefix(msg.Message, "^^") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.db.PushClankerMsg(ctx, msg.Channel, userCfg.twitchUserID, msg.User.Name, twitchUserID, msg.Message, msg.ID)
		if err != nil {
			s.logger.Error("failed to push clanker message", "err", err, "user", msg.Channel)
		} else {
			s.logger.Info("routed clanker message", "user", msg.Channel, "msg_id", msg.ID)
		}
		return
	}

	in := arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  msg.User.Name,
			TwitchUserID: twitchUserID,
			Message:      msg.Message,
			RewardID:     msg.CustomRewardID,
		},
		uniqueID: msg.ID,
	}

	switch {
	case len(msg.CustomRewardID) != 0:
		in.pairAs = msg.CustomRewardID
	case msg.Bits > 0 && !fromAnotherChannel(msg.Tags):
		s.logger.Info("chat cheer", "user", msg.Channel, "bits", msg.Bits, "raw", msg.Raw)
		if !userCfg.settings.LaneEnabled(db.MsgClassBits) {
			return
		}
		in.msg.Event = &db.EventMeta{Kind: db.EventKindCheer, Bits: msg.Bits, USD: float64(msg.Bits) / db.BitsPerUSD}
		in.pairAs = pairAsCheer(msg.Bits)
	case !userCfg.settings.LaneEnabled(db.MsgClassChat):
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.push(ctx, msg.Channel, userCfg, in, feedChat)
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

	switch {
	case msg.MsgID == "resub":
		in.msg.Event = &db.EventMeta{
			Kind:   db.EventKindResub,
			Tier:   subTier(msg.MsgParams["msg-param-sub-plan"]),
			Months: param("msg-param-cumulative-months"),
		}
		in.pairAs = pairAsResub
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

func (s *Service) pushMsg(ctx context.Context, userCfg *ingestUserConfig, msg db.TwitchMessage, uniqueID string) (uuid.UUID, error) {
	showImages := false
	data := &db.MessageData{
		ImageIDs:   imagetag.ExtractIDs(msg.Message, 2),
		ShowImages: &showImages,
	}

	return s.db.PushIngestMsg(ctx, userCfg.id, msg, data, uniqueID)
}

// push takes one feed's arrival. Of an event both feeds deliver, the first
// arrival becomes the queue row and the second only adds what its feed alone
// knows.
func (s *Service) push(ctx context.Context, channel string, userCfg *ingestUserConfig, in arrival, from feed) {
	lane, _ := in.msg.EventLane()
	logger := s.logger.With("user", channel, "unique_id", in.uniqueID)
	if lane != "" {
		logger = logger.With("lane", lane)
		if !userCfg.settings.LaneEnabled(lane) {
			logger.Info("event not queued", "reason", "lane is off")
			return
		}
	}

	// The feeds agree on the text as typed, so that is what pairs them.
	key := newPairKey(userCfg.twitchUserID, in.msg.TwitchUserID, in.pairAs, in.msg.Message)

	if in.msg.Event != nil && in.msg.Event.Kind == db.EventKindCheer {
		in.msg.Message = s.cheermotes.strip(ctx, userCfg.twitchUserID, in.msg.Message)
		if in.msg.Message == "" {
			logger.Info("event not queued", "reason", "nothing to say but the cheermotes")
			return
		}
	}

	create := func() (uuid.UUID, error) {
		return s.pushMsg(ctx, userCfg, in.msg, in.uniqueID)
	}

	var (
		twin   uuid.UUID
		paired bool
		err    error
	)
	if in.pairAs == "" {
		_, err = create()
	} else {
		twin, paired, err = s.correlator.pair(key, from, time.Now(), create)
	}
	if err != nil {
		logger.Error("failed to push message", "err", err)
		return
	}

	if !paired {
		logger.Info("ingested message", "msg_id", in.uniqueID)
		return
	}

	if from == feedEventSub {
		if err := s.db.SetMsgEvent(ctx, twin, in.msg.Event); err != nil {
			logger.Error("failed to add event to paired message", "err", err, "queue_id", twin)
			return
		}
	}
	logger.Info("paired message", "queue_id", twin)
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
