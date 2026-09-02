package emoteservice

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("emoteservice: not found")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, connStr string) (*Store, error) {
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, fmt.Errorf("create emote db pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping emote db: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies every embedded migration that this database has not recorded
// yet, each file in one transaction.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		content, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.applyMigration(ctx, name, string(content)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, content string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer tx.Rollback(ctx)

	// Parameterless Exec goes out over the simple protocol, which accepts the
	// whole multi-statement file in one round trip.
	if _, err := tx.Exec(ctx, content); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	return tx.Commit(ctx)
}

// tagsParam keeps a nil slice out of a NOT NULL text[] column.
func tagsParam(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// vectorParam renders an embedding as pgvector's text form. Queries cast it with
// ::text::vector so pgx never needs a codec for the extension's dynamic OID.
func vectorParam(v []float32) *string {
	if len(v) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	out := b.String()
	return &out
}

const emoteColumns = `provider, emote_id, name, listed, animated, deleted, flags, tags,
	description, classes, model, classifier_version, classified_at, original_key, grid_key,
	flash_hz, flash_hz_max, red_flash_hz, flash_measured_at,
	image_embedding IS NOT NULL`

func scanEmote(row pgx.Row) (*Emote, error) {
	var e Emote
	err := row.Scan(&e.Provider, &e.ID, &e.Name, &e.Listed, &e.Animated, &e.Deleted, &e.Flags, &e.Tags,
		&e.Description, &e.Classes, &e.Model, &e.ClassifierVersion, &e.ClassifiedAt, &e.OriginalKey, &e.GridKey,
		&e.FlashHz, &e.FlashHzMax, &e.RedFlashHz, &e.FlashMeasuredAt,
		&e.HasImageEmbedding)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan emote: %w", err)
	}
	return &e, nil
}

func (s *Store) GetEmote(ctx context.Context, provider, emoteID string) (*Emote, error) {
	return scanEmote(s.pool.QueryRow(ctx,
		`SELECT `+emoteColumns+` FROM emote_moderation WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID))
}

// SearchEmotes lists cached emotes whose name contains q, newest first.
func (s *Store) SearchEmotes(ctx context.Context, q string, limit int) ([]*Emote, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+emoteColumns+` FROM emote_moderation
		 WHERE $1 = '' OR lower(name) LIKE '%' || lower($1) || '%'
		 ORDER BY updated_at DESC LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search emotes: %w", err)
	}
	defer rows.Close()

	var out []*Emote
	for rows.Next() {
		e, err := scanEmote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertMetadata records what 7TV knows about an emote without touching the
// classification columns.
func (s *Store) UpsertMetadata(ctx context.Context, e *Emote) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO emote_moderation (provider, emote_id, name, listed, animated, deleted, flags, tags, original_key, grid_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (provider, emote_id) DO UPDATE SET
			name = EXCLUDED.name,
			listed = EXCLUDED.listed,
			animated = EXCLUDED.animated,
			deleted = EXCLUDED.deleted,
			flags = EXCLUDED.flags,
			tags = EXCLUDED.tags,
			original_key = CASE WHEN EXCLUDED.original_key = '' THEN emote_moderation.original_key ELSE EXCLUDED.original_key END,
			grid_key = CASE WHEN EXCLUDED.grid_key = '' THEN emote_moderation.grid_key ELSE EXCLUDED.grid_key END,
			updated_at = now()`,
		NormalizeProvider(e.Provider), e.ID, e.Name, e.Listed, e.Animated, e.Deleted, e.Flags,
		tagsParam(e.Tags), e.OriginalKey, e.GridKey)
	if err != nil {
		return fmt.Errorf("upsert emote metadata %s: %w", e.ID, err)
	}
	return nil
}

// SaveClassification writes the vision verdict. It is stored on its own so an
// embedder outage never costs a second GPU pass over the same emote, and it
// overwrites in place so reclassifying an existing row needs no special path.
func (s *Store) SaveClassification(ctx context.Context, provider, emoteID string, c *Classification, model string, version int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emote_moderation SET
			description = $3,
			classes = $4,
			model = $5,
			classifier_version = $6,
			classified_at = now(),
			updated_at = now()
		 WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID, c.Description, tagsParam(c.Classes), model, version)
	if err != nil {
		return fmt.Errorf("save classification %s: %w", emoteID, err)
	}
	return nil
}

// SaveFlashMeasure writes the measured rates and derives the flashing class from
// them. It is the only writer of that class, in both directions: it adds it to
// whatever the vision model found and removes it when the numbers are clean, so
// re-measuring an emote can never disturb the rest of the verdict.
func (s *Store) SaveFlashMeasure(ctx context.Context, provider, emoteID string, m FlashMeasure) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emote_moderation SET
			flash_hz = $3,
			flash_hz_max = $4,
			red_flash_hz = $5,
			flash_measured_at = now(),
			classes = (
				SELECT coalesce(array_agg(c ORDER BY c), '{}')
				FROM unnest(CASE WHEN $6
					THEN array_append(array_remove(classes, $7), $7)
					ELSE array_remove(classes, $7) END) AS c
			),
			updated_at = now()
		 WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID, m.Hz, m.HzMax, m.RedHz, m.Flashing(), ClassFlashing)
	if err != nil {
		return fmt.Errorf("save flash measure %s: %w", emoteID, err)
	}
	return nil
}

// FlashTarget is one emote the flash backfill can rescore: a classified row
// whose original webp is still in S3.
type FlashTarget struct {
	Provider    string `json:"provider"`
	EmoteID     string `json:"emote_id"`
	OriginalKey string `json:"original_key"`
}

// FlashBackfillTargets lists every classified emote that can be rescored from
// storage. Rows that were never classified need nothing: they are measured on
// their first pass through the queue.
func (s *Store) FlashBackfillTargets(ctx context.Context) ([]FlashTarget, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT provider, emote_id, original_key FROM emote_moderation
		  WHERE classified_at IS NOT NULL AND original_key <> ''
		  ORDER BY provider, emote_id`)
	if err != nil {
		return nil, fmt.Errorf("list flash backfill targets: %w", err)
	}
	defer rows.Close()

	var out []FlashTarget
	for rows.Next() {
		var t FlashTarget
		if err := rows.Scan(&t.Provider, &t.EmoteID, &t.OriginalKey); err != nil {
			return nil, fmt.Errorf("scan flash backfill target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveEmbeddings attaches the vectors a rule is matched against. A nil vector
// leaves the stored one alone.
func (s *Store) SaveEmbeddings(ctx context.Context, provider, emoteID string, imageEmbedding, descEmbedding []float32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emote_moderation SET
			image_embedding = COALESCE($3::text::vector, image_embedding),
			desc_embedding = COALESCE($4::text::vector, desc_embedding),
			updated_at = now()
		 WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID, vectorParam(imageEmbedding), vectorParam(descEmbedding))
	if err != nil {
		return fmt.Errorf("save embeddings %s: %w", emoteID, err)
	}
	return nil
}

// LoadFacts gathers every input the precedence chain needs for a batch of
// emotes in one channel.
func (s *Store) LoadFacts(ctx context.Context, streamerID, provider string, emoteIDs []string) (map[string]Facts, error) {
	provider = NormalizeProvider(provider)

	facts := make(map[string]Facts, len(emoteIDs))
	for _, id := range emoteIDs {
		facts[id] = Facts{}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT `+emoteColumns+` FROM emote_moderation WHERE provider = $2 AND emote_id = ANY($1)`,
		emoteIDs, provider)
	if err != nil {
		return nil, fmt.Errorf("load emotes: %w", err)
	}
	for rows.Next() {
		e, err := scanEmote(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		f := facts[e.ID]
		f.Emote = e
		facts[e.ID] = f
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load emotes: %w", err)
	}

	orows, err := s.pool.Query(ctx,
		`SELECT streamer_id::text, emote_id, verdict FROM emote_overrides
		 WHERE provider = $4 AND emote_id = ANY($1) AND streamer_id IN ($2, $3)`,
		emoteIDs, streamerID, GlobalScope, provider)
	if err != nil {
		return nil, fmt.Errorf("load overrides: %w", err)
	}
	for orows.Next() {
		var scope, emoteID, verdict string
		if err := orows.Scan(&scope, &emoteID, &verdict); err != nil {
			orows.Close()
			return nil, fmt.Errorf("scan override: %w", err)
		}
		f := facts[emoteID]
		switch {
		case scope == GlobalScope && verdict == string(VerdictBlock):
			f.GlobalBan = true
		case scope == GlobalScope:
			f.GlobalApprove = true
		case verdict == string(VerdictBlock):
			f.StreamerBan = true
		default:
			f.StreamerApprove = true
		}
		facts[emoteID] = f
	}
	orows.Close()
	if err := orows.Err(); err != nil {
		return nil, fmt.Errorf("load overrides: %w", err)
	}

	if streamerID != GlobalScope {
		rrows, err := s.pool.Query(ctx,
			`SELECT m.emote_id, m.rule_id FROM emote_rule_matches m
			 JOIN emote_rules r ON r.id = m.rule_id
			 WHERE r.streamer_id = $2 AND r.status = 'active'
			   AND m.provider = $3 AND m.emote_id = ANY($1)`,
			emoteIDs, streamerID, provider)
		if err != nil {
			return nil, fmt.Errorf("load rule matches: %w", err)
		}
		for rrows.Next() {
			var emoteID, ruleID string
			if err := rrows.Scan(&emoteID, &ruleID); err != nil {
				rrows.Close()
				return nil, fmt.Errorf("scan rule match: %w", err)
			}
			f := facts[emoteID]
			f.RuleID = ruleID
			facts[emoteID] = f
		}
		rrows.Close()
		if err := rrows.Err(); err != nil {
			return nil, fmt.Errorf("load rule matches: %w", err)
		}
	}

	return facts, nil
}

// GetPlatformSettings reads the default every channel falls back to. A missing
// row means the seed never ran, so it answers with the fail-safe rather than
// with nothing blocked.
func (s *Store) GetPlatformSettings(ctx context.Context) ([]string, error) {
	var blocked []string
	err := s.pool.QueryRow(ctx, `SELECT blocked_classes FROM emote_platform_settings WHERE id`).Scan(&blocked)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultBlockedClasses(), nil
	}
	if err != nil {
		return DefaultBlockedClasses(), fmt.Errorf("get platform settings: %w", err)
	}
	return blocked, nil
}

func (s *Store) UpsertPlatformSettings(ctx context.Context, blocked []string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO emote_platform_settings (id, blocked_classes) VALUES (true, $1)
		 ON CONFLICT (id) DO UPDATE SET blocked_classes = EXCLUDED.blocked_classes, updated_at = now()`,
		tagsParam(blocked))
	if err != nil {
		return fmt.Errorf("upsert platform settings: %w", err)
	}
	return nil
}

// GetSettings resolves a channel's effective configuration. A channel with no
// row, or one whose blocked_classes was never set, inherits the platform
// default — Decide only ever sees the resolved set.
func (s *Store) GetSettings(ctx context.Context, streamerID string) (Settings, error) {
	platform, err := s.GetPlatformSettings(ctx)
	if err != nil {
		return Settings{StreamerID: streamerID, BlockedClasses: platform, Inherited: true}, err
	}

	inherited := Settings{StreamerID: streamerID, BlockedClasses: platform, Inherited: true}

	var blocked []string
	err = s.pool.QueryRow(ctx,
		`SELECT blocked_classes FROM emote_streamer_settings WHERE streamer_id = $1`,
		streamerID).Scan(&blocked)
	if errors.Is(err, pgx.ErrNoRows) || blocked == nil {
		return inherited, nil
	}
	if err != nil {
		return inherited, fmt.Errorf("get settings %s: %w", streamerID, err)
	}

	return Settings{StreamerID: streamerID, BlockedClasses: blocked}, nil
}

func (s *Store) UpsertSettings(ctx context.Context, st Settings) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO emote_streamer_settings (streamer_id, blocked_classes)
		 VALUES ($1, $2)
		 ON CONFLICT (streamer_id) DO UPDATE SET
			blocked_classes = EXCLUDED.blocked_classes,
			updated_at = now()`,
		st.StreamerID, tagsParam(st.BlockedClasses))
	if err != nil {
		return fmt.Errorf("upsert settings %s: %w", st.StreamerID, err)
	}
	return nil
}

// ResetSettings drops a channel back to inheriting the platform default.
func (s *Store) ResetSettings(ctx context.Context, streamerID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM emote_streamer_settings WHERE streamer_id = $1`, streamerID)
	if err != nil {
		return fmt.Errorf("reset settings %s: %w", streamerID, err)
	}
	return nil
}

type Override struct {
	StreamerID string    `json:"streamer_id"`
	Provider   string    `json:"provider"`
	EmoteID    string    `json:"emote_id"`
	Verdict    Verdict   `json:"verdict"`
	Reason     string    `json:"reason,omitempty"`
	Author     string    `json:"author,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Store) AddOverride(ctx context.Context, o Override) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO emote_overrides (streamer_id, provider, emote_id, verdict, reason, author)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (streamer_id, provider, emote_id, verdict) DO UPDATE SET
			reason = EXCLUDED.reason,
			author = EXCLUDED.author,
			created_at = now()`,
		o.StreamerID, NormalizeProvider(o.Provider), o.EmoteID, string(o.Verdict), o.Reason, o.Author)
	if err != nil {
		return fmt.Errorf("add override %s/%s: %w", o.StreamerID, o.EmoteID, err)
	}
	return nil
}

func (s *Store) DeleteOverride(ctx context.Context, streamerID, provider, emoteID string, verdict Verdict) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM emote_overrides
		 WHERE streamer_id = $1 AND provider = $2 AND emote_id = $3 AND verdict = $4`,
		streamerID, NormalizeProvider(provider), emoteID, string(verdict))
	if err != nil {
		return fmt.Errorf("delete override %s/%s: %w", streamerID, emoteID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListOverrides(ctx context.Context, streamerID string) ([]Override, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT streamer_id::text, provider, emote_id, verdict, reason, author, created_at
		 FROM emote_overrides WHERE streamer_id = $1 ORDER BY created_at DESC`, streamerID)
	if err != nil {
		return nil, fmt.Errorf("list overrides %s: %w", streamerID, err)
	}
	defer rows.Close()

	out := []Override{}
	for rows.Next() {
		var o Override
		var verdict string
		if err := rows.Scan(&o.StreamerID, &o.Provider, &o.EmoteID, &verdict, &o.Reason, &o.Author, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan override: %w", err)
		}
		o.Verdict = Verdict(verdict)
		out = append(out, o)
	}
	return out, rows.Err()
}
