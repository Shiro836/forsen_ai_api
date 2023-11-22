package db

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"sort"
	"strings"

	"golang.org/x/exp/slices"
	_ "modernc.org/sqlite"
)

var db *sql.DB

func Close() {
	db.Close()
}

func InitDB() {
	db = CreateDb("sqlite.db")
}

func CreateDb(filePath string) *sql.DB {
	db, err := sql.Open("sqlite", filePath)
	if err != nil {
		log.Fatal(err)
	}

	folder := "db/migrations"

	migrations, err := os.ReadDir(folder)
	if err != nil {
		folder = "migrations"
		migrations, err = os.ReadDir(folder)
		if err != nil {
			log.Fatal(err)
		}
	}

	files := []string{}
	for _, migration := range migrations {
		files = append(files, migration.Name())
	}

	sort.Strings(files)

	for _, file := range files {
		f, err := os.Open(folder + "/" + file)
		if err != nil {
			log.Fatal(err)
		}

		data, err := io.ReadAll(f)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("applying", file)

		_, err = db.Exec(string(data))
		if err != nil {
			log.Fatal(err)
		}
	}

	return db
}

type UserLoginData struct {
	UserId   int
	UserName string
}

type UserData struct {
	ID int

	RefreshToken  string
	AccessToken   string
	UserLoginData *UserLoginData

	Session string
}

func UpsertUserData(userData *UserData) error {
	_, err := db.Exec(`
		insert into user_data(
			login,
			user_id,
			refresh_token,
			access_token,
			session
		) values (
			$1,
			$2,
			$3,
			$4,
			$5
		) on conflict(user_id) do update set
			login = excluded.login,
			refresh_token = excluded.refresh_token,
			access_token = excluded.access_token,
			session = excluded.session
		where excluded.user_id = user_data.user_id;
	`,
		userData.UserLoginData.UserName,
		userData.UserLoginData.UserId,
		userData.RefreshToken,
		userData.AccessToken,
		userData.Session,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert user data: %w", err)
	}

	return nil
}

func UpdateUserData(userData *UserData) error {
	_, err := db.Exec(`
		update user_data set 
			login = $1,
			refresh_token = $2,
			access_token = $3
		where
			user_id = $4
		`,
		userData.UserLoginData.UserName,
		userData.RefreshToken,
		userData.AccessToken,
		userData.UserLoginData.UserId,
	)

	if err != nil {
		return fmt.Errorf("failed to update user data: %w", err)
	}

	return nil
}

func GetUserData(user string) (*UserData, error) {
	row := db.QueryRow(`
		select
			id,
			user_id,
			refresh_token,
			access_token
		from user_data
		where
			login = $1
	`,
		user,
	)

	userData := &UserData{UserLoginData: &UserLoginData{UserName: user}}
	if err := row.Scan(
		&userData.ID,
		&userData.UserLoginData.UserId,
		&userData.RefreshToken,
		&userData.AccessToken,
	); err != nil {
		return nil, fmt.Errorf("failed to scan user data: %w", err)
	}

	return userData, nil
}

func GetUserDataBySessionId(sessionId string) (*UserData, error) {
	row := db.QueryRow(`
		select
			id,
			login,
			user_id,
			refresh_token,
			access_token
		from user_data
		where
			session = $1
	`,
		sessionId,
	)

	userData := &UserData{UserLoginData: &UserLoginData{}, Session: sessionId}
	if err := row.Scan(
		&userData.ID,
		&userData.UserLoginData.UserName,
		&userData.UserLoginData.UserId,
		&userData.RefreshToken,
		&userData.AccessToken,
	); err != nil {
		return nil, fmt.Errorf("failed to scan user data: %w", err)
	}

	return userData, nil
}

func GetRewardID(user string) (string, error) {
	row := db.QueryRow(`
		select reward_id from user_data where login = $1
	`, user)

	var rewardID string
	if err := row.Scan(&rewardID); err != nil {
		return "", fmt.Errorf("failed to scan reward id: %w", err)
	}

	return rewardID, nil
}

type Reward struct {
	RewardID       string
	TwitchRewardID string
}

