package main

import (
	"context"

	"golang.org/x/exp/slog"
)

type slogStruct struct {
	Name string
}

var slogKey = &slogStruct{Name: "slog"}

func GetSlog(ctx context.Context) *slog.Logger {
	return ctx.Value(slogKey).(*slog.Logger)
}

func WithSlog(ctx context.Context, log *slog.Logger) context.Context {
	return context.WithValue(ctx, slogKey, log)
}
