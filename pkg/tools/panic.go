package tools

import (
	"log/slog"
	"runtime/debug"
)

// LogPanic, deferred first in a spawned goroutine, turns a panic there into an
// error log instead of a process exit. Later defers in that goroutine still run.
func LogPanic(logger *slog.Logger, what string) {
	if r := recover(); r != nil {
		logger.Error("panic recovered", "in", what, "panic", r, "stack", string(debug.Stack()))
	}
}
