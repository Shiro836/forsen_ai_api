package donations

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/imagetag"

	"github.com/google/uuid"
)

const (
	StatusConnecting = "connecting"
	StatusConnected  = "connected"
	StatusError      = "error"
	StatusExpired    = "expired"

	maxBackoff = 5 * time.Minute
	stopWait   = 3 * time.Second
)

// Key identifies one streamer's saved credential for one provider.
type Key struct {
	UserID   uuid.UUID
	Provider string
}

// Spec is what the database says a connection should use. UpdatedAt is the
// user's action marker: a refresh must not look like a reconnect.
type Spec struct {
	Credentials Credentials
	UpdatedAt   time.Time
}

type connection struct {
	cancel    context.CancelFunc
	done      chan struct{}
	updatedAt time.Time
}

// Manager keeps one connection per saved credential. It is the only writer of
// connection status, and it is the only consumer of donation sockets.
type Manager struct {
	logger  *slog.Logger
	db      *db.DB
	rates   *Rates
	sources map[string]Source

	mu        sync.Mutex
	live      map[Key]*connection
	permanent map[Key]time.Time
	statuses  map[Key]string
}

func NewManager(logger *slog.Logger, database *db.DB, sources ...Source) *Manager {
	m := &Manager{
		logger:    logger,
		db:        database,
		rates:     NewRates(logger.WithGroup("rates"), database),
		sources:   make(map[string]Source, len(sources)),
		live:      make(map[Key]*connection),
		permanent: make(map[Key]time.Time),
		statuses:  make(map[Key]string),
	}
	for _, source := range sources {
		m.sources[source.Provider()] = source
	}
	return m
}

// Run refreshes rates and owns the manager until the service stops.
func (m *Manager) Run(ctx context.Context) {
	go m.rates.Run(ctx)

	<-ctx.Done()

	m.mu.Lock()
	conns := make([]*connection, 0, len(m.live))
	for key, conn := range m.live {
		conn.cancel()
		delete(m.live, key)
		delete(m.statuses, key)
		conns = append(conns, conn)
	}
	m.mu.Unlock()

	for _, conn := range conns {
		select {
		case <-conn.done:
		case <-time.After(stopWait):
			m.logger.Warn("donation connection did not stop in time")
		}
	}
}

