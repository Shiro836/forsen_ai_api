package processor

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/twitch"

	"github.com/google/uuid"
)

//go:embed sfx/*.mp3
var embeddedSFX embed.FS

type Processor struct {
	logger *slog.Logger

	db *db.DB

	connManager *conns.Manager

	// Handlers
	aiHandler        InteractionHandler
	ttsHandler       InteractionHandler
	universalHandler InteractionHandler
	agenticHandler   InteractionHandler
}

func NewProcessor(logger *slog.Logger, db *db.DB, connManager *conns.Manager, aiHandler InteractionHandler, ttsHandler InteractionHandler, universalHandler InteractionHandler, agenticHandler InteractionHandler) *Processor {
	return &Processor{
		logger:           logger,
		db:               db,
		connManager:      connManager,
		aiHandler:        aiHandler,
		ttsHandler:       ttsHandler,
		universalHandler: universalHandler,
		agenticHandler:   agenticHandler,
	}
}

type ProcessorState struct {
	skippedMsgIDs     map[uuid.UUID]struct{}
	skippedMsgIDsLock sync.Mutex

	currentMsgID     uuid.UUID
	currentMsgIDLock sync.Mutex
}

func NewProcessorState() *ProcessorState {
	return &ProcessorState{
		skippedMsgIDs: make(map[uuid.UUID]struct{}),
		currentMsgID:  uuid.Nil,
	}
}

func (s *ProcessorState) AddSkipped(id uuid.UUID) {
	s.skippedMsgIDsLock.Lock()
	defer s.skippedMsgIDsLock.Unlock()
	s.skippedMsgIDs[id] = struct{}{}
}

func (s *ProcessorState) IsSkipped(id uuid.UUID) bool {
	s.skippedMsgIDsLock.Lock()
	defer s.skippedMsgIDsLock.Unlock()
	_, ok := s.skippedMsgIDs[id]
	return ok
}

func (s *ProcessorState) SetCurrent(id uuid.UUID) {
	s.currentMsgIDLock.Lock()
	defer s.currentMsgIDLock.Unlock()
	s.currentMsgID = id
}

func (s *ProcessorState) GetCurrent() uuid.UUID {
	s.currentMsgIDLock.Lock()
	defer s.currentMsgIDLock.Unlock()
	return s.currentMsgID
}

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster *db.User) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	logger := p.logger.With("user", broadcaster.TwitchLogin)

	defer func() {
		if r := recover(); r != nil {
			logger.Error("connection panic", "r", r)
		}
	}()

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte(" "),
	})

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(" "),
	})

	state := NewProcessorState()

	go p.handleControlSignals(ctx, updates, eventWriter, broadcaster, state, cancel)

	go p.ingestTwitchMessages(ctx, broadcaster, cancel)

	return p.processLoop(ctx, eventWriter, broadcaster, state)
}

func (p *Processor) processLoop(ctx context.Context, eventWriter conns.EventWriter, broadcaster *db.User, state *ProcessorState) error {
	logger := p.logger.With("user", broadcaster.TwitchLogin, "component", "process_loop")
	for {
		updated, err := p.db.UpdateCurrentMessages(ctx, broadcaster.ID)
		if err != nil {
			logger.Error("error updating current message", "err", err)
			return fmt.Errorf("error updating current message: %w", err)
		}

		if updated > 0 {
			p.connManager.NotifyControlPanel(broadcaster.ID)
		}

		msg, err := p.db.GetNextMsg(ctx, broadcaster.ID)
		if err != nil {
			if errors.Is(err, db.ErrNoRows) {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Second):
					continue
				}
			}
			logger.Error("error getting next message from db", "err", err)
			return fmt.Errorf("error getting next message from db: %w", err)
		}

		if err := p.processNextMessage(ctx, eventWriter, broadcaster, state, msg); err != nil {
			logger.Error("error processing message", "msg_id", msg.ID, "err", err)
			// Continue processing other messages unless it's a critical error?
			// The original code returned error on some failures.
			// processNextMessage will return error if it's critical.
			// But we want to keep the loop running.
			// Let's log and continue.
			continue
		}
	}
}

func (p *Processor) processNextMessage(ctx context.Context, eventWriter conns.EventWriter, broadcaster *db.User, state *ProcessorState, msg *db.Message) error {
	logger := p.logger.With("user", broadcaster.TwitchLogin, "msg_id", msg.ID)

	userSettings, err := p.db.GetUserSettings(ctx, broadcaster.ID)
	if err != nil {
		logger.Warn("failed to get user settings, using defaults", "err", err)
		userSettings = &db.UserSettings{}
	}

	if err := p.db.UpdateMessageStatus(ctx, msg.ID, db.MsgStatusCurrent); err != nil {
		return fmt.Errorf("error updating message status: %w", err)
	}

	state.SetCurrent(msg.ID)
	defer state.SetCurrent(uuid.Nil)

	p.connManager.NotifyControlPanel(broadcaster.ID)

	if len(msg.TwitchMessage.RewardID) == 0 {
		return nil
	}

	// Try to get any reward (character or universal) by Twitch reward ID
	cardID, rewardType, err := p.db.GetRewardByTwitchReward(ctx, broadcaster.ID, msg.TwitchMessage.RewardID)
	if err != nil {
		if db.ErrCode(err) != db.ErrCodeNoRows {
			logger.Error("error getting reward by twitch reward", "err", err)
		}
		return nil
	}

	// Get character card if this is a character-based reward
	var charCard *db.Card
	if cardID != nil {
		charCard, err = p.db.GetCharCardByID(ctx, broadcaster.ID, *cardID)
		if err != nil {
			logger.Error("error getting character card", "err", err)
			return nil
		}
	}

	input := InteractionInput{
		Requester:    msg.TwitchMessage.TwitchLogin,
		Message:      msg.TwitchMessage.Message,
		Character:    charCard,
		UserSettings: userSettings,
		MsgID:        msg.ID.String(),
		State:        state,
	}

	switch rewardType {
	case db.TwitchRewardTTS:
		if err := p.ttsHandler.Handle(ctx, input, eventWriter); err != nil {
			return fmt.Errorf("tts handler error: %w", err)
		}
	case db.TwitchRewardUniversalTTS:
		input.Character = nil
		if err := p.universalHandler.Handle(ctx, input, eventWriter); err != nil {
			return fmt.Errorf("universal handler error: %w", err)
		}
	case db.TwitchRewardAI:
		if err := p.aiHandler.Handle(ctx, input, eventWriter); err != nil {
			return fmt.Errorf("ai handler error: %w", err)
		}
	case db.TwitchRewardAgentic:
		if err := p.agenticHandler.Handle(ctx, input, eventWriter); err != nil {
			return fmt.Errorf("agentic handler error: %w", err)
		}
	default:
		logger.Error("unexpected reward type", "reward_type", rewardType)
	}

	return nil
}

