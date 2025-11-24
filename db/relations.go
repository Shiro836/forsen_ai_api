package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type RelationType int

const (
	RelationTypeAny        = iota
	RelationTypeModerating // user 1 moderates user 2
)

func (r RelationType) String() string {
	switch r {
	case RelationTypeAny:
		return "any"
	case RelationTypeModerating:
		return "moderating"
	default:
		return "unknown"
	}
}

type Relation struct {
	ID uuid.UUID

	TwitchLogin1  string
	TwitchUserID1 int

	TwitchLogin2  string
	TwitchUserID2 int

	RelationType RelationType
}

func (db *DB) AddRelation(ctx context.Context, relation *Relation) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.QueryRow(ctx, `
		INSERT INTO relations (
			twitch_login_1,
			twitch_user_id_1,
			twitch_login_2,
			twitch_user_id_2,
			relation_type
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (twitch_user_id_1, twitch_user_id_2, relation_type) DO NOTHING
		RETURNING id
	`,
		relation.TwitchLogin1,
		relation.TwitchUserID1,
		relation.TwitchLogin2,
		relation.TwitchUserID2,
		relation.RelationType,
	).Scan(&id)

	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to add relation: %w", err)
	}

	return id, nil
}

func (db *DB) RemoveRelation(ctx context.Context, relation *Relation) error {
	_, err := db.Exec(ctx, `
		DELETE FROM relations
		WHERE
			twitch_user_id_1 = $1
		AND
			twitch_user_id_2 = $2
		AND
			relation_type = $3
	`,
		relation.TwitchUserID1,
		relation.TwitchUserID2,
		relation.RelationType,
	)
	if err != nil {
		return fmt.Errorf("failed to remove relation: %w", err)
	}

	return nil
}

func (db *DB) GetRelations(ctx context.Context, user *User, relationType RelationType) ([]Relation, error) {
	if relationType == RelationTypeAny {
		panic("not implemented")
	}

	var relations []Relation

	rows, err := db.Query(ctx, `
		SELECT
			id,
			twitch_login_1,
			twitch_user_id_1,
			twitch_login_2,
			twitch_user_id_2,
			relation_type
		FROM relations
		WHERE
			twitch_user_id_1 = $1
		AND
			relation_type = $2
	`, user.TwitchUserID, relationType)
	if err != nil {
		return nil, fmt.Errorf("failed to get relations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var relation Relation
		if err := rows.Scan(
			&relation.ID,
			&relation.TwitchLogin1,
			&relation.TwitchUserID1,
			&relation.TwitchLogin2,
			&relation.TwitchUserID2,
			&relation.RelationType,
		); err != nil {
			return nil, fmt.Errorf("failed to scan relation: %w", err)
		}

		relations = append(relations, relation)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get relations: %w", err)
	}

	return relations, nil
}