func GetRewards(userID int) ([]Reward, error) {
	row, err := db.Query(`
		select reward_id, twitch_reward_id from reward_ids where user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("get rewards query err: %w", err)
	}

	rewards := make([]Reward, 0, 5)

	var nextReward Reward
	for row.Next() {
		if err := row.Scan(&nextReward.RewardID, &nextReward.TwitchRewardID); err != nil {
			return nil, fmt.Errorf("get rewards scan err: %w", err)
		}
		rewards = append(rewards, nextReward)
	}

	if row.Err() != nil {
		return nil, fmt.Errorf("get rewards scan err: %w", err)
	}

	return rewards, nil
}

func SaveRewardID(rewardID, twitchRewardID string, userID int) error {
	_, err := db.Exec(`
	insert into reward_ids(user_id, reward_id, twitch_reward_id)
	values($1, $2, $3)
	on conflict(user_id, reward_id) do update set
		twitch_reward_id = $3
	where reward_ids.user_id = excluded.user_id and reward_ids.reward_id = excluded.reward_id
	`,
		userID,
		rewardID,
		twitchRewardID,
	)

	return err
}

type Settings struct {
	LuaScript string `json:"lua_script"`
}

func GetDbSettings(login string) (*Settings, error) {
	row := db.QueryRow(`
		select
			settings
		from user_data
		where login = $1
	`, login)

	var settingsData []byte
	if err := row.Scan(&settingsData); err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	if len(settingsData) == 0 {
		return &Settings{}, nil
	}

	var settings *Settings
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal settings: %w", err)
	}

	return settings, nil
}

func UpdateDbSettings(settings *Settings, login string) error {
	settingsData, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if _, err := db.Exec(`
		update user_data set
			settings = $1
		where login = $2
	`,
		settingsData,
		login,
	); err != nil {
		return fmt.Errorf("failed to update db settings: %w", err)
	}

	return nil
}

type Human struct {
	Login    string  `json:"login"`
	IsMod    bool    `json:"is_mod"`
	AddedBy  string  `json:"added_by"`
	BannedBy *string `json:"banned_by"`
}

func (h *Human) String() string {
	if h == nil {
		return "nil"
	}

	return fmt.Sprintf("Login=%s IsMod=%t AddedBy=%s BannedBy=%v", h.Login, h.IsMod, h.AddedBy, h.BannedBy)
}

type Whitelist struct {
	List []*Human `json:"list"`
}

func GetDbWhitelist() (*Whitelist, error) {
	rows, err := db.Query(`
		select 
			login,
			is_mod,
			added_by,
			banned_by
		from whitelist
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get whitelist: %w", err)
	}

	res := make([]*Human, 0, 50)

	for rows.Next() {
		nextHuman := &Human{}

		if err = rows.Scan(
			&nextHuman.Login,
			&nextHuman.IsMod,
			&nextHuman.AddedBy,
			&nextHuman.BannedBy,
		); err != nil {
			return nil, fmt.Errorf("failed to scan whitelist entry: %w", err)
		}

		res = append(res, nextHuman)
	}

	if rows.Err() != nil {
		return nil, rows.Err()
	}

	return &Whitelist{List: res}, nil
}

type WhitelistUpdate struct {
	Login string `json:"login"`
	IsAdd bool   `json:"is_add"`
}

func UpdateDbWhitelist(upd *WhitelistUpdate, adder string) error {
	whitelist, err := GetDbWhitelist()
	if err != nil {
		return fmt.Errorf("failed to get whitelist: %w", err)
	}
	if !slices.ContainsFunc(whitelist.List, func(h *Human) bool {
		return strings.EqualFold(adder, h.Login) && h.IsMod
	}) {
		return fmt.Errorf("unauthorized")
	}
	if slices.ContainsFunc(whitelist.List, func(h *Human) bool {
		return strings.EqualFold(upd.Login, h.Login) && h.IsMod
	}) {
		return fmt.Errorf("can't update mod")
	}

	if upd.IsAdd {
		if _, err := db.Exec(`
			insert into whitelist(login, is_mod, added_by, banned_by)
			values($1, false, $2, null)
			on conflict(login) do update set
				banned_by = null
			where whitelist.login = excluded.login
		`,
			upd.Login,
			adder,
		); err != nil {
			return fmt.Errorf("failed to update add whitelist")
		} else {
			return nil
		}
	} else {
		if _, err := db.Exec(`
			insert into whitelist(login, is_mod, added_by, banned_by)
			values($1, false, $2, $3)
			on conflict(login) do update set
				banned_by = excluded.banned_by
			where whitelist.login = excluded.login
		`,
			upd.Login,
			adder,
			adder,
		); err != nil {
			return fmt.Errorf("failed to update ban whitelist")
		} else {
			return nil
		}
	}
}

type Card struct {
	CharName string
	Card     []byte
}

func GetAllCards(userID int) ([]*Card, error) {
	rows, err := db.Query(`
	select char_name, card from char_cards
	where user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get all char cards: %w", err)
	}

	cards := make([]*Card, 0, 10)

	var charName, cardStr string
	for rows.Next() {
		if err := rows.Scan(&charName, &cardStr); err != nil {
			return nil, fmt.Errorf("failed to scan all char cards: %w", err)
		} else if cardData, err := base64.StdEncoding.DecodeString(cardStr); err != nil {
			slog.Error("corrupted card", "err", err)
		} else {
			cards = append(cards, &Card{
				CharName: charName,
				Card:     cardData,
			})
		}
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("failed to scan all char cards: %w", err)
	}

	return cards, nil
}

func AddCharCard(userID int, charName string, cardData []byte) error {
	if _, err := db.Exec(`
	insert into char_cards(
		user_id,
		char_name,
		card
	) values (
		$1,
		$2,
		$3
	)
	`,
		userID,
		charName,
		base64.StdEncoding.EncodeToString(cardData),
	); err != nil {
		return fmt.Errorf("failed to insert char card: %w", err)
	}

	return nil
}
