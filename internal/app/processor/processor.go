package processor

// use go interpreter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/internal/app/notifications"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/twitch"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

type Processor struct {
	logger *slog.Logger

	llmModel *llm.Client
	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	rvc      *ai.RVCClient
	whisper  *whisperx.Client

	db *db.DB

	ffmpeg *ffmpeg.Client

	controlPanelNotifications *notifications.Client
}

func NewProcessor(logger *slog.Logger, llmModel *llm.Client, styleTts *ai.StyleTTSClient, metaTts *ai.MetaTTSClient,
	rvc *ai.RVCClient, whisper *whisperx.Client, db *db.DB, ffmpeg *ffmpeg.Client, controlPanelNotifications *notifications.Client) *Processor {
	return &Processor{
		llmModel: llmModel,
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

	interrupt := make(chan struct{})
	defer close(interrupt)

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

					if err = p.db.UpdateMessageStatus(ctx, msgID, db.MsgStatusDeleted); err != nil {
						logger.Error("error updating message status", "err", err)
					}

					p.controlPanelNotifications.Notify(broadcaster.ID)
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
		updated, err := p.db.UpdateCurrentMessages(ctx, broadcaster.ID)
		if err != nil {
			logger.Error("error updating current message", "err", err)

			return fmt.Errorf("error updating current message: %w", err)
		}

		if updated > 0 {
			p.controlPanelNotifications.Notify(broadcaster.ID)
		}

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

		skipMsg := false

		func() {
			skippedMsgIDsLock.Lock()
			defer skippedMsgIDsLock.Unlock()

			if _, ok := skippedMsgIDs[msg.ID]; ok {
				skipMsg = true
			}
		}()

		if skipMsg {
			continue
		}

		if err := p.db.UpdateMessageStatus(ctx, msg.ID, db.MsgStatusCurrent); err != nil {
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
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeImage,
				EventData: []byte("/characters/" + charCard.ID.String() + "/image"),
			})

			filteredRequest := p.FilterText(ctx, broadcaster.ID, msg.TwitchMessage.Message)

			requestAudio, err := p.TTS(ctx, filteredRequest, charCard.Data.VoiceReference)
			if err != nil {
				logger.Error("error tts", "err", err)
				return err
			}

			requestTtsDone, err := p.playTTS(ctx, eventWriter, filteredRequest, msg.ID.String(), requestAudio)
			if err != nil {
				logger.Error("error playing tts", "err", err)
				return err
			}

			select {
			case <-requestTtsDone:
			case <-ctx.Done():
				return nil
			}

			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeImage,
				EventData: []byte(" "),
			})

			continue
		}

		if rewardType != db.TwitchRewardAI {
			logger.Error("unexpected reward type", "reward_type", rewardType)
			continue
		}

		requester := msg.TwitchMessage.TwitchLogin

		prompt, err := p.craftPrompt(charCard, requester, msg.TwitchMessage.Message)
		if err != nil {
			logger.Error("error crafting prompt", "err", err)
			continue
		}

		var llmResult string
		var llmResultErr error

		llmResultDone := make(chan struct{})
		go func() {
			defer close(llmResultDone)

			llmResult, llmResultErr = p.llmModel.Ask(ctx, prompt)
		}()

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte("/characters/" + charCard.ID.String() + "/image"),
		})

		requestText := requester + " asked me: " + msg.TwitchMessage.Message

		filteredRequestText := p.FilterText(ctx, broadcaster.ID, requestText)

		requestAudio, err := p.TTS(ctx, filteredRequestText, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			return err
		}

		requestTtsDone, err := p.playTTS(ctx, eventWriter, filteredRequestText, msg.ID.String(), requestAudio)
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

			if len(llmResult) == 0 {
				llmResult = "empty response"
			}

			err = p.db.UpdateMessageData(ctx, msg.ID, &db.MessageData{
				AIResponse: llmResult,
			})
			if err != nil {
				logger.Error("error updating message data", "err", err)
				continue
			}
			p.controlPanelNotifications.Notify(broadcaster.ID)

			logger.Info("llm result", "result", llmResult)
		case <-ctx.Done():
			return nil
		}

		filteredResponse := p.FilterText(ctx, broadcaster.ID, llmResult)

		responseTtsAudio, err := p.TTS(ctx, filteredResponse, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			continue
		}

		select {
		case <-requestTtsDone:
		case <-ctx.Done():
			return nil
		}

		select { // to prevent audio overlap
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil
		}

		func() {
			skippedMsgIDsLock.Lock()
			defer skippedMsgIDsLock.Unlock()

			if _, ok := skippedMsgIDs[msg.ID]; ok {
				skipMsg = true
			}
		}()

		if skipMsg {
			continue
		}

		responseTtsDone, err := p.playTTS(ctx, eventWriter, filteredResponse, msg.ID.String(), responseTtsAudio)
		if err != nil {
			logger.Error("error playing tts", "err", err)
			continue
		}

		select {
		case <-responseTtsDone:
		case <-ctx.Done():
			return nil
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte(" "),
		})
	}
}

