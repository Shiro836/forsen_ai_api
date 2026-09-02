package emoteservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ScoreEmotes ranks cached emotes against a rule vector. Image and description
// similarity are separate rows so the caller can see which signal fired.
func (s *Store) ScoreEmotes(ctx context.Context, vec []float32, imageThreshold, descThreshold float64, limit int) ([]CandidateRow, error) {
	param := vectorParam(vec)
	if param == nil {
		return nil, fmt.Errorf("score emotes: empty rule vector")
	}
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.pool.Query(ctx,
		`SELECT provider, emote_id, name, description, grid_key, source, score FROM (
			SELECT provider, emote_id, name, description, grid_key, 'image' AS source,
			       1 - (image_embedding <=> $1::text::vector) AS score
			  FROM emote_moderation WHERE image_embedding IS NOT NULL
			UNION ALL
			SELECT provider, emote_id, name, description, grid_key, 'description' AS source,
			       1 - (desc_embedding <=> $1::text::vector) AS score
			  FROM emote_moderation WHERE desc_embedding IS NOT NULL
		 ) scored
		 WHERE (source = 'image' AND score >= $2) OR (source = 'description' AND score >= $3)
		 ORDER BY score DESC
		 LIMIT $4`,
		param, imageThreshold, descThreshold, limit)
	if err != nil {
		return nil, fmt.Errorf("score emotes: %w", err)
	}
	defer rows.Close()

	var out []CandidateRow
	for rows.Next() {
		var r CandidateRow
		if err := rows.Scan(&r.Provider, &r.EmoteID, &r.Name, &r.Description, &r.GridKey, &r.Source, &r.Score); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRule(ctx context.Context, streamerID, ruleText string, threshold *float64) (*Rule, error) {
	var r Rule
	err := s.pool.QueryRow(ctx,
		`INSERT INTO emote_rules (streamer_id, rule_text, threshold) VALUES ($1, $2, $3)
		 RETURNING id, streamer_id::text, rule_text, threshold, status, created_at, updated_at`,
		streamerID, ruleText, threshold).Scan(&r.ID, &r.StreamerID, &r.RuleText, &r.Threshold, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create rule for %s: %w", streamerID, err)
	}
	return &r, nil
}

func (s *Store) GetRule(ctx context.Context, ruleID string) (*Rule, error) {
	var r Rule
	err := s.pool.QueryRow(ctx,
		`SELECT r.id, r.streamer_id::text, r.rule_text, r.threshold, r.status, r.created_at, r.updated_at,
		        (SELECT count(*) FROM emote_rule_matches m WHERE m.rule_id = r.id)
		   FROM emote_rules r WHERE r.id = $1`, ruleID,
	).Scan(&r.ID, &r.StreamerID, &r.RuleText, &r.Threshold, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.MatchCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get rule %s: %w", ruleID, err)
	}
	return &r, nil
}

func (s *Store) ListRules(ctx context.Context, streamerID string) ([]Rule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.streamer_id::text, r.rule_text, r.threshold, r.status, r.created_at, r.updated_at,
		        (SELECT count(*) FROM emote_rule_matches m WHERE m.rule_id = r.id)
		   FROM emote_rules r WHERE r.streamer_id = $1 ORDER BY r.created_at DESC`, streamerID)
	if err != nil {
		return nil, fmt.Errorf("list rules for %s: %w", streamerID, err)
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.StreamerID, &r.RuleText, &r.Threshold, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.MatchCount); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRule(ctx context.Context, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM emote_rules WHERE id = $1`, ruleID)
	if err != nil {
		return fmt.Errorf("delete rule %s: %w", ruleID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ConfirmRule replaces a rule's materialized blocklist with exactly the emotes
// the streamer ticked and makes the rule enforceable.
func (s *Store) ConfirmRule(ctx context.Context, ruleID, provider string, emoteIDs []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin confirm rule %s: %w", ruleID, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM emote_rule_matches WHERE rule_id = $1`, ruleID); err != nil {
		return fmt.Errorf("clear rule matches %s: %w", ruleID, err)
	}
	if len(emoteIDs) > 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO emote_rule_matches (rule_id, provider, emote_id)
			 SELECT $1, $3, unnest($2::text[])
			 ON CONFLICT (rule_id, provider, emote_id) DO NOTHING`,
			ruleID, emoteIDs, NormalizeProvider(provider)); err != nil {
			return fmt.Errorf("insert rule matches %s: %w", ruleID, err)
		}
	}

	tag, err := tx.Exec(ctx,
		`UPDATE emote_rules SET status = 'active', updated_at = now() WHERE id = $1`, ruleID)
	if err != nil {
		return fmt.Errorf("activate rule %s: %w", ruleID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}
