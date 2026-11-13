package db

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var (
	ErrNoRows        = &dbErr{Code: ErrCodeNoRows, Err: errors.New("no rows in result set")}
	ErrAlreadyExists = &dbErr{Code: ErrCodeAlreadyExists, Err: errors.New("already exists")}
)

type dbErr struct {
	Code int
	Err  error
}

func (e *dbErr) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%d", e.Code)
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Err.Error())
}

func (e *dbErr) Unwrap() error {
	return e.Err
}

const (
	ErrCodeUnknown = iota
	ErrCodeNoRows
	ErrCodeAlreadyExists
)

func ErrCode(e error) int {
	var err *dbErr
	if ok := errors.As(e, &err); ok {
		return err.Code
	}

	return ErrCodeUnknown
}

func parseErr(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}

	return err
}
