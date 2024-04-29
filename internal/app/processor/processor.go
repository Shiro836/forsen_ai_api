package processor

// use go interpreter

import (
	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/swearfilter"
	"app/pkg/tools"
	"app/pkg/twitch"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Processor struct {
	logger *slog.Logger

	llm      *ai.VLLMClient
	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	rvc      *ai.RVCClient
	whisper  *ai.WhisperClient

	db *db.DB

	skippedMsgs     map[int]struct{}
	skippedMsgsLock sync.RWMutex
}

func NewProcessor(logger *slog.Logger, llm *ai.VLLMClient, styleTts *ai.StyleTTSClient, metaTts *ai.MetaTTSClient, rvc *ai.RVCClient, whisper *ai.WhisperClient, db *db.DB) *Processor {
	return &Processor{
		llm:      llm,
		styleTts: styleTts,
		metaTts:  metaTts,
		rvc:      rvc,
		whisper:  whisper,

		logger: logger,

		db: db,
	}
}

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster *db.User) (globalErr error) {
	ctx, cancel := context.WithCancel(ctx)

	logger := p.logger.With("user", broadcaster.TwitchLogin)

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte("processor started"),
	})

	defer func() {
		cancel()
		if r := recover(); r != nil {
			stack := string(debug.Stack())

			if globalErr != nil {
				globalErr = fmt.Errorf("%w: %s", globalErr, stack)
			} else {
				globalErr = fmt.Errorf("paniced in Process: %s", stack)
			}

			logger.Error("connection panic", "user", broadcaster, "r", r, "stack", stack, "err", globalErr)
		}
	}()

	go func() {

	loop:
		for {
			select {
			case upd, ok := <-updates:
				if !ok {
					updates = nil
					cancel()
				}

				logger.Info("processor signal recieved", "upd_signal", upd)
				switch upd.UpdateType {
				case conns.RestartProcessor:
					cancel()
					break loop
				case conns.SkipMessage:
					msgID, err := strconv.Atoi(upd.Data)
					if err != nil {
						logger.Error("msg id is not integer", "err", err)
					}

					func() {
						p.skippedMsgsLock.Lock()
						defer p.skippedMsgsLock.Unlock()

						p.skippedMsgs[msgID] = struct{}{}
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

			if err := p.db.PushMsg(ctx, broadcaster.ID, db.TwitchMessage{
				TwitchLogin: msg.TwitchLogin,
				Message:     msg.Message,
				RewardID:    msg.RewardID,
			}); err != nil {
				logger.Error("error pushing message to db", "err", err)
			}
		}
	}()

	for {
		var msg *db.Message
		msg, globalErr = p.db.GetNextMsg(ctx, broadcaster.ID)
		if globalErr != nil {
			return
		}

		if len(msg.TwitchMessage.RewardID) == 0 {
			continue
		}

		charCard, err := p.db.GetCharCardByTwitchReward(ctx, broadcaster.ID, msg.TwitchMessage.RewardID)
		if err != nil {
			logger.Error("error getting card by twitch reward", "err", globalErr)
			continue
		}

		requester := msg.TwitchMessage.TwitchLogin

		prompt, err := p.craftPrompt(ctx, charCard, requester, msg.TwitchMessage.Message)
		if err != nil {
			logger.Error("error crafting prompt", "err", globalErr)
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
			return
		}

		requestTtsDone := p.playTTS(ctx, eventWriter, requestText, requestAudio)

		select {
		case <-llmResultDone:
			if llmResultErr != nil {
				logger.Error("error asking llm", "err", llmResultErr)
				continue
			}
		case <-ctx.Done():
			return
		}

		responseTtsAudio, err := p.TTS(ctx, llmResult, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			continue
		}

		select {
		case <-requestTtsDone:
		case <-ctx.Done():
			return
		}

		responseTtsDone := p.playTTS(ctx, eventWriter, llmResult, responseTtsAudio)

		select {
		case <-responseTtsDone:
		case <-ctx.Done():
			return
		}
	}
}

func (p *Processor) playTTS(ctx context.Context, eventWriter conns.EventWriter, msg string, audio []byte) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		defer close(done)

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: audio,
		})

		startTS := time.Now()

		textTimings := alignTextToAudio(msg, audio)

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
	}()

	return done
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

func getAudioLength(data []byte) time.Duration {
	panic("implement me")
}

type timing struct {
	Text  string
	Start time.Duration
	End   time.Duration
}

func alignTextToAudio(text string, audio []byte) []timing {
	return []timing{ // TODO: use https://github.com/Shiro836/whisperX-api to align text to audio
		{
			Text:  text,
			Start: 0,
			End:   getAudioLength(audio),
		},
	}
}
