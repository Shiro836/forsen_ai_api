package conns

import (
	"context"
	"errors"
)

type EventWriter func(event *DataEvent) bool

const (
	RestartProcessor UpdateType = iota
	SkipMessage
)

type UpdateType int

type Update struct {
	UpdateType UpdateType
	Data       string
}

type Processor interface {
	Process(ctx context.Context, updates chan *Update, eventWriter EventWriter, user string) error
}

var ErrProcessingEnd = errors.New("end of processing")
var ErrNoUser = errors.New("no user")
