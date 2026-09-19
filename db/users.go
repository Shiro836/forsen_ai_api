package db

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultTtsLimitSeconds = 80
	DefaultMaxSfxCount     = 10
	DefaultSfxTotalLimit   = 20 // seconds; total SFX duration per universal TTS message (0 = unlimited)
	DefaultMaxSingCount    = 2
)

type User struct {
	ID uuid.UUID

	TwitchLogin  string
	TwitchUserID int
}

func (db *DB) UpsertUser(ctx context.Context, user *User) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.QueryRow(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id)
		VALUES ($1, $2)
		ON CONFLICT (twitch_user_id) DO UPDATE SET
			twitch_login = excluded.twitch_login
		RETURNING id
	`, user.TwitchLogin, user.TwitchUserID).Scan(&id)

	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert user: %w", err)
	}

	return id, nil
}

func (db *DB) GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id
		FROM users
		WHERE id = $1
	`, userID).Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", parseErr(err))
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchLogin(ctx context.Context, twitchLogin string) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id
		FROM users
		WHERE lower(twitch_login) = lower($1)
	`, twitchLogin).Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by twitch login: %w", parseErr(err))
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchUserID(ctx context.Context, twitchUserID int) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id
		FROM users
		WHERE twitch_user_id = $1
	`, twitchUserID).Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by twitch user id: %w", parseErr(err))
	}

	return &user, nil
}

type UserSettings struct {
	Filters        string        `json:"filters"`
	RequestTimeout time.Duration `json:"requestTimeout"`
	TtsLimit       *int          `json:"tts_limit,omitempty"`       // Maximum TTS audio length in seconds (nil = not set, 0 = use default 80s)
	MaxSfxCount    *int          `json:"max_sfx_count,omitempty"`   // Maximum number of SFX that can be used in a single TTS message (nil = not set, 0 = unlimited)
	SfxTotalLimit  *int          `json:"sfx_total_limit,omitempty"` // Maximum cumulative SFX duration in seconds per universal TTS message (nil = not set, 0 = unlimited; default 20s)
	MaxSingCount   *int          `json:"max_sing_count,omitempty"`  // Maximum number of sung spans per universal TTS message (nil = not set, 0 = unlimited; default 2)
	Token          string        `json:"token,omitempty"`

	IngestAllMessages bool `json:"ingest_all_messages,omitempty"` // When true, ingest all chat messages, not just reward redemptions

	// Off leaves redemptions open on Twitch, where mods can still refund them
	// by hand; a completed redemption cannot be refunded.
	CloseRedemptions bool `json:"close_redemptions,omitempty"`

	DisableAudioNormalization bool `json:"disable_audio_normalization,omitempty"` // When true, skip loudnorm and alimiter on TTS audio

	DisableLLMFilter bool `json:"disable_llm_filter,omitempty"` // When true, skip the LLM-based content filter

	DisableRegexFilter bool `json:"disable_regex_filter,omitempty"` // When true, skip the regex/word-list content filter

	CustomFilterPrompt string `json:"custom_filter_prompt,omitempty"` // Streamer-written instructions appended to the LLM filter system prompt

	DisabledCardIDs []uuid.UUID `json:"disabled_card_ids,omitempty"` // Characters the streamer has switched off: not a voice, not a dialogue participant

	PlayGroups   [][]MsgClass              `json:"play_order,omitempty"`
	EventActions map[MsgClass]*EventAction `json:"event_actions,omitempty"`
	LanesEnabled map[MsgClass]bool         `json:"lanes_enabled,omitempty"`
	EventLines   map[EventLine]string      `json:"event_lines,omitempty"`
	// FollowsStay is the unticked "drop on higher tier events": follows are
	// free and bot-able, so by default they yield like chat TTS does.
	FollowsStay bool `json:"follows_stay,omitempty"`
}

type EventAction struct {
	RewardType TwitchRewardType `json:"reward_type"`
	CardID     *uuid.UUID       `json:"card_id,omitempty"`
}

var defaultPlayGroups = [][]MsgClass{{MsgClassDonation, MsgClassBits}, {MsgClassSub}, {MsgClassReward}, {MsgClassRaid}, {MsgClassStreak}, {MsgClassFollow}}

// Paid classes carry a money amount, so only they can share a play group.
func (c MsgClass) Paid() bool {
	return c == MsgClassDonation || c == MsgClassBits
}

func validPlayGroups(groups [][]MsgClass) bool {
	ranked := slices.Concat(defaultPlayGroups...)

	var seen []MsgClass
	for _, group := range groups {
		if len(group) == 0 {
			return false
		}
		for _, class := range group {
			if !slices.Contains(ranked, class) || slices.Contains(seen, class) || (len(group) > 1 && !class.Paid()) {
				return false
			}
			seen = append(seen, class)
		}
	}
	return len(seen) > 0
}

