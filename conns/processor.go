package conns

import (
	"context"
	"errors"
)

type EventWriter func(event *DataEvent) bool

type Processor interface {
	Process(ctx context.Context, updates chan struct{}, eventWriter EventWriter, user string) error
}

var ErrProcessingEnd = errors.New("end of processing")