func (p *Processor) handleControlSignals(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster *db.User, state *ProcessorState, cancel context.CancelFunc) {
	defer cancel()
	logger := p.logger.With("user", broadcaster.TwitchLogin, "component", "control_signals")

	// Helpers for control actions
	skipMessage := func(msgID uuid.UUID) {
		state.AddSkipped(msgID)
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeSkip,
			EventData: []byte(msgID.String()),
		})
		if err := p.db.UpdateMessageStatus(ctx, msgID, db.MsgStatusDeleted); err != nil {
			logger.Error("error updating message status", "err", err)
		}
		p.connManager.NotifyControlPanel(broadcaster.ID)
	}

	updateImageState := func(msgID uuid.UUID, show bool) {
		eventType := conns.EventTypeShowImages
		if !show {
			eventType = conns.EventTypeHideImages
		}
		eventWriter(&conns.DataEvent{
			EventType: eventType,
			EventData: []byte(msgID.String()),
		})
		p.db.UpdateMessageData(ctx, msgID, &db.MessageData{ShowImages: &show})
		p.connManager.NotifyControlPanel(broadcaster.ID)
	}

	for {
		select {
		case upd, ok := <-updates:
			if !ok {
				return
			}

			logger.Info("processor signal recieved", "upd_signal", upd)

			switch upd.UpdateType {
			case conns.RestartProcessor:
				return

			case conns.SkipMessage:
				msgID, err := uuid.Parse(upd.Data)
				if err != nil {
					logger.Error("msg id is not valid uuid", "err", err)
					continue
				}
				skipMessage(msgID)

			case conns.ShowImages:
				msgID, err := uuid.Parse(upd.Data)
				if err != nil {
					logger.Error("msg id is not valid uuid", "err", err)
					continue
				}
				updateImageState(msgID, true)

			case conns.HideImages:
				msgID, err := uuid.Parse(upd.Data)
				if err != nil {
					logger.Error("msg id is not valid uuid", "err", err)
					continue
				}
				updateImageState(msgID, false)

			case conns.CleanOverlay:
				eventWriter(&conns.DataEvent{
					EventType: conns.EventTypeText,
					EventData: []byte(" "),
				})
				eventWriter(&conns.DataEvent{
					EventType: conns.EventTypeImage,
					EventData: []byte(" "),
				})

			case conns.SkipCurrent, conns.ShowImagesCurrent:
				// Shared token check
				token := upd.Data
				if len(token) == 0 {
					continue
				}
				settings, err := p.db.GetUserSettings(ctx, broadcaster.ID)
				if err != nil {
					logger.Warn("failed to get user settings for token check", "err", err)
					continue
				}
				if token != settings.Token || len(settings.Token) == 0 {
					logger.Warn("invalid token for current action")
					continue
				}

				targetUUID := state.GetCurrent()
				if targetUUID == uuid.Nil {
					continue
				}

				if upd.UpdateType == conns.SkipCurrent {
					skipMessage(targetUUID)
				} else {
					updateImageState(targetUUID, true)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *Processor) ingestTwitchMessages(ctx context.Context, broadcaster *db.User, cancel context.CancelFunc) {
	defer cancel()
	logger := p.logger.With("user", broadcaster.TwitchLogin, "component", "ingest")

	twitchChatCh := twitch.MessagesFetcher(ctx, broadcaster.TwitchLogin, true)
	imgRegex := regexp.MustCompile(`<img:([A-Za-z0-9]{5})>`)

	for msg := range twitchChatCh {
		if len(msg.Message) == 0 || len(msg.TwitchLogin) == 0 {
			continue
		}

		if len(msg.RewardID) == 0 {
			continue
		}

		// msg.Message = unidecode.Unidecode(msg.Message)

		imgMatches := imgRegex.FindAllStringSubmatch(msg.Message, -1)
		imageIDs := make([]string, 0, 2)
		for _, m := range imgMatches {
			if len(m) >= 2 {
				imageIDs = append(imageIDs, m[1])
				if len(imageIDs) == 2 {
					break
				}
			}
		}

		showImages := false

		_, err := p.db.PushMsg(ctx, broadcaster.ID, db.TwitchMessage{
			TwitchLogin: msg.TwitchLogin,
			Message:     msg.Message,
			RewardID:    msg.RewardID,
		}, &db.MessageData{ImageIDs: imageIDs, ShowImages: &showImages})
		if err != nil {
			logger.Error("error pushing message to db", "err", err)
		}
		if len(imageIDs) > 0 {
			logger.Info("stored image ids with message", "ids", imageIDs)
		}

		p.connManager.NotifyControlPanel(broadcaster.ID)
	}
}
