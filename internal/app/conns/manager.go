package conns

import (
	"app/db"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Manager struct {
	processor Processor

	ctx context.Context

	wg sync.WaitGroup

	rwMutex sync.RWMutex

	// track running user processors and allow targeted cancellation
	runningUsers map[uuid.UUID]bool
	userCancels  map[uuid.UUID]context.CancelFunc

	// watermill in-memory pubsub for per-user data events
	bus *Watermill

	// live overlay state per user, for the connect snapshot; registered by the
	// processor for the lifetime of one Process run
	overlayStates     map[uuid.UUID]OverlayState
	overlayStatesLock sync.RWMutex

	logger *slog.Logger
}

// OverlayState is the processor-owned state a reconnecting overlay needs to
// resynchronize: which messages are skipped and which one is playing.
type OverlayState interface {
	SkippedList() []string
	CurrentID() string
}

func NewConnectionManager(ctx context.Context, logger *slog.Logger, processor Processor) *Manager {
	return &Manager{
		ctx: ctx,

		processor: processor,

		logger: logger,

		bus:           NewWatermill(),
		runningUsers:  make(map[uuid.UUID]bool, 100),
		userCancels:   make(map[uuid.UUID]context.CancelFunc, 100),
		overlayStates: make(map[uuid.UUID]OverlayState, 100),
	}
}

// SetProcessor allows late binding to avoid init cycles.
func SetProcessor(m *Manager, processor Processor) {
	m.processor = processor
}

type PromptImages struct {
	ImageIDs   []string `json:"image_ids"`
	ShowImages *bool    `json:"show_images,omitempty"`
}

type Audio struct {
	Audio []byte `json:"audio"`
}

type Text struct {
	Text string `json:"text"`
}

const (
	EventTypeAudio EventType = iota + 1
	EventTypeVideo
	EventTypeImage
	EventTypePromptImage
	EventTypeText
	EventTypeSkip
	EventTypeShowImages
	EventTypeHideImages
	EventTypePing
	EventTypeTrackMeta
	EventTypeSnapshot
	EventTypeClean
	EventTypeReload
)

type EventType int

func (et EventType) String() string {
	switch et {
	case EventTypeAudio:
		return "audio"
	case EventTypeVideo:
		return "video"
	case EventTypeImage:
		return "image"
	case EventTypePromptImage:
		return "prompt_image"
	case EventTypeText:
		return "text"
	case EventTypeSkip:
		return "skip"
	case EventTypePing:
		return "ping"
	case EventTypeShowImages:
		return "show_images"
	case EventTypeHideImages:
		return "hide_images"
	case EventTypeTrackMeta:
		return "track_meta"
	case EventTypeSnapshot:
		return "snapshot"
	case EventTypeClean:
		return "clean"
	case EventTypeReload:
		return "reload"
	default:
		return "unknown"
	}
}

// Audio-socket binary frame types (overlay-v2): [1B type][4B BE header len][header JSON][payload].
const (
	AudioFrameChunk     byte = 1
	AudioFrameTrackDone byte = 2
	AudioFramePing      byte = 3
)

type DataEvent struct {
	EventType EventType `json:"type"`
	EventData []byte    `json:"data"`
}

func (d *DataEvent) String() string {
	return fmt.Sprintf("%s:%s", d.EventType.String(), string(d.EventData))
}

func (m *Manager) topic(userID uuid.UUID) string {
	return "user.events." + userID.String()
}

func (m *Manager) controlTopic(userID uuid.UUID) string {
	return "user.control." + userID.String()
}

func (m *Manager) controlPanelTopic(userID uuid.UUID) string {
	return "controlpanel." + userID.String()
}

func (m *Manager) publishControl(userID uuid.UUID, upd *Update) {
	data, err := json.Marshal(upd)
	if err != nil {
		return
	}
	_ = m.bus.Publish(m.ctx, m.controlTopic(userID), data)
}

func (m *Manager) NotifyControlPanel(userID uuid.UUID) {
	_ = m.bus.Publish(m.ctx, m.controlPanelTopic(userID), []byte("1"))
}

func (m *Manager) SubscribeControlPanel(ctx context.Context, userID uuid.UUID) chan struct{} {
	events := make(chan struct{})
	msgs, err := m.bus.Subscribe(ctx, m.controlPanelTopic(userID))
	if err != nil {
		close(events)
		return events
	}
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				msg.Ack()
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	return events
}

func (m *Manager) Subscribe(userID uuid.UUID) (<-chan *DataEvent, func()) {
	// subscribe to watermill topic and adapt to DataEvent channel
	out := make(chan *DataEvent, 64)

	ctx, cancel := context.WithCancel(m.ctx)
	msgs, err := m.bus.Subscribe(ctx, m.topic(userID))
	if err != nil {
		cancel()
		close(out)
		return out, func() {}
	}

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				var ev DataEvent
				if err := json.Unmarshal(msg.Payload, &ev); err == nil {
					select {
					case out <- &ev:
					default:
					}
				}
				msg.Ack()
			}
		}
	}()

	return out, func() { cancel() }
}

