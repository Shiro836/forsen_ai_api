package conns

import (
	"app/db"
	"context"
	"errors"
)

type EventWriter func(event *DataEvent) bool

// AudioWriter delivers a binary overlay-v2 audio frame (chunk/track_done) to
// whatever sink owns the playback: the user's audio topic for the OBS overlay,
// or a try-page websocket directly.
type AudioWriter func(frame []byte) bool

const (
	RestartProcessor UpdateType = iota
	SkipMessage
	ShowImages
	HideImages
	CleanOverlay
	SkipCurrent
	ShowImagesCurrent
)

type UpdateType int

type Update struct {
	UpdateType UpdateType
	Data       string
	// MsgID is the overlay's audibly playing message for the *Current
	// actions; the processor's own notion of current can run ahead of
	// playback, so the client's report wins when present.
	MsgID string
}

type Processor interface {
	Process(ctx context.Context, updates chan *Update, eventWriter EventWriter, user *db.User) error
}

var ErrProcessingEnd = errors.New("end of processing")
var ErrNoUser = errors.New("no user")
