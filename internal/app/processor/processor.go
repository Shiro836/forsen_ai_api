package processor

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/s3client"
	ttsprocessor "app/pkg/tts_processor"
	"app/pkg/twitch"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

//go:embed sfx/*.mp3
var embeddedSFX embed.FS

const defaultTtsLimitSeconds = 80

type Processor struct {
	logger *slog.Logger

	llmModel *llm.Client
	imageLlm *llm.Client
	styleTts *ai.StyleTTSClient
	whisper  *whisperx.Client

	db *db.DB

	ffmpeg *ffmpeg.Client

	s3 *s3client.Client

	connManager *conns.Manager
}

func NewProcessor(logger *slog.Logger, llmModel *llm.Client, imageLlm *llm.Client, styleTts *ai.StyleTTSClient, whisper *whisperx.Client, db *db.DB, ffmpeg *ffmpeg.Client, connManager *conns.Manager, s3 *s3client.Client) *Processor {
	return &Processor{
		llmModel: llmModel,
		imageLlm: imageLlm,
		styleTts: styleTts,
		whisper:  whisper,

		logger: logger,

		db: db,

		ffmpeg: ffmpeg,

		s3: s3,

		connManager: connManager,
	}
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

	skippedMsgIDs := make(map[uuid.UUID]struct{})
	skippedMsgIDsLock := sync.Mutex{}

	go func() {
		defer cancel()

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

					p.connManager.NotifyControlPanel(broadcaster.ID)

				case conns.ShowImages:
					msgID, err := uuid.Parse(upd.Data)
					if err != nil {
						logger.Error("msg id is not valid uuid", "err", err)
						continue
					}

					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeShowImages,
						EventData: []byte(upd.Data),
					})

					showImages := true

					p.db.UpdateMessageData(ctx, msgID, &db.MessageData{ShowImages: &showImages})

					p.connManager.NotifyControlPanel(broadcaster.ID)
				case conns.HideImages:
					msgID, err := uuid.Parse(upd.Data)
					if err != nil {
						logger.Error("msg id is not valid uuid", "err", err)
						continue
					}

					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeHideImages,
						EventData: []byte(upd.Data),
					})

					showImages := false

					p.db.UpdateMessageData(ctx, msgID, &db.MessageData{ShowImages: &showImages})

					p.connManager.NotifyControlPanel(broadcaster.ID)
				case conns.CleanOverlay:
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeText,
						EventData: []byte(" "),
					})
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeImage,
						EventData: []byte(" "),
					})
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		defer cancel()

		twitchChatCh := twitch.MessagesFetcher(ctx, broadcaster.TwitchLogin, true)

		for msg := range twitchChatCh {
			if len(msg.Message) == 0 || len(msg.TwitchLogin) == 0 {
				continue
			}

			if len(msg.RewardID) == 0 {
				continue
			}

			imgMatches := regexp.MustCompile(`<img:([A-Za-z0-9]{5})>`).FindAllStringSubmatch(msg.Message, -1)
			imageIDs := make([]string, 0, 2)
			for _, m := range imgMatches {
				if len(m) >= 2 {
					imageIDs = append(imageIDs, m[1])
					if len(imageIDs) == 2 {
						break
					}
				}
			}

			// logger.Info("ingest twitch msg", "from", msg.TwitchLogin, "reward", msg.RewardID)

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
	}()

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

		userSettings, err := p.db.GetUserSettings(ctx, broadcaster.ID)
		if err != nil {
			logger.Warn("failed to get user settings, using defaults", "err", err)
			userSettings = &db.UserSettings{} // Use empty settings with defaults
		}

		skipMsg := false

		if err := p.db.UpdateMessageStatus(ctx, msg.ID, db.MsgStatusCurrent); err != nil {
			logger.Error("error updating message status", "err", err)

			return fmt.Errorf("error updating message status: %w", err)
		}

		p.connManager.NotifyControlPanel(broadcaster.ID)

		if len(msg.TwitchMessage.RewardID) == 0 {
			continue
		}

		// Try to get any reward (character or universal) by Twitch reward ID
		cardID, rewardType, err := p.db.GetRewardByTwitchReward(ctx, broadcaster.ID, msg.TwitchMessage.RewardID)
		if err != nil {
			if db.ErrCode(err) != db.ErrCodeNoRows {
				logger.Error("error getting reward by twitch reward", "err", err)
			}
			continue
		}

		// Get character card if this is a character-based reward
		var charCard *db.Card
		if cardID != nil {
			charCard, err = p.db.GetCharCardByID(ctx, broadcaster.ID, *cardID)
			if err != nil {
				logger.Error("error getting character card", "err", err)
				continue
			}
		}

		if rewardType == db.TwitchRewardTTS {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeImage,
				EventData: []byte("/characters/" + charCard.ID.String() + "/image"),
			})

			// Replace any <img:{id}> tags with image_1, image_2 for TTS readability
			ttsMsg := replaceImageTagsForTTS(msg.TwitchMessage.Message)
			filteredRequest := p.FilterText(ctx, userSettings, ttsMsg)

			requestAudio, textTimings, err := p.TTSWithTimings(ctx, filteredRequest, charCard.Data.VoiceReference)
			if err != nil {
				logger.Error("error tts", "err", err)
				return err
			}

			func() {
				skippedMsgIDsLock.Lock()
				defer skippedMsgIDsLock.Unlock()

				_, skipMsg = skippedMsgIDs[msg.ID]
			}()

			if skipMsg {
				continue
			}

			requestTtsDone, err := p.playTTS(ctx, logger, eventWriter, filteredRequest, msg.ID, requestAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock, userSettings)
			if err != nil {
				fmt.Println(err)
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

		if rewardType == db.TwitchRewardUniversalTTS {
			// Universal TTS processing using tts_processor library
			ttsMsg := replaceImageTagsForTTS(msg.TwitchMessage.Message)
			filteredRequest := p.FilterText(ctx, userSettings, ttsMsg)

			// Process the message with tts_processor
			actions, err := p.processUniversalTTSMessage(ctx, filteredRequest)
			if err != nil {
				logger.Error("error processing universal tts message", "err", err)
				continue
			}

			func() {
				skippedMsgIDsLock.Lock()
				defer skippedMsgIDsLock.Unlock()

				_, skipMsg = skippedMsgIDs[msg.ID]
			}()

			if skipMsg {
				continue
			}

			requestTtsDone, err := p.playUniversalTTS(ctx, logger, eventWriter, actions, msg.ID, &skippedMsgIDs, &skippedMsgIDsLock, userSettings)
			if err != nil {
				fmt.Println(err)
				logger.Error("error playing universal tts", "err", err)
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

		updatedMessage := msg.TwitchMessage.Message

		var imageIDs []string
		showImages := false

		if msgData, err := db.ParseMessageData(msg.Data); err == nil {
			imageIDs = msgData.ImageIDs
			showImages = msgData.ShowImages != nil && *msgData.ShowImages

			logger.Info("parsed image ids from message data", "ids", imageIDs)
		} else {
			logger.Warn("failed to parse message data for image ids", "err", err)
		}

		imageAnalysisDone := make(chan struct{})
		if len(imageIDs) == 0 || p.s3 == nil || p.imageLlm == nil {
			// Nothing to analyze; keep updatedMessage as original and signal done
			close(imageAnalysisDone)
		} else {
			go func(origMsg string, ids []string) {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("panic in image analysis goroutine", "r", r)
						updatedMessage = origMsg
					}
					close(imageAnalysisDone)
				}()

				localMsg := origMsg
				replacedCount := 0

				var wg sync.WaitGroup
				type res struct {
					id       string
					analysis string
					ok       bool
				}
				resultsCh := make(chan res, len(ids))

				for _, id := range ids {
					id := id
					wg.Add(1)
					go func() {
						defer wg.Done()
						// Fetch image bytes
						obj, err := p.s3.GetObject(ctx, s3client.UserImagesBucket, id)
						if err != nil {
							logger.Warn("failed to fetch image from s3", "id", id, "err", err)
							resultsCh <- res{id: id, ok: false}
							return
						}
						data, err := io.ReadAll(obj)
						obj.Close()
						if err != nil {
							logger.Warn("failed to read image from s3", "id", id, "err", err)
							resultsCh <- res{id: id, ok: false}
							return
						}
						logger.Info("fetched image for analysis", "id", id, "bytes", len(data))

						messages := []llm.Message{
							{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: "."}}},
							{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: `Describe the imag as if you are a witty streamer's AI co-host in a witty, playful style while staying in third person. 
Do not use first-person expressions like "I" or "we," and avoid conversational greetings. 
The description should read like a clever commentary, not like someone talking about themselves.  in 4 to 20 sentences. No markdown.`}}},
						}
						analysis, err := p.imageLlm.AskMessages(ctx, messages, []llm.Attachment{{Data: data, ContentType: "image/png"}})
						if err != nil || len(analysis) == 0 {
							logger.Warn("image analysis failed", "id", id, "err", err)
							resultsCh <- res{id: id, ok: false}
							return
						}
						logger.Info("image analysis", "id", id, "analysis", analysis)
						resultsCh <- res{id: id, analysis: analysis, ok: true}
					}()
				}

				go func() {
					wg.Wait()
					close(resultsCh)
				}()

				for r := range resultsCh {
					if !r.ok {
						localMsg = strings.Replace(localMsg, "<img:"+r.id+">", "", 1)
						continue
					}
					wrapped := "<image_" + r.id + ":{" + r.analysis + "}>"
					localMsg = strings.Replace(localMsg, "<img:"+r.id+">", wrapped, 1)
					replacedCount++
					logger.Info("replaced image tag with analysis", "id", r.id, "analysis_len", len(r.analysis))
				}

				logger.Info("inline image replacements done", "count", replacedCount)
				updatedMessage = localMsg
			}(updatedMessage, imageIDs)
		}

		var llmResult string
		var llmResultErr error
		llmResultDone := make(chan struct{})

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte("/characters/" + charCard.ID.String() + "/image"),
		})

		imageIDsBytes, err := json.Marshal(&conns.PromptImages{
			ImageIDs:   imageIDs,
			ShowImages: &showImages,
		})
		if err != nil {
			logger.Error("failed to marshal image ids", "err", err)
		} else {
			logger.Info("sending prompt image", "prompt_image", string(imageIDsBytes))
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypePromptImage,
				EventData: imageIDsBytes,
			})
		}

		ttsUserMsg := replaceImageTagsForTTS(msg.TwitchMessage.Message)
		requestText := requester + " asked me: " + ttsUserMsg

		filteredRequestText := p.FilterText(ctx, userSettings, requestText)

		requestAudio, textTimings, err := p.TTSWithTimings(ctx, filteredRequestText, charCard.Data.VoiceReference)
		if err != nil {
			logger.Error("error tts", "err", err)
			return err
		}

		func() {
			skippedMsgIDsLock.Lock()
			defer skippedMsgIDsLock.Unlock()

			_, skipMsg = skippedMsgIDs[msg.ID]
		}()

		if skipMsg {
			continue
		}

		requestTtsDone, err := p.playTTS(ctx, logger, eventWriter, filteredRequestText, msg.ID, requestAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock, userSettings)
		if err != nil {
			fmt.Println(err)
			logger.Error("error playing tts", "err", err)
			return err
		}

		select {
		case <-imageAnalysisDone:
		case <-ctx.Done():
			return nil
		}

		prompt, err := p.craftPrompt(charCard, requester, updatedMessage)
		if err != nil {
			logger.Error("error crafting prompt", "err", err)
			continue
		}

		go func() {
			defer close(llmResultDone)
			llmResult, llmResultErr = p.llmModel.Ask(ctx, prompt)
		}()

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
			p.connManager.NotifyControlPanel(broadcaster.ID)

			logger.Info("llm result", "result", llmResult)
		case <-ctx.Done():
			return nil
		}

		filteredResponse := p.FilterText(ctx, userSettings, llmResult)

		responseTtsAudio, textTimings, err := p.TTSWithTimings(ctx, filteredResponse, charCard.Data.VoiceReference)
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

			_, skipMsg = skippedMsgIDs[msg.ID]
		}()

		if skipMsg {
			continue
		}

		responseTtsDone, err := p.playTTS(ctx, logger, eventWriter, filteredResponse, msg.ID, responseTtsAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock, userSettings)
		if err != nil {
			fmt.Println(err)
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

func replaceImageTagsForTTS(s string) string {
	re := regexp.MustCompile(`<img:([A-Za-z0-9]{5})>`)
	idx := 0
	return re.ReplaceAllStringFunc(s, func(_ string) string {
		idx++
		return fmt.Sprintf("image_%d", idx)
	})
}

type audioMsg struct {
	Audio []byte `json:"audio"`
	MsgID string `json:"msg_id"`
}

func (p *Processor) playTTS(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, msg string, msdID uuid.UUID, audio []byte, textTimings []whisperx.Timiing, skippedMsgIDs *map[uuid.UUID]struct{}, skippedMsgIDsLock *sync.Mutex, userSettings *db.UserSettings) (<-chan struct{}, error) {
	mp3Audio, err := p.ffmpeg.Ffmpeg2Mp3(ctx, audio)
	if err == nil {
		audio = mp3Audio
	} else {
		p.logger.Error("error converting audio to mp3", "err", err)
	}

	audio, err = p.cutTtsAudio(ctx, logger, userSettings, audio)
	if err != nil {
		p.logger.Warn("failed to cut TTS audio", "err", err)
	}

	audioLen, err := p.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
	}

	if textTimings == nil {
		textTimings = append(textTimings, whisperx.Timiing{
			Text:  msg,
			Start: 0,
			End:   audioLen,
		})
		// var err error

		// textTimings, err = p.alignTextToAudio(ctx, msg, audio)
		// if err != nil {
		// 	return nil, fmt.Errorf("error aligning text to audio: %w", err)
		// }
	}

	if len(textTimings) == 0 {
		textTimings = append(textTimings, whisperx.Timiing{
			Text:  msg,
			Start: 0,
			End:   audioLen,
		})
	} else {
		textTimings[len(textTimings)-1].End = audioLen
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		// Check skip before sending any audio to the client
		var skipBeforeSend bool
		func() {
			skippedMsgIDsLock.Lock()
			defer skippedMsgIDsLock.Unlock()
			_, skipBeforeSend = (*skippedMsgIDs)[msdID]
		}()
		if skipBeforeSend {
			return
		}

		audioMsg, err := json.Marshal(&audioMsg{
			Audio: audio,
			MsgID: msdID.String(),
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

			var skipMsg bool
			func() {
				skippedMsgIDsLock.Lock()
				defer skippedMsgIDsLock.Unlock()

				_, skipMsg = (*skippedMsgIDs)[msdID]
			}()

			if skipMsg {
				break
			}

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

func (p *Processor) TTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error) {
	ttsResult, ttsSegments, err := p.styleTts.TTS(ctx, msg, refAudio)
	if err != nil {
		return nil, nil, err
	}

	return ttsResult, ttsSegments, nil
}

func (p *Processor) FilterText(ctx context.Context, userSettings *db.UserSettings, text string) string {
	swears := GlobalSwears // regex patterns

	if len(userSettings.Filters) != 0 {
		swears = slices.Concat(swears, strings.Split(userSettings.Filters, ","))
	}

	for _, exp := range swears {
		r, err := regexp.Compile("(?i)" + exp) // makes them case-insensitive by default
		if err != nil {
			p.logger.Warn(fmt.Sprintf("failed compiling reg expression '%s'", exp), "err", err)
			continue
		}
		text = r.ReplaceAllString(text, "(filtered)")
	}

	return text
}

// cutTtsAudio cuts audio to the user's TTS limit if it exceeds the limit
func (p *Processor) cutTtsAudio(ctx context.Context, logger *slog.Logger, userSettings *db.UserSettings, audio []byte) ([]byte, error) {
	ttsLimit := userSettings.TtsLimit
	if ttsLimit <= 0 {
		ttsLimit = defaultTtsLimitSeconds
	}

	audioLen, err := p.getAudioLength(ctx, audio)
	if err != nil {
		p.logger.Warn("failed to get audio length for TTS cutting", "err", err)
		return audio, nil
	}

	maxDuration := time.Duration(ttsLimit) * time.Second
	if audioLen <= maxDuration {
		return audio, nil
	}

	logger.Debug("cutting TTS audio to fit limit", "original_duration", audioLen, "max_duration", maxDuration)

	cutAudio, err := p.ffmpeg.CutAudio(ctx, audio, maxDuration)
	if err != nil {
		logger.Warn("failed to cut audio, using original", "err", err)
		return audio, nil
	}

	return cutAudio, nil
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

// func (p *Processor) alignTextToAudio(ctx context.Context, text string, audio []byte) ([]whisperx.Timiing, error) {
// 	audioLen, err := p.getAudioLength(ctx, audio)
// 	if err != nil {
// 		return nil, err
// 	}

// 	timings, err := p.whisper.Align(ctx, text, audio, audioLen)
// 	if err != nil {
// 		return nil, fmt.Errorf("error aligning text to audio: %w", err)
// 	}

// 	if len(timings) == 0 {
// 		timings = append(timings, whisperx.Timiing{
// 			Start: 0,
// 			End:   audioLen,
// 			Text:  text,
// 		})
// 	}

// 	for _, timing := range timings {
// 		if timing.End > audioLen {
// 			timing.End = audioLen
// 		}
// 	}

// 	timings[len(timings)-1].End = audioLen

// 	return timings, nil
// }

func (p *Processor) processUniversalTTSMessage(ctx context.Context, message string) ([]ttsprocessor.Action, error) {
	checkVoice := func(voice string) bool {
		name := strings.TrimSpace(voice)
		if len(name) == 0 {
			return false
		}

		_, _, err := p.db.GetVoiceReferenceByShortName(ctx, name)

		return err == nil
	}

	checkFilter := func(filter string) bool {
		if len(filter) == 0 {
			return false
		}

		v, err := strconv.Atoi(filter)
		if err != nil {
			return false
		}

		return v >= 1 && v < int(ffmpeg.FilterLast)
	}

	checkSfx := func(sfx string) bool {
		name := strings.TrimSpace(sfx)
		if len(name) == 0 {
			return false
		}

		if _, err := embeddedSFX.Open("sfx/" + name + ".mp3"); err != nil {
			return false
		}

		return true
	}

	return ttsprocessor.ProcessMessage(message, checkVoice, checkFilter, checkSfx)
}

func (p *Processor) playUniversalTTS(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, actions []ttsprocessor.Action, msgID uuid.UUID, skippedMsgIDs *map[uuid.UUID]struct{}, skippedMsgIDsLock *sync.Mutex, userSettings *db.UserSettings) (<-chan struct{}, error) {
	combinedAudio, combinedText, combinedTimings, err := p.craftUniversalTTSAudio(ctx, logger, actions, userSettings)
	if err != nil {
		done := make(chan struct{})
		close(done)
		logger.Error("error crafting universal TTS audio", "err", err)
		return done, err
	}

	return p.playTTS(ctx, logger, eventWriter, combinedText, msgID, combinedAudio, combinedTimings, skippedMsgIDs, skippedMsgIDsLock, userSettings)
}

func (p *Processor) craftUniversalTTSAudio(ctx context.Context, logger *slog.Logger, actions []ttsprocessor.Action, userSettings *db.UserSettings) ([]byte, string, []whisperx.Timiing, error) {
	var combinedAudio [][]byte
	var combinedText strings.Builder
	var combinedTimings []whisperx.Timiing
	currentOffset := time.Duration(0)

	concatPadding := 500 * time.Millisecond
	defaultVoice := "tresh"

	ttsLimit := userSettings.TtsLimit
	if ttsLimit <= 0 {
		ttsLimit = defaultTtsLimitSeconds
	}
	maxDuration := time.Duration(ttsLimit) * time.Second

actions_loop:
	for _, action := range actions {
		if action.Text != "" && action.Text != " " {
			voice := action.Voice
			if voice == "" {
				voice = defaultVoice
			}

			_, voiceRef, err := p.getVoiceReference(ctx, logger, voice)
			if err != nil {
				logger.Error("error getting voice reference", "err", err, "voice", action.Voice)
				voiceRef = []byte{}
			}

			audio, timings, err := p.TTSWithTimings(ctx, action.Text, voiceRef)
			if err != nil {
				logger.Error("error generating TTS for universal action", "err", err, "text", action.Text)
				continue
			}

			originalAudioLen, err := p.getAudioLength(ctx, audio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			processedAudio, err := p.applyAudioEffects(ctx, audio, action.Filters...)
			if err != nil {
				logger.Error("error applying audio effects", "err", err, "filters", action.Filters)

				processedAudio = audio
			}

			processedAudioLen, err := p.getAudioLength(ctx, processedAudio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			if originalAudioLen != 0 {
				change := float64(processedAudioLen) / float64(originalAudioLen)

				if change > 1.05 || change < 0.95 {
					for i := range timings {
						timings[i].Start = time.Duration(float64(timings[i].Start) * change)
						timings[i].End = time.Duration(float64(timings[i].End) * change)
					}
				}
			}

			combinedAudio = append(combinedAudio, processedAudio)

			if combinedText.Len() > 0 {
				combinedText.WriteString(" ")
			}
			combinedText.WriteString(action.Text)

			for i := range timings {
				timings[i].Start += currentOffset
				timings[i].End += currentOffset
			}
			combinedTimings = append(combinedTimings, timings...)

			audioLen, err := p.getAudioLength(ctx, processedAudio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			currentOffset += audioLen
			currentOffset += concatPadding

			if currentOffset > maxDuration {
				logger.Info("stopping universal TTS generation due to length limit", "current_duration", currentOffset, "max_duration", maxDuration)
				break actions_loop
			}

			if action.Sfx != "" {
				sfxAudio, err := p.getSFX(action.Sfx)
				if err != nil {
					logger.Error("error generating SFX", "err", err, "sfx", action.Sfx)

					if combinedText.Len() > 0 {
						combinedText.WriteString(" ")
					}
					combinedText.WriteString(fmt.Sprintf("[%s]", action.Sfx))

					continue
				}

				processedSFX, err := p.applyAudioEffects(ctx, sfxAudio, action.Filters...)
				if err != nil {
					logger.Error("error applying audio effects to SFX", "err", err, "filters", action.Filters)

					processedSFX = sfxAudio
				}

				combinedAudio = append(combinedAudio, processedSFX)

				if combinedText.Len() > 0 {
					combinedText.WriteString(" ")
				}
				combinedText.WriteString(fmt.Sprintf("[%s]", action.Sfx))

				sfxLen, err := p.getAudioLength(ctx, processedSFX)
				if err != nil {
					logger.Error("error getting SFX length", "err", err)

					continue
				}

				currentOffset += sfxLen

				if currentOffset > maxDuration {
					logger.Info("stopping universal TTS generation due to length limit", "current_duration", currentOffset, "max_duration", maxDuration)
					break actions_loop
				}
			}
		}
	}

	for i := range combinedTimings {
		combinedTimings[i].Start = min(combinedTimings[i].Start, maxDuration)
		combinedTimings[i].End = min(combinedTimings[i].End, maxDuration)
	}

	if len(combinedAudio) == 0 {
		return nil, "", nil, fmt.Errorf("no audio generated from actions")
	}

	finalAudio, err := p.ffmpeg.ConcatenateAudio(ctx, concatPadding, combinedAudio...)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error concatenating audio: %w", err)
	}

	return finalAudio, combinedText.String(), combinedTimings, nil
}

func (p *Processor) getVoiceReference(ctx context.Context, logger *slog.Logger, voice string) (uuid.UUID, []byte, error) {
	if voice == "" {
		return uuid.Nil, nil, fmt.Errorf("empty voice")
	}
	logger.Debug("voice reference requested", "voice", voice)

	id, card, err := p.db.GetVoiceReferenceByShortName(ctx, voice)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("failed to get voice reference for '%s': %w", voice, err)
	}

	return id, card.VoiceReference, nil
}

func (p *Processor) limitFilters(filters []string) []string {
	const (
		maxPerFilter = 3
		maxTotal     = 15
	)

	if len(filters) == 0 {
		return filters
	}

	filterCounts := make(map[string]int)
	var limitedFilters []string

	for _, filter := range filters {
		if filterCounts[filter] < maxPerFilter {
			filterCounts[filter]++
			limitedFilters = append(limitedFilters, filter)

			if len(limitedFilters) >= maxTotal {
				break
			}
		}
	}

	return limitedFilters
}

func (p *Processor) applyAudioEffects(ctx context.Context, audio []byte, filters ...string) ([]byte, error) {
	if len(filters) == 0 {
		return audio, nil
	}

	limitedFilters := p.limitFilters(filters)

	processedAudio, err := p.ffmpeg.ApplyStringFilters(ctx, audio, limitedFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to apply audio effects: %w", err)
	}

	return processedAudio, nil
}

func (p *Processor) getSFX(sfxName string) ([]byte, error) {
	name := strings.TrimSpace(sfxName)
	if len(name) == 0 {
		return nil, fmt.Errorf("empty sfx name")
	}

	data, err := embeddedSFX.ReadFile("sfx/" + name + ".mp3")
	if err != nil {
		return nil, fmt.Errorf("sfx '%s' not found: %w", sfxName, err)
	}

	return data, nil
}
