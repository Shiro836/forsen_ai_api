package processor

// use go interpreter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"app/pkg/s3client"
	"app/pkg/twitch"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

type Processor struct {
	logger *slog.Logger

	llmModel *llm.Client
	imageLlm *llm.Client
	styleTts *ai.StyleTTSClient
	whisper  *whisperx.Client

	db *db.DB

	ffmpeg *ffmpeg.Client

	s3 *s3client.Client

	controlPanelNotifications *notifications.Client
}

func NewProcessor(logger *slog.Logger, llmModel *llm.Client, imageLlm *llm.Client, styleTts *ai.StyleTTSClient, whisper *whisperx.Client, db *db.DB, ffmpeg *ffmpeg.Client, controlPanelNotifications *notifications.Client, s3 *s3client.Client) *Processor {
	return &Processor{
		llmModel: llmModel,
		imageLlm: imageLlm,
		styleTts: styleTts,
		whisper:  whisper,

		logger: logger,

		db: db,

		ffmpeg: ffmpeg,

		s3: s3,

		controlPanelNotifications: controlPanelNotifications,
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
		// EventData: []byte("processor started"),
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

					p.controlPanelNotifications.Notify(broadcaster.ID)

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

					p.controlPanelNotifications.Notify(broadcaster.ID)
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

					p.controlPanelNotifications.Notify(broadcaster.ID)
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

			// Extract up to two image ids and store with the message on insert
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
			if db.ErrCode(err) != db.ErrCodeNoRows {
				logger.Error("error getting card by twitch reward", "err", err)
			}
			continue
		}

		if rewardType == db.TwitchRewardTTS {
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeImage,
				EventData: []byte("/characters/" + charCard.ID.String() + "/image"),
			})

			// Replace any <img:{id}> tags with image_1, image_2 for TTS readability
			ttsMsg := replaceImageTagsForTTS(msg.TwitchMessage.Message)
			filteredRequest := p.FilterText(ctx, broadcaster.ID, ttsMsg)

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

			requestTtsDone, err := p.playTTS(ctx, eventWriter, filteredRequest, msg.ID, requestAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock)
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
						obj, err := p.s3.GetObject(ctx, id)
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

		// For TTS of the incoming request, replace inline image tags with placeholders
		ttsUserMsg := replaceImageTagsForTTS(msg.TwitchMessage.Message)
		requestText := requester + " asked me: " + ttsUserMsg

		filteredRequestText := p.FilterText(ctx, broadcaster.ID, requestText)

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

		requestTtsDone, err := p.playTTS(ctx, eventWriter, filteredRequestText, msg.ID, requestAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock)
		if err != nil {
			fmt.Println(err)
			logger.Error("error playing tts", "err", err)
			return err
		}

		// Wait for image analysis to complete, then craft prompt and start text LLM while TTS plays
		select {
		case <-imageAnalysisDone:
			// proceed with updatedMessage (may be unchanged if no images or analysis failed)
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
			p.controlPanelNotifications.Notify(broadcaster.ID)

			logger.Info("llm result", "result", llmResult)
		case <-ctx.Done():
			return nil
		}

		filteredResponse := p.FilterText(ctx, broadcaster.ID, llmResult)

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

		responseTtsDone, err := p.playTTS(ctx, eventWriter, filteredResponse, msg.ID, responseTtsAudio, textTimings, &skippedMsgIDs, &skippedMsgIDsLock)
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

// replaceImageTagsForTTS converts <img:{id}> occurrences to sequential placeholders image_1, image_2 in order of appearance
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

func (p *Processor) playTTS(ctx context.Context, eventWriter conns.EventWriter, msg string, msdID uuid.UUID, audio []byte, textTimings []whisperx.Timiing, skippedMsgIDs *map[uuid.UUID]struct{}, skippedMsgIDsLock *sync.Mutex) (<-chan struct{}, error) {
	if textTimings == nil {
		var err error

		textTimings, err = p.alignTextToAudio(ctx, msg, audio)
		if err != nil {
			return nil, fmt.Errorf("error aligning text to audio: %w", err)
		}
	}

	audioLen, err := p.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
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

	mp3Audio, err := p.ffmpeg.Ffmpeg2Mp3(ctx, audio)
	if err == nil {
		audio = mp3Audio
	} else {
		p.logger.Error("error converting audio to mp3", "err", err)
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
