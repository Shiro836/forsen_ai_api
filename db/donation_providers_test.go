package db

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDonationProviderSQLKeepsTokensBound(t *testing.T) {
	id := uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")
	refresh := "refresh-me"

	sql, args, err := upsertDonationProviderSQL(id, ProviderStreamlabs, "access-me", &refresh, []string{"socket.token"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "access-me") || strings.Contains(sql, "refresh-me") {
		t.Fatalf("token leaked into SQL: %s", sql)
	}
	lower := strings.ToLower(sql)
	if !strings.Contains(lower, "insert into donation_providers") {
		t.Fatalf("sql: %s", sql)
	}
	if !strings.Contains(lower, "on conflict (user_id, provider)") {
		t.Fatalf("sql: %s", sql)
	}
	if !slices.Contains(args, any("access-me")) || !slices.Contains(args, any(&refresh)) {
		t.Fatalf("args: %#v", args)
	}

	sql, _, err = connectedDonationProvidersSQL(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "access_token") || strings.Contains(sql, "refresh_token") {
		t.Fatalf("list query reads tokens: %s", sql)
	}
	if sql != "SELECT provider, status, status_error, expires_at FROM donation_providers WHERE user_id = $1" {
		t.Fatalf("sql: %s", sql)
	}
}

func TestUpsertDonationProviderRejectsBadInput(t *testing.T) {
	database := &DB{}
	id := uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")

	if err := database.UpsertDonationProvider(context.Background(), id, "twitch", "tok", nil, nil, nil); err == nil {
		t.Fatal("unknown provider")
	}
	if err := database.UpsertDonationProvider(context.Background(), id, ProviderStreamElements, "  ", nil, nil, nil); err == nil {
		t.Fatal("blank token")
	}
}

func TestDeleteDonationProviderKeepsUserBound(t *testing.T) {
	database := &DB{}
	id := uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")

	sql, args, err := deleteDonationProviderSQL(id, ProviderStreamlabs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(sql) != "delete from donation_providers where provider = $1 and user_id = $2" {
		t.Fatalf("sql: %s", sql)
	}
	if !slices.Contains(args, any(ProviderStreamlabs)) || !slices.Contains(args, any(id.String())) {
		t.Fatalf("args: %#v", args)
	}
	if err := database.DeleteDonationProvider(context.Background(), id, "twitch"); err == nil {
		t.Fatal("unknown provider")
	}
}