// Sync turns the desired set into live connections. It stops what is gone or
// changed first, waits for those sockets to close, then starts what is new.
func (m *Manager) Sync(ctx context.Context, desired map[Key]Spec) {
	var stopping []*connection

	m.mu.Lock()
	for key, conn := range m.live {
		spec, wanted := desired[key]
		if wanted && spec.UpdatedAt.Equal(conn.updatedAt) {
			continue
		}
		conn.cancel()
		delete(m.live, key)
		delete(m.statuses, key)
		stopping = append(stopping, conn)
	}
	for key := range m.permanent {
		if _, wanted := desired[key]; !wanted {
			delete(m.permanent, key)
		}
	}
	m.mu.Unlock()

	for _, conn := range stopping {
		select {
		case <-conn.done:
		case <-time.After(stopWait):
			m.logger.Warn("donation connection did not stop in time")
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for key, spec := range desired {
		source, ok := m.sources[key.Provider]
		if !ok {
			continue
		}
		if _, live := m.live[key]; live {
			continue
		}
		if failedAt, failed := m.permanent[key]; failed && failedAt.Equal(spec.UpdatedAt) {
			continue
		}
		m.start(ctx, key, source, spec)
	}
}

func (m *Manager) start(ctx context.Context, key Key, source Source, spec Spec) {
	connCtx, cancel := context.WithCancel(ctx)
	conn := &connection{
		cancel:    cancel,
		done:      make(chan struct{}),
		updatedAt: spec.UpdatedAt,
	}
	m.live[key] = conn

	go m.run(connCtx, key, source, spec, conn)
}

func (m *Manager) run(ctx context.Context, key Key, source Source, spec Spec, conn *connection) {
	defer close(conn.done)

	lastEvent := time.Time{}
	if spec.Credentials.LastEventAt != nil {
		lastEvent = *spec.Credentials.LastEventAt
	}
	var lastEventMu sync.Mutex

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		m.setStatus(key, StatusConnecting, "")
		connected := false
		session := Session{
			Emit: func(event Event) {
				if !m.enqueue(ctx, key, event) || event.CreatedAt.IsZero() {
					return
				}
				lastEventMu.Lock()
				if event.CreatedAt.After(lastEvent) {
					lastEvent = event.CreatedAt
				}
				lastEventMu.Unlock()
			},
			Connected: func() {
				connected = true
				m.clearPermanent(key)
				m.setStatus(key, StatusConnected, "")
			},
		}

		creds := spec.Credentials
		lastEventMu.Lock()
		at := lastEvent
		lastEventMu.Unlock()
		if !at.IsZero() {
			creds.LastEventAt = &at
		}
		err := source.Run(ctx, creds, session)
		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, ErrAuth) || errors.Is(err, ErrExpired) {
			m.setStatus(key, StatusExpired, errorText(err))
			m.setPermanent(key, spec.UpdatedAt)
			m.logger.Warn("donation connection needs the streamer",
				"user", key.UserID, "provider", key.Provider, "err", err)
			return
		}

		m.setStatus(key, StatusError, errorText(err))
		m.logger.Warn("donation connection failed",
			"user", key.UserID, "provider", key.Provider, "err", err)

		if connected {
			backoff = time.Second
		}
		delay := backoff + time.Duration(rand.Int64N(int64(backoff/4)+1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (m *Manager) setStatus(key Key, status, errorText string) {
	m.mu.Lock()
	if m.statuses[key] == status {
		m.mu.Unlock()
		return
	}
	m.statuses[key] = status
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.db.SetDonationProviderStatus(ctx, key.UserID, key.Provider, status, errorText); err != nil {
		m.logger.Error("failed to store donation provider status",
			"user", key.UserID, "provider", key.Provider, "err", err)
	}
}

func (m *Manager) setPermanent(key Key, updatedAt time.Time) {
	m.mu.Lock()
	m.permanent[key] = updatedAt
	m.mu.Unlock()
}

func (m *Manager) clearPermanent(key Key) {
	m.mu.Lock()
	delete(m.permanent, key)
	m.mu.Unlock()
}

func (m *Manager) enqueue(ctx context.Context, key Key, event Event) bool {
	logger := m.logger.With(
		"user", key.UserID,
		"provider", key.Provider,
		"provider_id", event.ProviderID,
	)

	// An empty donation is still played: the streamer's "no message" line is
	// filled in at play time, like every other event lane.
	message := strings.TrimSpace(event.Message)
	if event.AmountMinor <= 0 {
		logger.Info("donation not queued", "reason", "no amount")
		return true
	}

	usdMinor, rate, ok := m.rates.USD(event.AmountMinor, event.Currency)
	if !ok {
		logger.Error("donation has no USD rate", "currency", event.Currency)
	}

	from := strings.TrimSpace(event.From)
	if from == "" {
		from = "anonymous"
	}

	msg := db.TwitchMessage{
		TwitchLogin: from,
		Message:     message,
		Event: &db.EventMeta{
			Kind:        db.EventKindDonation,
			Provider:    event.Provider,
			ProviderID:  event.ProviderID,
			AmountMinor: event.AmountMinor,
			AmountText:  FormatAmount(event.AmountMinor, event.Currency),
			Currency:    event.Currency,
			Rate:        rate,
			USD:         float64(usdMinor) / 100,
		},
	}

	showImages := false
	data := &db.MessageData{
		ImageIDs:   imagetag.ExtractIDs(message, 2),
		ShowImages: &showImages,
	}

	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	queueID, created, err := m.db.PushIngestMsg(writeCtx, key.UserID, msg, data, event.Provider+":"+event.ProviderID, "")
	if err != nil {
		logger.Error("failed to queue donation", "err", err)
		return false
	}
	if err := m.db.AdvanceDonationProviderLastEvent(writeCtx, key.UserID, key.Provider, event.CreatedAt); err != nil {
		logger.Error("failed to advance the donation backfill watermark", "err", err)
	}
	if created {
		logger.Info("ingested donation",
			"queue_id", queueID,
			"amount_minor", event.AmountMinor,
			"currency", event.Currency,
			"usd", float64(usdMinor)/100,
			"mock", event.Mock,
		)
		return true
	}
	logger.Info("donation was queued already", "queue_id", queueID)
	return true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
