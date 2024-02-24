package processor

// use go interpreter

import (
	"app/ai_clients/llm"
	"app/ai_clients/rvc"
	"app/ai_clients/tts"
	"app/conns"
	"app/db"
	"app/pkg/char"
	"app/pkg/slg"
	"app/processor/scripts"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

type DB interface {
	GetGoScript(ctx context.Context, user string) string
	GetCharCard(ctx context.Context, user string, charName string) (*char.Card, error)
	GetNextMsg(ctx context.Context, userID int) (*db.Message, error)
}

type Processor struct {
	logger *slog.Logger

	rvc *rvc.Client
	llm *llm.Client
	tts *tts.Client

	db DB
}

func NewProcessor(logger *slog.Logger, llm *llm.Client, tts *tts.Client, rvc *rvc.Client, db DB) *Processor {
	return &Processor{
		rvc: rvc,
		llm: llm,
		tts: tts,

		logger: logger,

		db: db,
	}
}

func (p *Processor) Process(ctx context.Context, updates chan *conns.Update, eventWriter conns.EventWriter, broadcaster string) (err error) {
	ctx, cancel := context.WithCancel(ctx)

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

			slg.GetSlog(ctx).Error("connection panic", "user", broadcaster, "r", r, "stack", stack, "err", err)
		}
	}()

	go func() {
		// TODO: react to updates
	}()

	broadcasterUserData, err := db.GetUserData(broadcaster)
	if err != nil {
		return conns.ErrNoUser
	}

	interpreter := interp.New(interp.Options{})
	if err := interpreter.Use(stdlib.Symbols); err != nil {
		return fmt.Errorf("failed to use stdlib: %w", err)
	}

	_, err = interpreter.Eval(p.db.GetGoScript(ctx, broadcaster))
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

	process, ok := v.Interface().(func(context.Context, scripts.CallLayer) error)
	if !ok {
		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeError,
			EventData: []byte(err.Error()),
		})
		return fmt.Errorf("failed to cast Process: %w", err)
	}

	callLayer := &callLayerImpl{
		broadcaster: broadcasterUserData,

		rvc: p.rvc,
		llm: p.llm,
		tts: p.tts,
	}

	err = process(ctx, callLayer)
	if err != nil {
		return fmt.Errorf("process err: %w", err)
	}

	return nil
}
