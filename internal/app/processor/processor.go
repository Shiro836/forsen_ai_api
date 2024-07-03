package processor

// use go interpreter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/internal/app/notifications"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/swearfilter"
	"app/pkg/tools"
	"app/pkg/twitch"

	"github.com/google/uuid"
)

type Processor struct {
	logger *slog.Logger

	llm      *ai.VLLMClient
	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	rvc      *ai.RVCClient
	whisper  *ai.WhisperClient

	db *db.DB

	ffmpeg *ffmpeg.Client

	controlPanelNotifications *notifications.Client
}

func NewProcessor(logger *slog.Logger, llm *ai.VLLMClient, styleTts *ai.StyleTTSClient, metaTts *ai.MetaTTSClient,
	rvc *ai.RVCClient, whisper *ai.WhisperClient, db *db.DB, ffmpeg *ffmpeg.Client, controlPanelNotifications *notifications.Client) *Processor {
	return &Processor{
		llm:      llm,
		styleTts: styleTts,
		metaTts:  metaTts,
		rvc:      rvc,
		whisper:  whisper,

		logger: logger,

		db: db,

		ffmpeg: ffmpeg,

		controlPanelNotifications: controlPanelNotifications,
	}
}

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster *db.User) error {
	ctx, cancel := context.WithCancel(ctx)

	logger := p.logger.With("user", broadcaster.TwitchLogin)

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte("processor started"),
	})

	defer func() {
		cancel()
		if r := recover(); r != nil {
			logger.Error("connection panic")
		}
	}()

	skippedMsgIDs := make(map[uuid.UUID]struct{})
	skippedMsgIDsLock := sync.Mutex{}

	go func() {

	loop:
		for {
			select {
			case upd, ok := <-updates:
				if !ok {
					updates = nil
					cancel()
					break loop
				}

				logger.Info("processor signal recieved", "upd_signal", upd)
				switch upd.UpdateType {
				case conns.RestartProcessor:
					cancel()
					break loop
				case conns.SkipMessage:
					msgID, err := uuid.Parse(upd.Data)
					if err != nil {
						logger.Error("msg id is not valid uuid", "err", err)
						continue
					}

					func() {
						skippedMsgIDsLock.Lock()
						defer skippedMsgIDsLock.Unlock()

						skippedMsgIDs[msgID] = struct{}{}
					}()

					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeSkip,
						EventData: []byte(upd.Data),
					})
				}
			case <-ctx.Done():
				break loop
			}
		}
	}()

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		twitchChatCh := twitch.MessagesFetcher(ctx, broadcaster.TwitchLogin, true)

		for msg := range twitchChatCh {
			if len(msg.Message) == 0 || len(msg.TwitchLogin) == 0 {
				continue
			}

			if len(msg.RewardID) == 0 {
				continue
			}

			if err := p.db.PushMsg(ctx, broadcaster.ID, db.TwitchMessage{
				TwitchLogin: msg.TwitchLogin,
				Message:     msg.Message,
				RewardID:    msg.RewardID,
			}); err != nil {
				logger.Error("error pushing message to db", "err", err)
			}

			p.controlPanelNotifications.Notify(broadcaster.ID)
		}
	}()

	for {
		if err := p.db.UpdateCurrentMessage(ctx, broadcaster.ID); err != nil {
			logger.Error("error updating current message", "err", err)

			return fmt.Errorf("error updating current message: %w", err)
		}

		p.controlPanelNotifications.Notify(broadcaster.ID)

		var msg *db.Message
		msg, err := p.db.GetNextMsg(ctx, broadcaster.ID)
		if err != nil {
			if errors.Is(err, db.ErrNoRows) {
				// TODO: metrics

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

		if err := p.db.UpdateMessageStatus(ctx, msg.ID, db.StatusCurrent); err != nil {
			logger.Error("error updating message status", "err", err)

			return fmt.Errorf("error updating message status: %w", err)
		}

		p.controlPanelNotifications.Notify(broadcaster.ID)

		if len(msg.TwitchMessage.RewardID) == 0 {
			continue
		}

		charCard, rewardType, err := p.db.GetCharCardByTwitchReward(ctx, broadcaster.ID, msg.TwitchMessage.RewardID)
		if err != nil {
			logger.Error("error getting card by twitch reward", "err", err)
			continue
		}

		if rewardType == db.TwitchRewardTTS {
			requestAudio, err := p.TTS(ctx, msg.TwitchMessage.Message, charCard.Data.VoiceReference)
			if err != nil {
				logger.Error("error tts", "err", err)
				return err
			}

			requestTtsDone, err := p.playTTS(ctx, eventWriter, msg.TwitchMessage.Message, requestAudio)
			if err != nil {
				logger.Error("error playing tts", "err", err)
				return err
			}

			select {
			case <-requestTtsDone:
			case <-ctx.Done():
				return nil
			}

			continue
		}

		if rewardType != db.TwitchRewardAI {
			logger.Error("unexpected reward type", "reward_type", rewardType)
			continue
		}

		requester := msg.TwitchMessage.TwitchLogin

		prompt, err := p.craftPrompt(ctx, charCard, requester, msg.TwitchMessage.Message)
		if err != nil {
			logger.Error("error crafting prompt", "err", err)
			continue
		}

		var llmResult string
		var llmResultErr error

		llmResultDone := make(chan struct{})
		go func() {
			defer close(llmResultDone)

			llmResult, llmResultErr = p.llm.Ask(ctx, prompt)
		}()

		requestText := requester + " asked me: " + msg.TwitchMessage.Message

		requestAudio, err := p.TTS(ctx, requestText, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			return err
		}

		requestTtsDone, err := p.playTTS(ctx, eventWriter, requestText, requestAudio)
		if err != nil {
			logger.Error("error playing tts", "err", err)
			return err
		}

		select {
		case <-llmResultDone:
			if llmResultErr != nil {
				logger.Error("error asking llm", "err", llmResultErr)
				continue
			}
		case <-ctx.Done():
			return nil
		}

		responseTtsAudio, err := p.TTS(ctx, llmResult, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			continue
		}

		select {
		case <-requestTtsDone:
		case <-ctx.Done():
			return nil
		}

		responseTtsDone, err := p.playTTS(ctx, eventWriter, llmResult, responseTtsAudio)
		if err != nil {
			logger.Error("error playing tts", "err", err)
			continue
		}

		select {
		case <-responseTtsDone:
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *Processor) playTTS(ctx context.Context, eventWriter conns.EventWriter, msg string, audio []byte) (<-chan struct{}, error) {
	textTimings, err := p.alignTextToAudio(ctx, msg, audio)
	if err != nil {
		return nil, fmt.Errorf("error aligning text to audio: %w", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: audio,
		})

		startTS := time.Now() // linter, are you drunk, or am I drunk???

		fullText := ""

		for _, timing := range textTimings {
			fullText += timing.Text

			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte(fullText),
			})

			select {
			case <-time.After(time.Until(startTS.Add(timing.End))):
			case <-ctx.Done():
				return
			}
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeText,
			EventData: []byte(" "),
		})
	}()

	return done, nil
}

