package processor

// use go interpreter

import (
	"app/db"
	"app/internal/app/conns"
	"app/internal/app/processor/scripts"
	"app/pkg/ai"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

type DB interface {
	GetGoScript(ctx context.Context, userID int) string
	GetCharCard(ctx context.Context, userID int, twitchRewardID string) (*db.Card, error)
	GetNextMsg(ctx context.Context, userID int) (*db.Message, error)
	GetFilters(ctx context.Context, userID int) (string, error)
}

type Processor struct {
	logger *slog.Logger

	llm      *ai.VLLMClient
	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	rvc      *ai.RVCClient
	whisper  *ai.WhisperClient

	db DB

	skippedMsgs     map[int]struct{}
	skippedMsgsLock sync.RWMutex
}

func NewProcessor(logger *slog.Logger, llm *ai.VLLMClient, styleTts *ai.StyleTTSClient, metaTts *ai.MetaTTSClient, rvc *ai.RVCClient, whisper *ai.WhisperClient, db DB) *Processor {
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

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster *db.User) (err error) {
	ctx, cancel := context.WithCancel(ctx)

	logger := p.logger.With("user", broadcaster.TwitchLogin)

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte("start"),
	})

	defer func() {
		cancel()
		if r := recover(); r != nil {
			stack := string(debug.Stack())

			if err != nil {
				err = fmt.Errorf("%w: %s", err, stack)
			} else {
				err = fmt.Errorf("paniced in Process: %s", stack)
			}

			logger.Error("connection panic", "user", broadcaster, "r", r, "stack", stack, "err", err)
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

					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeText,
						EventData: []byte(" "),
					})
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeImage,
						EventData: []byte(""),
					})
				}
			case <-ctx.Done():
				break loop
			}
		}
	}()

	interpreter := interp.New(interp.Options{})
	if err := interpreter.Use(stdlib.Symbols); err != nil {
		return fmt.Errorf("failed to use stdlib: %w", err)
	}

	_, err = interpreter.Eval(p.db.GetGoScript(ctx, broadcaster.ID))
	if err != nil {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeError,
			EventData: []byte(err.Error()),
		})
		return fmt.Errorf("failed to eval script: %w", err)
	}

	v, err := interpreter.Eval("Process")
	if err != nil {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeError,
			EventData: []byte(err.Error()),
		})
		return fmt.Errorf("failed to eval main: %w", err)
	}

	process, ok := v.Interface().(func(context.Context, scripts.AppAPI) error)
	if !ok {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeError,
			EventData: []byte(err.Error()),
		})
		return fmt.Errorf("failed to cast Process: %w", err)
	}

	callLayer := &appApiImpl{
		broadcaster: broadcaster,

		llm:      p.llm,
		styleTts: p.styleTts,
		metaTts:  p.metaTts,
		rvc:      p.rvc,
		whisper:  p.whisper,
	}

	err = process(ctx, callLayer)
	if err != nil {
		return fmt.Errorf("process err: %w", err)
	}

	return nil
}
