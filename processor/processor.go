package processor

// use go interpreter

import (
	"app/ai"
	"app/char"
	"app/conns"
	"app/db"
	"app/processor/scripts"
	"app/rvc"
	"app/slg"
	"app/tts"
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
	rvc *rvc.Client
	ai  *ai.Client
	tts *tts.Client

	logger *slog.Logger

	db DB
}

func NewProcessor(logger *slog.Logger, ai *ai.Client, tts *tts.Client, rvc *rvc.Client, db DB) *Processor {
	return &Processor{
		rvc: rvc,
		ai:  ai,
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

			err = fmt.Errorf("paniced in Process: %s", stack)
			slg.GetSlog(ctx).Error("connection panic", "user", broadcaster, "r", r, "stack", stack)
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
	interpreter.Use(stdlib.Symbols)

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
		ai:  p.ai,
		tts: p.tts,
	}

	err = process(ctx, callLayer)
	if err != nil {
		return fmt.Errorf("process err: %w", err)
	}

	return nil
}
