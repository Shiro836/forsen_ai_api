package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	migrations, err := os.ReadDir("db/migrations")
	if err != nil {
		migrations, err = os.ReadDir("migrations")
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
		f, err := os.Open("migrations/" + file)
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

func GetRewardIDBySessionID(sessionId string) (string, error) {
	row := db.QueryRow(`
		select reward_id from user_data where session = $1
	`, sessionId)

	var rewardID string
	if err := row.Scan(&rewardID); err != nil {
		return "", fmt.Errorf("failed to scan reward id: %w", err)
	}

	return rewardID, nil
}

func SaveRewardID(rewardID, sessionId string) error {
	_, err := db.Exec(`
		update user_data set
			reward_id = $1
		where session = $2
	`,
		rewardID,
		sessionId,
	)

	return err
}

type Settings struct {
	Chat           bool `json:"chat"`
	ChannelPts     bool `json:"chan_pts"`
	Follows        bool `json:"follows"`
	Subs           bool `json:"subs"`
	Raids          bool `json:"raids"`
	Events         bool `json:"events"`
	EventsInterval int  `json:"events_interval"`
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

	var settings *Settings
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal settings: %w", err)
	}

	return settings, nil
}

func UpdateDbSettings(settings *Settings, sessionId string) error {
	settingsData, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if _, err := db.Exec(`
		update user_data set
			settings = $1
		where session = $2
	`,
		settingsData,
		sessionId,
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