// PlayOrder is the stored order with the lanes it does not rank yet appended
// in their default order, so a lane added later shows up for everyone.
func (s *UserSettings) PlayOrder() [][]MsgClass {
	groups := s.PlayGroups
	if !validPlayGroups(groups) {
		groups = defaultPlayGroups
	}

	order := make([][]MsgClass, len(groups))
	for i, group := range groups {
		order[i] = slices.Clone(group)
	}

	stored := slices.Concat(groups...)
	for _, class := range slices.Concat(defaultPlayGroups...) {
		if !slices.Contains(stored, class) {
			order = append(order, []MsgClass{class})
		}
	}
	return order
}

func (a EventAction) Complete() bool {
	return a.RewardType == TwitchRewardUniversalTTS || a.CardID != nil
}

func (s *UserSettings) EventAction(class MsgClass) EventAction {
	if action := s.EventActions[class]; action != nil && action.Complete() {
		return *action
	}
	return EventAction{RewardType: TwitchRewardUniversalTTS}
}

func (s *UserSettings) CardDisabled(cardID uuid.UUID) bool {
	return slices.Contains(s.DisabledCardIDs, cardID)
}

func (db *DB) SetCardDisabled(ctx context.Context, userID, cardID uuid.UUID, disabled bool) error {
	settings, err := db.GetUserSettings(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	ids := slices.DeleteFunc(settings.DisabledCardIDs, func(id uuid.UUID) bool { return id == cardID })
	if disabled {
		ids = append(ids, cardID)
	}
	settings.DisabledCardIDs = ids

	return db.UpdateUserData(ctx, userID, settings)
}

func (db *DB) UpdateUserData(ctx context.Context, userID uuid.UUID, settings *UserSettings) error {
	_, err := db.Exec(ctx, `
		UPDATE users
		SET
			data = $1
		WHERE id = $2
	`, settings, userID)
	if err != nil {
		return fmt.Errorf("failed to update user data: %w", err)
	}

	return nil
}

// GenerateUserToken sets a new API token in user's settings and persists it.
func (db *DB) GenerateUserToken(ctx context.Context, userID uuid.UUID) (string, error) {
	settings, err := db.GetUserSettings(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("failed to load settings: %w", err)
	}

	token, err := generateToken()
	if err != nil {
		return "", err
	}

	settings.Token = token

	if err := db.UpdateUserData(ctx, userID, settings); err != nil {
		return "", err
	}

	return token, nil
}

// generateToken returns a 10-char token using allowed chars only: 4-9 and a-z
func generateToken() (string, error) {
	const length = 10
	const letters = "abcdefghijklmnopqrstuvwxyz456789"

	b := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := randInt(len(letters))
		if err != nil {
			return "", fmt.Errorf("generate token: %w", err)
		}
		b[i] = letters[n]
	}
	return string(b), nil
}

// randInt returns crypto-strong random int in [0, max)
func randInt(max int) (int, error) {
	if max <= 0 {
		return 0, fmt.Errorf("invalid max")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	// Use uint64 to avoid modulo bias concerns at this scale
	v := binary.LittleEndian.Uint64(b[:])
	return int(v % uint64(max)), nil
}

func (db *DB) GetUserSettings(ctx context.Context, userID uuid.UUID) (*UserSettings, error) {
	var settings UserSettings

	err := db.QueryRow(ctx, `
		SELECT
			data
		FROM users
		WHERE id = $1
	`, userID).Scan(&settings)
	if err != nil {
		return nil, fmt.Errorf("failed to get user settings: %w", err)
	}

	// Set default values for new fields that might not exist in old user data
	if settings.TtsLimit == nil {
		defaultTtsLimit := DefaultTtsLimitSeconds
		settings.TtsLimit = &defaultTtsLimit
	}

	if settings.MaxSfxCount == nil {
		defaultMaxSfxCount := DefaultMaxSfxCount
		settings.MaxSfxCount = &defaultMaxSfxCount // Default to 10 SFX per message
	}

	if settings.SfxTotalLimit == nil {
		defaultSfxTotal := DefaultSfxTotalLimit
		settings.SfxTotalLimit = &defaultSfxTotal // Default to 20 seconds total SFX
	}

	if settings.MaxSingCount == nil {
		defaultMaxSing := DefaultMaxSingCount
		settings.MaxSingCount = &defaultMaxSing
	}

	return &settings, nil
}
