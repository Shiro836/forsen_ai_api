package ctxstore

import (
	"app/db"
	"context"
)

type userDataStruct struct {
	Name string
}

var userDataKey = &userDataStruct{Name: "user_data"}

func WithUser(ctx context.Context, userData *db.User) context.Context {
	return context.WithValue(ctx, userDataKey, userData)
}

func GetUser(ctx context.Context) *db.User {
	val, ok := ctx.Value(userDataKey).(*db.User)
	if !ok {
		return nil
	}

	return val
}