type audioMsg struct {
	Audio []byte `json:"audio"`
	MsgID string `json:"msg_id"`
}

func (p *Processor) playTTS(ctx context.Context, eventWriter conns.EventWriter, msg string, msdID string, audio []byte) (<-chan struct{}, error) {
	textTimings, err := p.alignTextToAudio(ctx, msg, audio)
	if err != nil {
		return nil, fmt.Errorf("error aligning text to audio: %w", err)
	}

	mp3Audio, err := p.ffmpeg.Ffmpeg2Mp3(ctx, audio)
	if err == nil {
		audio = mp3Audio
	} else {
		p.logger.Error("error converting audio to mp3", "err", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		audioMsg, err := json.Marshal(&audioMsg{
			Audio: audio,
			MsgID: msdID,
		})
		if err != nil {
			return
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: audioMsg,
		})

		startTS := time.Now()

		fullText := ""

		for _, timing := range textTimings {
			fullText += timing.Text + " "

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

func (p *Processor) FilterText(ctx context.Context, broadcasterID uuid.UUID, text string) string {
	swears := GlobalSwears // regex patterns

	userSettings, err := p.db.GetUserSettings(ctx, broadcasterID)
	if err == nil && len(userSettings.Filters) != 0 {
		swears = slices.Concat(swears, strings.Split(userSettings.Filters, ","))
	}

	for _, exp := range swears {
		r, err := regexp.Compile("(?i)" + exp) // makes them case-insensitive by default
		if err != nil {
			p.logger.Warn(fmt.Sprintf("failed compiling reg expression '%s' for %s", exp, broadcasterID), "err", err)
			continue
		}
		text = r.ReplaceAllString(text, "(filtered)")
	}

	return text
}

func (p *Processor) craftPrompt(char *db.Card, requester string, message string) (string, error) {
	data := char.Data

	messageExamples := &strings.Builder{}
	for _, msgExample := range data.MessageExamples {
		messageExamples.WriteString(fmt.Sprintf("<START>###UserName: %s\n###%s: %s<END>\n", msgExample.Request, data.Name, msgExample.Response))
	}

	prompt := &strings.Builder{}
	prompt.WriteString("Start request/response pairs with <START> and end with <END>\n")
	if len(data.Name) != 0 {
		prompt.WriteString(fmt.Sprintf("Name: %s\n", data.Name))
	}
	if len(data.Description) != 0 {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", data.Description))
	}
	if len(data.Personality) != 0 {
		prompt.WriteString(fmt.Sprintf("Personality: %s\n", data.Personality))
	}
	if len(data.MessageExamples) != 0 {
		prompt.WriteString(fmt.Sprintf("Message Examples: %s", messageExamples.String()))
	}
	if len(data.SystemPrompt) != 0 {
		prompt.WriteString(fmt.Sprintf("System Instructions: %s\n", data.SystemPrompt))
	}

	prompt.WriteString(fmt.Sprintf("Prompt: <START>###%s: %s\n###%s: ", requester, message, data.Name))

	return prompt.String(), nil
}

func (p *Processor) getAudioLength(ctx context.Context, data []byte) (time.Duration, error) {
	res, err := p.ffmpeg.Ffprobe(ctx, data)
	if err != nil {
		return 0, fmt.Errorf("error getting audio length: %w", err)
	}

	return res.Duration, nil
}

func (p *Processor) alignTextToAudio(ctx context.Context, text string, audio []byte) ([]whisperx.Timiing, error) {
	audioLen, err := p.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
	}

	timings, err := p.whisper.Align(ctx, text, audio, audioLen)
	if err != nil {
		return nil, fmt.Errorf("error aligning text to audio: %w", err)
	}

	if len(timings) == 0 {
		timings = append(timings, whisperx.Timiing{
			Start: 0,
			End:   audioLen,
			Text:  text,
		})
	}

	for _, timing := range timings {
		if timing.End > audioLen {
			timing.End = audioLen
		}
	}

	timings[len(timings)-1].End = audioLen

	return timings, nil
}
