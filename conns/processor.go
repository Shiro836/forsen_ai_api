package conns

import (
	"context"
	"errors"
)

type Processor interface {
	Process(ctx context.Context, user string) error
}

var ErrProcessingEnd = errors.New("end of processing")
