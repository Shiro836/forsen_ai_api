package db

import (
	"app/pkg/s3client"
	"app/pkg/tools"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Card struct {
	ID uuid.UUID

	OwnerUserID uuid.UUID

	Name        string
	Description string

	ShortCharName sql.NullString

	Public     bool
	Redeems    int
	TTSRedeems int

	UpdatedAt time.Time
	CreatedAt time.Time

	Data *CardData
}

// CharacterBasicInfo represents minimal character information for detection
type CharacterBasicInfo struct {
	ID          uuid.UUID
	Name        string
	ShortName   string
	Description string
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

	ImageID string `json:"image_id,omitempty"`
	VoiceID string `json:"voice_id,omitempty"`
}

type PublicShortName struct {
	ID            uuid.UUID
	ShortCharName string
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

	if data != nil && len(data.Image) == 0 && data.ImageID != "" && db.s3 != nil {
		rc, err := db.s3.GetObject(ctx, s3client.CharDataBucket, data.ImageID)
		if err == nil {
			b, rerr := io.ReadAll(rc)
			rc.Close()
			if rerr == nil {
				return b, nil
			}
		}
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
            cc.short_char_name,
			cc.public,
			cc.redeems,
			cc.tts_redeems,
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
		&card.ShortCharName,
		&card.Public,
		&card.Redeems,
		&card.TTSRedeems,
		&card.UpdatedAt,
		&card.Data,
	)
	if err != nil {
		return nil, fmt.Errorf("get char card by id: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	db.populateCardDataMedia(ctx, card.Data)

	return &card, nil
}

func (db *DB) GetCharCardByTwitchRewardNoPerms(ctx context.Context, twitchRewardID string) (*Card, TwitchRewardType, error) {
	var rewardType TwitchRewardType
	var card Card
	err := db.QueryRow(ctx, `
		select
			cc.id,
			cc.owner_user_id,
			cc.name,
			cc.description,
            cc.short_char_name,
			cc.public,
			cc.redeems,
			cc.tts_redeems,
			cc.updated_at,
			cc.data,
			rb.reward_type
		from char_cards cc join reward_buttons rb on cc.id = rb.card_id
		where
			rb.twitch_reward_id = $1
	`, twitchRewardID).Scan(
		&card.ID,
		&card.OwnerUserID,
		&card.Name,
		&card.Description,
		&card.ShortCharName,
		&card.Public,
		&card.Redeems,
		&card.TTSRedeems,
		&card.UpdatedAt,
		&card.Data,
		&rewardType,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get char card: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	db.populateCardDataMedia(ctx, card.Data)

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
            cc.short_char_name,
			cc.public,
			cc.redeems,
			cc.tts_redeems,
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
		&card.Name,
		&card.Description,
		&card.ShortCharName,
		&card.Public,
		&card.Redeems,
		&card.TTSRedeems,
		&card.UpdatedAt,
		&card.Data,
		&rewardType,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get char card: %w", parseErr(err))
	}

	card.CreatedAt = tools.UUIDToTime(card.ID)

	db.populateCardDataMedia(ctx, card.Data)

	return &card, rewardType, nil
}

func (db *DB) InsertCharCard(ctx context.Context, card *Card) (uuid.UUID, error) {
	var cardID uuid.UUID

	// store media to s3 if configured
	dataForStore := *card.Data
	if db.s3 != nil && card.Data != nil {
		if len(card.Data.Image) > 0 {
			imgID := uuid.New().String()
			if err := db.s3.PutObject(ctx, s3client.CharDataBucket, imgID, bytes.NewReader(card.Data.Image), int64(len(card.Data.Image)), "application/octet-stream"); err != nil {
				return uuid.Nil, fmt.Errorf("upload image to s3: %w", err)
			}
			dataForStore.ImageID = imgID
			dataForStore.Image = nil
		}
		if len(card.Data.VoiceReference) > 0 {
			voiceID := uuid.New().String()
			if err := db.s3.PutObject(ctx, s3client.CharDataBucket, voiceID, bytes.NewReader(card.Data.VoiceReference), int64(len(card.Data.VoiceReference)), "application/octet-stream"); err != nil {
				return uuid.Nil, fmt.Errorf("upload voice to s3: %w", err)
			}
			dataForStore.VoiceID = voiceID
			dataForStore.VoiceReference = nil
		}
	}

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
    `, card.OwnerUserID, card.Name, card.Description, card.Public, &dataForStore).Scan(&cardID); err != nil {
		return uuid.Nil, fmt.Errorf("failed to insert char card: %w", err)
	}

	return cardID, nil
}

func (db *DB) UpdateCharCard(ctx context.Context, userID uuid.UUID, card *Card) error {
	// store media to s3 if configured
	dataForStore := *card.Data
	if db.s3 != nil && card.Data != nil {
		if len(card.Data.Image) > 0 {
			imgID := uuid.New().String()
			if err := db.s3.PutObject(ctx, s3client.CharDataBucket, imgID, bytes.NewReader(card.Data.Image), int64(len(card.Data.Image)), "application/octet-stream"); err != nil {
				return fmt.Errorf("upload image to s3: %w", err)
			}
			dataForStore.ImageID = imgID
			dataForStore.Image = nil
		}
		if len(card.Data.VoiceReference) > 0 {
			voiceID := uuid.New().String()
			if err := db.s3.PutObject(ctx, s3client.CharDataBucket, voiceID, bytes.NewReader(card.Data.VoiceReference), int64(len(card.Data.VoiceReference)), "application/octet-stream"); err != nil {
				return fmt.Errorf("upload voice to s3: %w", err)
			}
			dataForStore.VoiceID = voiceID
			dataForStore.VoiceReference = nil
		}
	}

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
    `, userID, card.Name, card.Description, card.Public, &dataForStore, card.ID)
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

func (db *DB) populateCardDataMedia(ctx context.Context, data *CardData) {
	if db.s3 == nil || data == nil {
		return
	}
	if len(data.Image) == 0 && data.ImageID != "" {
		if rc, err := db.s3.GetObject(ctx, s3client.CharDataBucket, data.ImageID); err == nil {
			if b, rerr := io.ReadAll(rc); rerr == nil {
				data.Image = b
			}
			rc.Close()
		}
	}
	if len(data.VoiceReference) == 0 && data.VoiceID != "" {
		if rc, err := db.s3.GetObject(ctx, s3client.CharDataBucket, data.VoiceID); err == nil {
			if b, rerr := io.ReadAll(rc); rerr == nil {
				data.VoiceReference = b
			}
			rc.Close()
		}
	}
}

func (db *DB) GetCharCards(ctx context.Context, userID uuid.UUID, params GetChatCardsParams) ([]*Card, error) {
	rows, err := db.Query(ctx, `
		select
			id,
			owner_user_id,
			name,
			description,
            short_char_name,
			redeems,
			tts_redeems,
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
			&card.ShortCharName,
			&card.Redeems,
			&card.TTSRedeems,
			&card.Public,
			&card.UpdatedAt,
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

func (db *DB) SetShortCharName(ctx context.Context, cardID uuid.UUID, shortName *string) error {
	var arg any
	if shortName == nil || strings.TrimSpace(*shortName) == "" {
		arg = nil
	} else {
		arg = strings.TrimSpace(*shortName)
	}

	_, err := db.Exec(ctx, `
        update char_cards set
            short_char_name = $2,
            updated_at = now()
        where id = $1
    `, cardID, arg)
	if err != nil {
		return fmt.Errorf("failed to set short_char_name for card %s: %w", cardID, err)
	}

	return nil
}

func (db *DB) IncrementCharRedeems(ctx context.Context, cardID uuid.UUID) error {
	_, err := db.Exec(ctx, `
		update char_cards set
			redeems = redeems + 1,
			updated_at = now()
		where id = $1
	`, cardID)
	if err != nil {
		return fmt.Errorf("failed to increment redeems for card %s: %w", cardID, err)
	}
	return nil
}

func (db *DB) IncrementCharTTSRedeems(ctx context.Context, cardID uuid.UUID) error {
	_, err := db.Exec(ctx, `
		update char_cards set
			tts_redeems = tts_redeems + 1,
			updated_at = now()
		where id = $1
	`, cardID)
	if err != nil {
		return fmt.Errorf("failed to increment tts_redeems for card %s: %w", cardID, err)
	}
	return nil
}

func (db *DB) GetVoiceReferenceByShortName(ctx context.Context, shortName string) (uuid.UUID, *CardData, error) {
	shortName = strings.TrimSpace(shortName)
	if shortName == "" {
		return uuid.Nil, nil, fmt.Errorf("empty short_char_name")
	}

	shortName = strings.ToLower(shortName)

	var data *CardData
	var id uuid.UUID
	err := db.QueryRow(ctx, `
        select cc.id, cc.data
        from char_cards cc
        where lower(cc.short_char_name) = $1
          and cc.public = true
        limit 1
    `, shortName).Scan(&id, &data)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("get voice by short name: %w", parseErr(err))
	}

	if data == nil {
		return uuid.Nil, nil, fmt.Errorf("no data for short_char_name '%s'", shortName)
	}

	db.populateCardDataMedia(ctx, data)

	return id, data, nil
}

func (db *DB) GetPublicShortNamedCards(ctx context.Context) ([]PublicShortName, error) {
	rows, err := db.Query(ctx, `
        select id, short_char_name
        from char_cards
        where public = true
          and short_char_name is not null
          and length(trim(short_char_name)) > 0
        order by short_char_name asc
    `)
	if err != nil {
		return nil, fmt.Errorf("get public short named cards: %w", err)
	}
	defer rows.Close()

	var out []PublicShortName
	for rows.Next() {
		var rec PublicShortName
		if err := rows.Scan(&rec.ID, &rec.ShortCharName); err != nil {
			return nil, fmt.Errorf("scan public short named cards: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public short named cards: %w", err)
	}
	return out, nil
}

// GetAllCharacterBasicInfo returns basic info for all public characters for detection
func (db *DB) GetAllCharacterBasicInfo(ctx context.Context) ([]CharacterBasicInfo, error) {
	rows, err := db.Query(ctx, `
        select
			id,
			name,
			coalesce(short_char_name, ''),
			coalesce(description, '')
        from char_cards
        where public = true
        order by name asc
    `)
	if err != nil {
		return nil, fmt.Errorf("get all character basic info: %w", err)
	}
	defer rows.Close()

	var out []CharacterBasicInfo
	for rows.Next() {
		var char CharacterBasicInfo
		if err := rows.Scan(&char.ID, &char.Name, &char.ShortName, &char.Description); err != nil {
			return nil, fmt.Errorf("scan character basic info: %w", err)
		}
		out = append(out, char)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate character basic info: %w", err)
	}
	return out, nil
}
