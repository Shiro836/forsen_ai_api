package db

import (
	"context"
	"fmt"
	"time"
)

type Card struct {
	ID int

	OwnerUserID     int
	CharName        string
	CharDescription string
	Public          bool
	Redeems         int

	UpdatedAt time.Time
	CreatedAt time.Time

	Data *CardData
}

type MessageExample struct {
	Request  string `json:"request"`
	Response string `json:"response"`
}

type CardData struct {
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	Personality     string           `json:"personality"`
	FirstMessage    string           `json:"first_message"`
	MessageExamples []MessageExample `json:"message_examples"`
	SystemPrompt    string           `json:"system_prompt"`
}

func (db *DB) GetCharCard(ctx context.Context, userID int, twitchRewardID string) (*Card, error) {
	var card Card
	err := db.db.QueryRow(ctx, `
		select
			cc.id,
			cc.owner_user_id,
			cc.char_name,
			cc.char_description,
			cc.public,
			cc.redeems,
			cc.updated_at,
			cc.created_at,
			cc.data
		from char_cards cc join reward_buttons rb on cc.id = rb.card_id
		where
			rb.twitch_reward_id = $1
			and (
				rb.user_id = $2
				or cc.public = true
			)
	`, twitchRewardID, userID).Scan(
		&card.ID,
		&card.OwnerUserID,
		&card.CharDescription,
		&card.CharName,
		&card.Public,
		&card.Redeems,
		&card.UpdatedAt,
		&card.CreatedAt,
		&card.Data,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get char card: %w", err)
	}

	return &card, nil
}

func (db *DB) InsertCharCard(ctx context.Context, card *Card) error {
	_, err := db.db.Exec(ctx, `
		insert into char_cards (
			owner_user_id,
			char_name,
			char_description,
			public,
			data
		) values (
			$1,
			$2,
			$3,
			$4
		)
	`, card.OwnerUserID, card.CharName, card.CharDescription, card.Public, card.Data)
	if err != nil {
		return fmt.Errorf("failed to insert char card: %w", err)
	}

	return nil
}

func (db *DB) UpdateCharCard(ctx context.Context, card *Card) error {
	_, err := db.db.Exec(ctx, `
		update char_cards set
			owner_user_id = $1,
			char_name = $2,
			char_description = $3,
			public = $4,
			data = $5,
			updated_at = now()
		where id = $6
	`, card.OwnerUserID, card.CharName, card.CharDescription, card.Public, card.Data, card.ID)
	if err != nil {
		return fmt.Errorf("failed to update char card: %w", err)
	}

	return nil
}

func (db *DB) DeleteCharCard(ctx context.Context, cardID int) error {
	_, err := db.db.Exec(ctx, `
		delete from char_cards where id = $1
	`, cardID)
	if err != nil {
		return fmt.Errorf("failed to delete char card: %w", err)
	}

	return nil
}

type CharCardSortBy int

const (
	SortByCharName CharCardSortBy = iota
	SortByRedeems
	SortByNewest
	SortByOldest
)

type GetChatCardsParams struct {
	ShowPublic bool
	SortBy     CharCardSortBy
}

func (db *DB) GetCharCards(ctx context.Context, userID int, params GetChatCardsParams) ([]*Card, error) {
	rows, err := db.db.Query(ctx, `
		select
			id,
			owner_user_id,
			char_name,
			char_description,
			redeems,
			public,
			updated_at,
			created_at,
			data
		from char_cards
		where (
			owner_user_id = $1
			or (public = true and $2 = true)
		)
		order by
			case when $3 = 0 then id end asc,
			case when $3 = 1 then char_name end asc,
			case when $3 = 2 then redeems end desc,
			case when $3 = 3 then id end desc,
			case when $3 = 4 then id end asc
		limit 100
	`, userID, params.ShowPublic, params.SortBy)
	if err != nil {
		return nil, fmt.Errorf("failed to get char cards: %w", err)
	}
	defer rows.Close()

	var cards []*Card
	for rows.Next() {
		var card Card
		err := rows.Scan(
			&card.ID,
			&card.OwnerUserID,
			&card.CharName,
			&card.CharDescription,
			&card.Redeems,
			&card.Public,
			&card.UpdatedAt,
			&card.CreatedAt,
			&card.Data,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan char card: %w", err)
		}
		cards = append(cards, &card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get char cards: %w", err)
	}

	return cards, nil
}
