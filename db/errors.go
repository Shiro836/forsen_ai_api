package db

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type dbErr struct {
	Code int
	Err  error
}

func (e *dbErr) Error() string {
	return fmt.Sprintf("%d: %s", e.Code, e.Err.Error())
}

func (e *dbErr) Unwrap() error {
	return e.Err
}

const (
	ErrCodeUnknown = iota
	ErrCodeNoRows
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
		return &dbErr{
			Code: ErrCodeNoRows,
			Err:  err,
		}
	}

	return err
}
