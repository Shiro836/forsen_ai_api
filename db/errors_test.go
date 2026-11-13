package db

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrNoRowsFormats(t *testing.T) {
	if ErrNoRows.Error() == "" {
		t.Fatal("ErrNoRows has no message")
	}
	wrapped := fmt.Errorf("query: %w", ErrNoRows)
	if !errors.Is(wrapped, ErrNoRows) {
		t.Fatal("wrapping lost ErrNoRows")
	}
	if wrapped.Error() == "" {
		t.Fatal("wrapped ErrNoRows has no message")
	}
}
