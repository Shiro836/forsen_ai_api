package main

import (
	"context"
	"fmt"

	"app/conns"
	"app/db"
	"app/pkg/slg"
)

func ProcessingLoop(ctx context.Context, cm *conns.Manager) error {
	users, err := db.GetDbWhitelist()
	if err != nil {
		return fmt.Errorf("failed to get whitelist: %w", err)
	}

	slg.GetSlog(ctx).Info("got users from db", "users", users.List)

	func() {
		for _, user := range users.List {
			cm.HandleUser(user)
		}
	}()

	cm.Wait()

	return nil
}
