package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/llm"
	"app/pkg/s3client"

	"github.com/google/uuid"
)

type AIHandler struct {
	logger   *slog.Logger
	llmModel LLMClient
	imageLlm LLMClient
	db       *db.DB
	s3       *s3client.Client
	service  *Service
}

func NewAIHandler(logger *slog.Logger, llmModel LLMClient, imageLlm LLMClient, db *db.DB, s3 *s3client.Client, service *Service) *AIHandler {
	return &AIHandler{
		logger:   logger,
		llmModel: llmModel,
		imageLlm: imageLlm,
		db:       db,
		s3:       s3,
		service:  service,
	}
}

func (h *AIHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "AI", "requester", input.Requester)

	// Increment AI redeems for the character
	if input.Character != nil {
		if err := h.db.IncrementCharRedeems(ctx, input.Character.ID); err != nil {
			logger.Warn("failed to increment ai redeems", "err", err)
		}
	}

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

	// Fetch message data to see if there are images
	msg, err := h.db.GetMessageByID(ctx, msgID)
	if err != nil {
		logger.Warn("failed to get message by id", "err", err)
		// Proceed without images if fail
	}

	var imageIDs []string
	showImages := false
	updatedMessage := input.Message

	if msg != nil {
		if msgData, err := db.ParseMessageData(msg.Data); err == nil {
			imageIDs = msgData.ImageIDs
			showImages = msgData.ShowImages != nil && *msgData.ShowImages
			logger.Info("parsed image ids from message data", "ids", imageIDs)
		}
	}

	imageAnalysisDone := make(chan struct{})
	if len(imageIDs) == 0 || h.s3 == nil || h.imageLlm == nil {
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
					obj, err := h.s3.GetObject(ctx, s3client.UserImagesBucket, id)
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

					messages := []llm.Message{
						{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: "."}}},
						{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: `Describe the image as if you are a witty streamer's AI co-host in a witty, playful style while staying in third person. 
Do not use first-person expressions like "I" or "we," and avoid conversational greetings. 
The description should read like a clever commentary, not like someone talking about themselves.  in 4 to 20 sentences. No markdown.`}}},
					}
					analysis, err := h.imageLlm.AskMessages(ctx, messages, []llm.Attachment{{Data: data, ContentType: "image/png"}})
					if err != nil || len(analysis) == 0 {
						logger.Warn("image analysis failed", "id", id, "err", err)
						resultsCh <- res{id: id, ok: false}
						return
					}
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
			}
			updatedMessage = localMsg
		}(updatedMessage, imageIDs)
	}

	var llmResult string
	var llmResultErr error
	llmResultDone := make(chan struct{})

	if input.Character != nil {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte("/characters/" + input.Character.ID.String() + "/image"),
		})
	}

	imageIDsBytes, err := json.Marshal(&conns.PromptImages{
		ImageIDs:   imageIDs,
		ShowImages: &showImages,
	})
	if err == nil {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypePromptImage,
			EventData: imageIDsBytes,
		})
	}

	ttsUserMsg := replaceImageTagsForTTS(input.Message)
	requestText := input.Requester + " asked me: " + ttsUserMsg
	filteredRequestText := h.service.FilterText(ctx, input.UserSettings, requestText)

	requestAudio, textTimings, err := h.service.TTSWithTimings(ctx, filteredRequestText, input.Character.Data.VoiceReference)
	if err != nil {
		return err
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	requestTtsDone, err := h.service.playTTS(ctx, logger, eventWriter, filteredRequestText, msgID, requestAudio, textTimings, input.State, input.UserSettings)
	if err != nil {
		return err
	}

	select {
	case <-imageAnalysisDone:
	case <-ctx.Done():
		return nil
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	prompt, err := h.service.craftPrompt(input.Character, input.Requester, updatedMessage)
	if err != nil {
		return err
	}

	go func() {
		defer close(llmResultDone)
		llmResult, llmResultErr = h.llmModel.Ask(ctx, prompt)
	}()

	select {
	case <-llmResultDone:
		if llmResultErr != nil {
			return llmResultErr
		}
		if len(llmResult) == 0 {
			llmResult = "empty response"
		}
		h.db.UpdateMessageData(ctx, msgID, &db.MessageData{AIResponse: llmResult})
		h.service.connManager.NotifyControlPanel(input.Character.OwnerUserID) // Assuming UserID is available or we use broadcaster ID
	case <-ctx.Done():
		return nil
	}

	// llmResult = unidecode.Unidecode(llmResult)
	filteredResponse := h.service.FilterText(ctx, input.UserSettings, llmResult)

	responseTtsAudio, textTimings, err := h.service.TTSWithTimings(ctx, filteredResponse, input.Character.Data.VoiceReference)
	if err != nil {
		return err
	}

	select {
	case <-requestTtsDone:
	case <-ctx.Done():
		return nil
	}

	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
		return nil
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	responseTtsDone, err := h.service.playTTS(ctx, logger, eventWriter, filteredResponse, msgID, responseTtsAudio, textTimings, input.State, input.UserSettings)
	if err != nil {
		return err
	}

	select {
	case <-responseTtsDone:
	case <-ctx.Done():
		return nil
	}

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte(" "),
	})

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(" "),
	})

	return nil
}
