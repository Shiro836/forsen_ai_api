package tools

import (
	"log/slog"
	"testing"
)

func TestLogPanicKeepsGoroutineDefers(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer LogPanic(slog.Default(), "test")
		defer close(done)
		var m map[string]int
		m["boom"] = 1
	}()
	<-done
}
