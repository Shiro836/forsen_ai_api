package db

import (
	"app/pkg/tools"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Card struct {
	ID uuid.UUID

	OwnerUserID uuid.UUID

	Name        string
	Description string

	Public  bool
	Redeems int

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
	MessageExamples []MessageExample `json:"message_examples"`
	FirstMessage    string           `json:"first_message"`
	SystemPrompt    string           `json:"system_prompt"`

	Image          []byte `json:"image"`
	VoiceReference []byte `json:"voice_reference"`
}

func (db *DB) GetCharImage(ctx context.Context, cardID uuid.UUID) ([]byte, error) {
	var data *CardData
	err := db.QueryRow(ctx, `
		select
			cc.data
		from char_cards cc
		where
			cc.id = $1
	`, cardID).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("get char image: %w", parseErr(err))
	}

	return data.Image, nil
}

func (db *DB) GetCharCardByID(ctx context.Context, userID uuid.UUID, cardID uuid.UUID) (*Card, error) {
	var card Card
	err := db.QueryRow(ctx, `
		select
			cc.id,
			cc.owner_user_id,
			cc.name,
			cc.description,
			cc.public,
			cc.redeems,
			cc.updated_at,
			cc.data
		from char_cards cc
		where
			cc.id = $1
		and
			(cc.owner_user_id = $2 or cc.public = true)
	`, cardID, userID).Scan(
		&card.ID,
		&card.OwnerUserID,
		&card.Name,
		&card.Description,
		&card.Public,
		&card.Redeems,
		&card.UpdatedAt,
		&card.Data,
	)
	if err != nil {
		return nil, fmt.Errorf("get char card by id: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	return &card, nil
}

func (db *DB) GetCharCardByTwitchRewardNoPerms(ctx context.Context, userID uuid.UUID, twitchRewardID string) (*Card, TwitchRewardType, error) {
	var rewardType TwitchRewardType
	var card Card
	err := db.QueryRow(ctx, `
		select
			cc.id,
			cc.owner_user_id,
			cc.name,
			cc.description,
			cc.public,
			cc.redeems,
			cc.updated_at,
			cc.data,
			rb.reward_type
		from char_cards cc join reward_buttons rb on cc.id = rb.card_id
		where
			rb.twitch_reward_id = $1
	`, twitchRewardID).Scan(
		&card.ID,
		&card.OwnerUserID,
		&card.Description,
		&card.Name,
		&card.Public,
		&card.Redeems,
		&card.UpdatedAt,
		&card.Data,
		&rewardType,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get char card: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	return &card, rewardType, nil
}

func (db *DB) GetCharCardByTwitchReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) (*Card, TwitchRewardType, error) {
	var rewardType TwitchRewardType
	var card Card
	err := db.QueryRow(ctx, `
		select
			cc.id,
			cc.owner_user_id,
			cc.name,
			cc.description,
			cc.public,
			cc.redeems,
			cc.updated_at,
			cc.data,
			rb.reward_type
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
		&card.Description,
		&card.Name,
		&card.Public,
		&card.Redeems,
		&card.UpdatedAt,
		&card.Data,
		&rewardType,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get char card: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	return &card, rewardType, nil
}

func (db *DB) InsertCharCard(ctx context.Context, card *Card) (uuid.UUID, error) {
	var cardID uuid.UUID

	if err := db.QueryRow(ctx, `
		insert into char_cards (
			owner_user_id,
			name,
			description,
			public,
			data
		) values (
			$1,
			$2,
			$3,
			$4,
			$5
		)
		RETURNING id
	`, card.OwnerUserID, card.Name, card.Description, card.Public, card.Data).Scan(&cardID); err != nil {
		return uuid.Nil, fmt.Errorf("failed to insert char card: %w", err)
	}

	return cardID, nil
}

func (db *DB) UpdateCharCard(ctx context.Context, userID uuid.UUID, card *Card) error {
	_, err := db.Exec(ctx, `
		update char_cards set
			name = $2,
			description = $3,
			public = $4,
			data = $5,
			updated_at = now()
		where
			id = $6
		and
			owner_user_id = $1
	`, userID, card.Name, card.Description, card.Public, card.Data, card.ID)
	if err != nil {
		return fmt.Errorf("failed to update char card: %w", err)
	}

	return nil
}

func (db *DB) DeleteCharCard(ctx context.Context, cardID int) error {
	_, err := db.Exec(ctx, `
		delete from char_cards where id = $1
	`, cardID)
	if err != nil {
		return fmt.Errorf("failed to delete char card: %w", err)
	}

	return nil
}

type CharCardSortBy int

const (
	SortByDefault CharCardSortBy = iota
	SortByName
	SortByRedeems
	SortByNewest
	SortByOldest
)

type GetChatCardsParams struct {
	ShowPublic bool
	SortBy     CharCardSortBy
}

func (db *DB) GetCharCards(ctx context.Context, userID uuid.UUID, params GetChatCardsParams) ([]*Card, error) {
	rows, err := db.Query(ctx, `
		select
			id,
			owner_user_id,
			name,
			description,
			redeems,
			public,
			updated_at
			-- data -- very heavy
		from char_cards
		where (
			owner_user_id = $1
			or (public = true and $2 = true)
		)
		order by
			case when $3 = 0 then id end asc,
			case when $3 = 1 then name end asc,
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
			&card.Name,
			&card.Description,
			&card.Redeems,
			&card.Public,
			&card.UpdatedAt,
			// &card.Data,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan char card: %w", err)
		}

		card.CreatedAt = tools.UUIDToTime(card.ID)

		cards = append(cards, &card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get char cards: %w", err)
	}

	return cards, nil
}