func (m *Manager) TryWrite(userID uuid.UUID, event *DataEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_ = m.bus.Publish(m.ctx, m.topic(userID), data)
	return true
}

func (m *Manager) audioTopic(userID uuid.UUID) string {
	return "user.audio." + userID.String()
}

// TryWriteAudio publishes a pre-assembled binary overlay frame on the audio
// topic. Audio rides its own topic so chunk bursts can never evict control
// events (skip, meta) from the shared drop-on-full subscriber buffer.
func (m *Manager) TryWriteAudio(userID uuid.UUID, frame []byte) bool {
	_ = m.bus.Publish(m.ctx, m.audioTopic(userID), frame)
	return true
}

// SubscribeAudio delivers raw binary frames for the audio websocket. Same
// drop-on-full semantics as Subscribe.
func (m *Manager) SubscribeAudio(userID uuid.UUID) (<-chan []byte, func()) {
	out := make(chan []byte, 64)

	ctx, cancel := context.WithCancel(m.ctx)
	msgs, err := m.bus.Subscribe(ctx, m.audioTopic(userID))
	if err != nil {
		cancel()
		close(out)
		return out, func() {}
	}

	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				select {
				case out <- msg.Payload:
				default:
				}
				msg.Ack()
			}
		}
	}()

	return out, func() { cancel() }
}

// RegisterOverlayState exposes a processor's live state for connect snapshots.
func (m *Manager) RegisterOverlayState(userID uuid.UUID, st OverlayState) {
	m.overlayStatesLock.Lock()
	defer m.overlayStatesLock.Unlock()
	m.overlayStates[userID] = st
}

func (m *Manager) UnregisterOverlayState(userID uuid.UUID) {
	m.overlayStatesLock.Lock()
	defer m.overlayStatesLock.Unlock()
	delete(m.overlayStates, userID)
}

// OverlaySnapshot returns the skip set and current message for a fresh overlay
// connection; empty snapshot when no processor is running.
func (m *Manager) OverlaySnapshot(userID uuid.UUID) (skipped []string, current string) {
	m.overlayStatesLock.RLock()
	st := m.overlayStates[userID]
	m.overlayStatesLock.RUnlock()
	if st == nil {
		return nil, ""
	}
	return st.SkippedList(), st.CurrentID()
}

// ReloadOverlay tells every connected overlay of the user to location.reload()
// — the escape hatch from the OBS browser-source cache after JS deploys.
func (m *Manager) ReloadOverlay(userID uuid.UUID) {
	m.TryWrite(userID, &DataEvent{
		EventType: EventTypeReload,
		EventData: []byte("reload"),
	})
}

func (m *Manager) NotifyUpdateSettings(userID uuid.UUID) {
	m.publishControl(userID, &Update{UpdateType: RestartProcessor})
}

