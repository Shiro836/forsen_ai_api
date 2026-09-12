package processor

import (
	"app/pkg/tools"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/archive"
	"app/pkg/imagetag"
	"app/pkg/llm"
	"app/pkg/s3client"
	"app/pkg/textfilter"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"app/internal/app/monitoring"
)

type AIHandler struct {
	logger       *slog.Logger
	llmModel     CharacterLLM
	imageLlm     LLMClient
	nativeImages bool
	db           *db.DB
	s3           *s3client.Client
	service      *Service
}

func NewAIHandler(logger *slog.Logger, llmModel CharacterLLM, imageLlm LLMClient, nativeImages bool, db *db.DB, s3 *s3client.Client, service *Service) *AIHandler {
	return &AIHandler{
		logger:       logger,
		llmModel:     llmModel,
		imageLlm:     imageLlm,
		nativeImages: nativeImages,
		db:           db,
		s3:           s3,
		service:      service,
	}
}

func (h *AIHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "AI", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	timer := prometheus.NewTimer(monitoring.AppMetrics.AIQueryTime)
	defer timer.ObserveDuration()

	if input.Character != nil {
		if err := h.db.IncrementCharRedeems(ctx, input.Character.ID); err != nil {
			logger.Warn("failed to increment ai redeems", "err", err)
		}
	}

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

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

	// the snapshot must reflect the DB flag for an overlay that (re)connects
	// mid-message; clicks made while the message was queued only live in the DB
	input.State.SetImageShown(msgID, showImages)

	if len(imageIDs) == 0 {
		if ids := imagetag.ExtractIDs(updatedMessage, 0); len(ids) > 0 {
			imageIDs = ids
			logger.Info("parsed image ids from input message", "ids", imageIDs)
		}
	}

	var attachments []llm.Attachment
	imagesDone := make(chan struct{})
	if len(imageIDs) == 0 || h.s3 == nil || (!h.nativeImages && h.imageLlm == nil) {
		close(imagesDone)
	} else {
		go func(origMsg string, ids []string) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("panic in image processing goroutine", "r", r)
					updatedMessage = origMsg
					attachments = nil
				}
				close(imagesDone)
			}()

			images := h.fetchImages(ctx, logger, ids)
			if h.nativeImages {
				updatedMessage, attachments = attachImages(origMsg, images)
			} else {
				updatedMessage = h.describeImages(ctx, logger, origMsg, images)
			}
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
		MsgID:      input.MsgID,
		ImageIDs:   imageIDs,
		ShowImages: &showImages,
	})
	if err == nil {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypePromptImage,
			EventData: imageIDsBytes,
		})
	}

	skipLLMFilterPerUser := input.UserSettings.DisableLLMFilter
	skipLLMFilter := input.SkipLLMFilterFully || skipLLMFilterPerUser

	ttsUserMsg := imagetag.ReplaceImageTags(input.Message)
	spokenRequestText, requestMap := spokenRequest(input.Requester+" asked me: ", input.Message)
	requestRun, err := h.service.filterSpans(ctx, input.UserSettings, spokenRequestText, skipLLMFilter)
	if err != nil {
		return fmt.Errorf("failed to filter request: %w", err)
	}
	requestSpans := requestRun.Spans()
	filteredRequestText := textfilter.Censor(spokenRequestText, requestSpans, "(filtered)")
	recordFilter(ctx, requestRun, requestMap)

	requesterLen := utf8.RuneCountInString(input.Requester)
	archive.RecordFilter(ctx, archive.FilterRun{
		Target:  archive.FilterTargetRequester,
		Regex:   textfilter.Window(requestRun.Regex, 0, requesterLen),
		LLM:     textfilter.Window(requestRun.LLM, 0, requesterLen),
		Skipped: requestRun.Skipped,
	})

	requestData := db.MessageData{
		RequestFiltered:   requestMap.MapBack(requestSpans),
		RequesterFiltered: textfilter.Window(requestSpans, 0, requesterLen),
	}
	if len(requestData.RequestFiltered) > 0 || len(requestData.RequesterFiltered) > 0 {
		h.db.UpdateMessageData(ctx, msgID, &requestData)
		h.service.connManager.NotifyControlPanel(input.Broadcaster.ID)
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	requestTtsDone, err := h.service.playTTSStreaming(ctx, logger, eventWriter, input.AudioWriter, filteredRequestText, msgID, input.Character.Data.VoiceReference, input.State, input.UserSettings, nil)
	if err != nil {
		return err
	}

	select {
	case <-imagesDone:
	case <-ctx.Done():
		return nil
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	go func() {
		defer tools.LogPanic(logger, "character reply")
		defer close(llmResultDone)
		llmResult, llmResultErr = h.llmModel.CharacterReply(ctx, input.Character, input.Requester, updatedMessage, attachments)
	}()

	select {
	case <-llmResultDone:
		if llmResultErr != nil {
			return llmResultErr
		}
		llmResult = strings.TrimSpace(llmResult)
		if len(llmResult) == 0 {
			llmResult = "empty response"
		}
	case <-ctx.Done():
		return nil
	}

	responseRun, err := h.service.filterReplySpans(ctx, input.UserSettings, ttsUserMsg, llmResult, skipLLMFilter)
	if err != nil {
		return fmt.Errorf("failed to filter response: %w", err)
	}
	recordFilter(ctx, responseRun, nil)
	responseSpans := responseRun.Spans()

	h.db.UpdateMessageData(ctx, msgID, &db.MessageData{AIResponse: llmResult, FilteredText: responseSpans})
	h.service.connManager.NotifyControlPanel(input.Broadcaster.ID)

	filteredResponse := textfilter.Censor(llmResult, responseSpans, "(filtered)")

	// response synthesis starts now, hidden under request playback; emission
	// waits for the request track plus a one-second breather
	responseGate := make(chan struct{})
	go func() {
		defer close(responseGate)
		select {
		case <-requestTtsDone:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}()

	responseTtsDone, err := h.service.playTTSStreaming(ctx, logger, eventWriter, input.AudioWriter, filteredResponse, msgID, input.Character.Data.VoiceReference, input.State, input.UserSettings, responseGate)
	if err != nil {
		return err
	}

	select {
	case <-responseTtsDone:
	case <-ctx.Done():
		return nil
	}

	eventWriter(cleanEvent())

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(" "),
	})

	return nil
}

