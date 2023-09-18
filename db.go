package main

import (
	"database/sql"
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

func init() {
	var err error

	db, err = sql.Open("sqlite", "sqlite.db")
	if err != nil {
		log.Fatal(err)
	}

	migrations, err := os.ReadDir("migrations")
	if err != nil {
		log.Fatal(err)
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

		_, err = db.Exec(string(data))
		if err != nil {
			log.Fatal(err)
		}
	}
}

func updateUserData(userData *UserData) error {
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

func getUserDataBySessionId(sessionId string) (*UserData, error) {
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

func getRewardID(sessionId string) (string, error) {
	row := db.QueryRow(`
		select reward_id from user_data where session = $1
	`, sessionId)

	var rewardID string
	if err := row.Scan(&rewardID); err != nil {
		return "", fmt.Errorf("failed to scan reward id: %w", err)
	}

	return rewardID, nil
}

func saveRewardID(rewardID, sessionId string) error {
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

func getDbSettings(sessionId string) (*Settings, error) {
	row := db.QueryRow(`
		select
			chat,
			chan_pts,
			follows,
			subs,
			raids,
			random_events,
			events_interval
		from user_data
		where session = $1
	`, sessionId)

	settings := &Settings{}

	if err := row.Scan(
		&settings.Chat,
		&settings.ChannelPts,
		&settings.Follows,
		&settings.Subs,
		&settings.Raids,
		&settings.Events,
		&settings.EventsInterval,
	); err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}

	return settings, nil
}

func updateDbSettings(settings *Settings, sessionId string) error {
	if _, err := db.Exec(`
		update user_data set
			chat     		= $1,
			chan_pts 		= $2,
			follows  		= $3,
			subs     		= $4,
			raids	 		= $5,
			random_events	= $6,
			events_interval	= $7
		where session = $8
	`,
		settings.Chat,
		settings.ChannelPts,
		settings.Follows,
		settings.Subs,
		settings.Raids,
		settings.Events,
		settings.EventsInterval,
		sessionId,
	); err != nil {
		return fmt.Errorf("failed to update db settings: %w", err)
	}

	return nil
}

type human struct {
	Login    string  `json:"login"`
	IsMod    bool    `json:"is_mod"`
	AddedBy  string  `json:"added_by"`
	BannedBy *string `json:"banned_by"`
}

type whitelist struct {
	List []*human `json:"list"`
}

func getDbWhitelist() (*whitelist, error) {
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

	res := make([]*human, 0, 50)

	for rows.Next() {
		nextHuman := &human{}

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

	return &whitelist{List: res}, nil
}

type whitelistUpdate struct {
	Login string `json:"login"`
	IsAdd bool   `json:"is_add"`
}

func updateDbWhitelist(upd *whitelistUpdate, adder string) error {
	whitelist, err := getDbWhitelist()
	if err != nil {
		return fmt.Errorf("failed to get whitelist: %w", err)
	}
	if !slices.ContainsFunc(whitelist.List, func(h *human) bool {
		return strings.EqualFold(adder, h.Login) && h.IsMod
	}) {
		return fmt.Errorf("unauthorized")
	}
	if slices.ContainsFunc(whitelist.List, func(h *human) bool {
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