func (m *Manager) SkipMessage(userID uuid.UUID, msgID string) {
	m.publishControl(userID, &Update{UpdateType: SkipMessage, Data: msgID})
}

func (m *Manager) ShowImages(userID uuid.UUID, msgID string) {
	m.publishControl(userID, &Update{UpdateType: ShowImages, Data: msgID})
}

func (m *Manager) HideImages(userID uuid.UUID, msgID string) {
	m.publishControl(userID, &Update{UpdateType: HideImages, Data: msgID})
}

func (m *Manager) CleanOverlay(userID uuid.UUID) {
	m.publishControl(userID, &Update{UpdateType: CleanOverlay})
}

func (m *Manager) SkipCurrent(userID uuid.UUID, token, msgID string) {
	m.publishControl(userID, &Update{UpdateType: SkipCurrent, Data: token, MsgID: msgID})
}

func (m *Manager) ShowImagesCurrent(userID uuid.UUID, token, msgID string) {
	m.publishControl(userID, &Update{UpdateType: ShowImagesCurrent, Data: token, MsgID: msgID})
}

func (m *Manager) DisableUser(userID uuid.UUID) {
	m.rwMutex.Lock()
	if cancel, ok := m.userCancels[userID]; ok {
		cancel()
		delete(m.userCancels, userID)
	}
	delete(m.runningUsers, userID)
	m.rwMutex.Unlock()
}

func (m *Manager) HandleUser(user *db.User) {
	logger := m.logger.With("user", user.TwitchLogin)

	m.rwMutex.Lock()
	if m.runningUsers[user.ID] {
		m.rwMutex.Unlock()
		logger.Info("starting processor failed, already running")
		return
	}
	// mark running and create per-user context
	userCtx, cancel := context.WithCancel(m.ctx)
	m.userCancels[user.ID] = cancel
	m.runningUsers[user.ID] = true
	m.rwMutex.Unlock()

	logger.Info("starting processor")

	m.wg.Add(1)
	go func() {
		defer logger.Info("stopped processor")
		defer m.wg.Done()

		// subscribe to control updates via watermill and bridge to chan for processor
		updates := make(chan *Update, 64)
		msgs, err := m.bus.Subscribe(userCtx, m.controlTopic(user.ID))
		if err != nil {
			logger.Error("failed to subscribe to control topic", "err", err)
			close(updates)
			return
		}

		bridgeDone := make(chan struct{})
		go func() {
			defer close(bridgeDone)
			defer close(updates)
			for {
				select {
				case <-userCtx.Done():
					return
				case msg, ok := <-msgs:
					if !ok {
						return
					}
					var u Update
					if err := json.Unmarshal(msg.Payload, &u); err == nil {
						select {
						case updates <- &u:
						default:
							logger.Warn("dropped control update, processor too slow", "update_type", u.UpdateType)
						}
					}
					msg.Ack()
				}
			}
		}()

	loop:
		for {
			select {
			case <-m.ctx.Done():
				break loop
			default:
			}

			if err := m.processor.Process(userCtx, updates, func(event *DataEvent) bool {
				return m.TryWrite(user.ID, event)
			}, user); err != nil {
				if errors.Is(err, ErrProcessingEnd) {
					break loop
				} else if errors.Is(err, ErrNoUser) {
					select {
					case <-time.After(time.Second):
					case <-m.ctx.Done():
					}
				} else {
					logger.Error("processor Process error", "err", err)

					select {
					case <-time.After(10 * time.Second):
					case <-m.ctx.Done():
					}
				}
			}

			select {
			case <-time.After(time.Second):
			case <-m.ctx.Done():
			}
		}

		cancel()
		<-bridgeDone

		m.rwMutex.Lock()
		delete(m.userCancels, user.ID)
		delete(m.runningUsers, user.ID)
		m.rwMutex.Unlock()
	}()
}

func (m *Manager) Wait() {
	m.wg.Wait()
}
