package db

import "fmt"

const (
	CustomCharPrivate CustomCharState = iota
	CustomCharPublic
)

type CustomCharState int

func UpdateCustomCharState(id int, state CustomCharState) error {
	_, err := db.Exec(`
		update
			custom_chars
		set
			state=$1
		where
			id=$2
	`,
		state,
		id,
	)

	if err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	return nil
}

func GetAllCustomChars(userID int) ([]string, error) {
	chars := make([]string, 0, 100)

	rows, err := db.Query(`
		select
			ud.login,
			cc.char_name
		from
			custom_chars as cc join user_data as ud on(cc.user_id = ud.id)
		where
			cc.user_id = $1
		or
			cc.state = 1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query custom chars: %w", err)
	}

	var char string
	for rows.Next() {
		if err := rows.Scan(&char); err != nil {
			return nil, fmt.Errorf("failed to scan custom chars: %w", err)
		}

		chars = append(chars, char)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("custom chars Next err: %w", err)
	}

	return chars, nil
}

func GetCustomChars(userID int) ([]string, error) {
	chars := make([]string, 0, 10)

	rows, err := db.Query(`
		select
			char_name
		from
			custom_chars
		where
			user_id = $1
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to query custom chars: %w", err)
	}

	var char string
	for rows.Next() {
		if err := rows.Scan(&char); err != nil {
			return nil, fmt.Errorf("failed to scan custom chars: %w", err)
		}

		chars = append(chars, char)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("custom chars Next err: %w", err)
	}

	return chars, nil
}

func DeleteCustomChar(userID int, charName string) error {
	if _, err := db.Exec(`
		DELETE
			FROM custom_chars
		WHERE
			user_id = $1 and char_name = $2
	`, userID, charName); err != nil {
		return fmt.Errorf("failed to delete custom char: %w", err)
	}

	return nil
}

func AddCustomChar(userID int, charName string) error {
	if _, err := db.Exec(`
		insert into custom_chars(
			user_id,
			char_name
		) values (
			$1,
			$2
		) on conflict do nothing
		`,
		userID,
		charName,
	); err != nil {
		return fmt.Errorf("failed to insert custom char: %w", err)
	}

	return nil
}

func GetCustomCharState(userID int) (CustomCharState, error) {
	row := db.QueryRow(`
		select
			state
		from
			custom_chars
		where
			user_id = $1
	`, userID)

	var state CustomCharState
	if err := row.Scan(&state); err != nil {
		return state, fmt.Errorf("failed to scan state of custom char: %w", err)
	}

	return state, nil
}