func (p *Processor) TTS(ctx context.Context, msg string, refAudio []byte) ([]byte, error) {
	ttsResult, err := p.styleTts.TTS(ctx, msg, refAudio)
	if err != nil {
		return nil, err
	}

	return ttsResult, nil
}

func (p *Processor) FilterText(ctx context.Context, broadcaster *db.User, text string) string {
	var swears []string

	filters, err := p.db.GetFilters(ctx, broadcaster.ID)
	if err == nil {
		slices.Concat(swears, strings.Split(filters, ","))
	}

	slices.Concat(swears, swearfilter.Swears)

	swearFilterObj := swearfilter.NewSwearFilter(false, swears...)

	filtered := text

	tripped, _ := swearFilterObj.Check(text)
	for _, word := range tripped {
		filtered = tools.IReplace(filtered, word, strings.Repeat("*", len(word)))
	}

	return filtered
}

func (p *Processor) craftPrompt(ctx context.Context, char *db.Card, requester string, message string) (prompt string, err error) {
	panic("implement me")
}

func (p *Processor) getAudioLength(ctx context.Context, data []byte) (time.Duration, error) {
	res, err := p.ffmpeg.Ffprobe(ctx, data)
	if err != nil {
		return 0, fmt.Errorf("error getting audio length: %w", err)
	}

	return res.Duration, nil
}

type timing struct {
	Text  string
	Start time.Duration
	End   time.Duration
}

func (p *Processor) alignTextToAudio(ctx context.Context, text string, audio []byte) ([]timing, error) {
	audioData, err := p.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
	}

	return []timing{ // TODO: use https://github.com/Shiro836/whisperX-api to align text to audio
		{
			Text:  text,
			Start: 0,
			End:   audioData,
		},
	}, nil
}