type fetchedImage struct {
	id   string
	data []byte
}

func (h *AIHandler) fetchImages(ctx context.Context, logger *slog.Logger, ids []string) []fetchedImage {
	images := make([]fetchedImage, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		images[i] = fetchedImage{id: id}
		wg.Add(1)
		go func(i int, id string) {
			defer tools.LogPanic(logger, "image fetch")
			defer wg.Done()
			obj, err := h.s3.GetObject(ctx, s3client.UserImagesBucket, id)
			if err != nil {
				logger.Warn("failed to fetch image from s3", "id", id, "err", err)
				return
			}
			defer obj.Close()
			data, err := io.ReadAll(obj)
			if err != nil {
				logger.Warn("failed to read image from s3", "id", id, "err", err)
				return
			}
			// Attachments are declared image/png, so an unconvertible image is
			// dropped rather than sent mislabelled.
			small, err := imageForLLM(ctx, h.service.ffmpeg, data)
			if err != nil {
				logger.Warn("failed to prepare image for llm, dropping it", "id", id, "err", err)
				return
			}
			images[i].data = small
		}(i, id)
	}
	wg.Wait()
	return images
}

// attachImages prepares the message for a vision-capable character model:
// image tags become "image_N" references and the bytes ride along as
// attachments in the same order.
func attachImages(msg string, images []fetchedImage) (string, []llm.Attachment) {
	attachments := make([]llm.Attachment, 0, len(images))
	for _, img := range images {
		if img.data == nil {
			msg = imagetag.ReplaceID(msg, img.id, "")
			continue
		}
		attachments = append(attachments, llm.Attachment{ID: img.id, Data: img.data, ContentType: "image/png"})
	}
	return imagetag.ReplaceImageTags(msg), attachments
}

// describeImages is the text-only fallback: a separate vision model describes
// each image and the description is injected into the message in place of the
// tag.
func (h *AIHandler) describeImages(ctx context.Context, logger *slog.Logger, msg string, images []fetchedImage) string {
	analyses := make([]string, len(images))
	var wg sync.WaitGroup
	for i, img := range images {
		if img.data == nil {
			continue
		}
		wg.Add(1)
		go func(i int, img fetchedImage) {
			defer tools.LogPanic(logger, "image describe")
			defer wg.Done()
			messages := []llm.Message{
				{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: "."}}},
				{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: `Describe the image as if you are a witty streamer's AI co-host in a witty, playful style while staying in third person.
Do not use first-person expressions like "I" or "we," and avoid conversational greetings.
The description should read like a clever commentary, not like someone talking about themselves.  in 4 to 20 sentences. No markdown.`}}},
			}
			analysis, err := h.imageLlm.AskMessages(archive.WithLLMKind(ctx, "image_describe"), messages, []llm.Attachment{{ID: img.id, Data: img.data, ContentType: "image/png"}})
			if err != nil || len(analysis) == 0 {
				logger.Warn("image analysis failed", "id", img.id, "err", err)
				return
			}
			analyses[i] = analysis
		}(i, img)
	}
	wg.Wait()

	for i, img := range images {
		if analyses[i] == "" {
			msg = imagetag.ReplaceID(msg, img.id, "")
			continue
		}
		msg = imagetag.ReplaceID(msg, img.id, "<image_"+img.id+":{"+analyses[i]+"}>")
	}
	return msg
}
